package documents

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
)

// The core API client. Every call carries the module token; a call a viewer triggers
// also carries that viewer's context token, so the core answers exactly what the viewer
// may see (PRD 9.4).

// coreError is a non-success answer from the core, with its Problem code.
type coreError struct {
	Status int
	Code   string
	Detail string
	Path   string
}

func (e *coreError) Error() string {
	return fmt.Sprintf("core %s: %d %s %s", e.Path, e.Status, e.Code, e.Detail)
}

// isStatus reports whether err is a core answer with that HTTP status.
func isStatus(err error, status int) bool {
	var ce *coreError
	return errors.As(err, &ce) && ce.Status == status
}

// isCode reports whether err is a core answer with that Problem code.
func isCode(err error, code string) bool {
	var ce *coreError
	return errors.As(err, &ce) && ce.Code == code
}

func readProblem(resp *http.Response, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &p)
	return &coreError{Status: resp.StatusCode, Code: p.Code, Detail: p.Detail, Path: path}
}

// newID returns a UUIDv7, the core's id form.
func newID(ms int64) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(ms))
	copy(b[:6], ts[2:])
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Server) id() string { return newID(s.c.Now().UnixMilli()) }

// request sends one call to the core and returns the open response on success.
func (s *Server) request(ctx context.Context, method, path, tok string, hdr map[string]string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.c.Core, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.c.Token)
	if tok != "" {
		req.Header.Set(modkit.ContextHeader, tok)
	}
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", s.id())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, readProblem(resp, path)
	}
	return resp, nil
}

// call sends a JSON call and decodes the JSON answer into out.
func (s *Server) call(ctx context.Context, method, path, tok string, body, out any) error {
	var rd io.Reader
	hdr := map[string]string{}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
		hdr["Content-Type"] = "application/json"
	}
	resp, err := s.request(ctx, method, path, tok, hdr, rd)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// download reads a whole response body: a template, a component or a version, held in
// memory for one request only.
func (s *Server) download(ctx context.Context, path, tok string) ([]byte, error) {
	resp, err := s.request(ctx, http.MethodGet, path, tok, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

type page struct {
	NextCursor *string `json:"next_cursor"`
}

// listAll reads every page of a core list.
func listAll[T any](ctx context.Context, s *Server, path string, q url.Values, tok string) ([]T, error) {
	if q == nil {
		q = url.Values{}
	}
	var out []T
	for {
		var pg struct {
			Data []T  `json:"data"`
			Page page `json:"page"`
		}
		if err := s.call(ctx, http.MethodGet, path+"?"+q.Encode(), tok, nil, &pg); err != nil {
			return nil, err
		}
		out = append(out, pg.Data...)
		if pg.Page.NextCursor == nil || *pg.Page.NextCursor == "" {
			return out, nil
		}
		q.Set("cursor", *pg.Page.NextCursor)
	}
}

// Core records, with only the fields the module reads.

type coreFile struct {
	ID               string                      `json:"id"`
	Name             string                      `json:"name"`
	Extension        string                      `json:"extension"`
	MatterID         *string                     `json:"matter_id"`
	ContactID        *string                     `json:"contact_id"`
	FolderID         *string                     `json:"folder_id"`
	CategoryID       *string                     `json:"category_id"`
	Status           *string                     `json:"status"`
	Origin           string                      `json:"origin"`
	AttorneyOnly     bool                        `json:"attorney_only"`
	SharedWithClient bool                        `json:"shared_with_client"`
	Source           string                      `json:"source"`
	Library          bool                        `json:"library"`
	CurrentVersionID *string                     `json:"current_version_id"`
	TrashedAt        *string                     `json:"trashed_at"`
	CreatedAt        string                      `json:"created_at"`
	Included         map[string][]map[string]any `json:"included"`
}

func (f coreFile) status() string {
	if f.Status == nil {
		return ""
	}
	return *f.Status
}

func (f coreFile) matter() string {
	if f.MatterID == nil {
		return ""
	}
	return *f.MatterID
}

type coreVersion struct {
	ID          string `json:"id"`
	FileID      string `json:"file_id"`
	Number      int    `json:"number"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"media_type"`
	SavedBy     string `json:"saved_by"`
	How         string `json:"how"`
	Note        string `json:"note"`
	Quarantined bool   `json:"quarantined"`
	CreatedAt   string `json:"created_at"`
}

type coreRendition struct {
	ID            string `json:"id"`
	FileVersionID string `json:"file_version_id"`
	Kind          string `json:"kind"`
	Number        int    `json:"number"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
}

type coreFolder struct {
	ID       string `json:"id"`
	MatterID string `json:"matter_id"`
	Name     string `json:"name"`
}

type cfValue struct {
	DefinitionID string `json:"definition_id"`
	Key          string `json:"key"`
	Value        any    `json:"value"`
}

type coreMatter struct {
	ID                        string    `json:"id"`
	Number                    string    `json:"number"`
	Name                      string    `json:"name"`
	PracticeAreaID            string    `json:"practice_area_id"`
	Status                    string    `json:"status"`
	CaseNumber                string    `json:"case_number"`
	Court                     string    `json:"court"`
	CountyID                  *string   `json:"county_id"`
	ClientPosition            string    `json:"client_position"`
	ResponsibleAttorneyUserID *string   `json:"responsible_attorney_user_id"`
	CustomFields              []cfValue `json:"custom_fields"`
}

type coreParty struct {
	ID               string  `json:"id"`
	MatterID         string  `json:"matter_id"`
	ContactID        string  `json:"contact_id"`
	Role             string  `json:"role"`
	Primary          bool    `json:"primary"`
	ServiceAddressID *string `json:"service_address_id"`
}

type coreContact struct {
	ID           string    `json:"id"`
	FullName     string    `json:"full_name"`
	GivenName    string    `json:"given_name"`
	FamilyName   string    `json:"family_name"`
	AlsoKnownAs  []string  `json:"also_known_as"`
	CustomFields []cfValue `json:"custom_fields"`
}

type coreAddress struct {
	ID            string  `json:"id"`
	ContactID     string  `json:"contact_id"`
	Primary       bool    `json:"primary"`
	Line1         string  `json:"line1"`
	Line2         string  `json:"line2"`
	City          string  `json:"city"`
	Region        string  `json:"region"`
	PostalCode    string  `json:"postal_code"`
	EffectiveFrom *string `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
}

type coreCFDef struct {
	ID         string   `json:"id"`
	RecordType string   `json:"record_type"`
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	FieldType  string   `json:"field_type"`
	Options    []string `json:"options"`
	Retired    bool     `json:"retired"`
}

type firmProfile struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
	Website  string `json:"website"`
	TimeZone string `json:"time_zone"`
}

type dirUser struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Roles  []string `json:"roles"`
	Active bool     `json:"active"`
}

type docCategory struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Active bool   `json:"active"`
}

type dtStatus struct {
	Status string `json:"status"`
	Label  string `json:"label"`
}

// Typed reads.

func (s *Server) getFile(ctx context.Context, tok, id string) (coreFile, error) {
	var f coreFile
	err := s.call(ctx, http.MethodGet, "/api/v1/files/"+url.PathEscape(id), tok, nil, &f)
	return f, err
}

func (s *Server) versions(ctx context.Context, tok, fileID string) ([]coreVersion, error) {
	return listAll[coreVersion](ctx, s, "/api/v1/files/"+url.PathEscape(fileID)+"/versions", nil, tok)
}

// currentVersion returns the file's current version and its bytes.
func (s *Server) currentVersion(ctx context.Context, tok, fileID string) (coreVersion, []byte, error) {
	f, err := s.getFile(ctx, tok, fileID)
	if err != nil {
		return coreVersion{}, nil, err
	}
	vs, err := s.versions(ctx, tok, fileID)
	if err != nil {
		return coreVersion{}, nil, err
	}
	var cur coreVersion
	for _, v := range vs {
		if (f.CurrentVersionID != nil && v.ID == *f.CurrentVersionID) || (f.CurrentVersionID == nil && v.Number > cur.Number) {
			cur = v
		}
	}
	if cur.ID == "" {
		return coreVersion{}, nil, &coreError{Status: http.StatusNotFound, Code: "not_found", Path: "versions"}
	}
	b, err := s.download(ctx, versionPath(fileID, cur.ID), tok)
	return cur, b, err
}

func versionPath(fileID, versionID string) string {
	return "/api/v1/files/" + url.PathEscape(fileID) + "/versions/" + url.PathEscape(versionID) + "/download"
}

func (s *Server) getMatter(ctx context.Context, tok, id string) (coreMatter, error) {
	var m coreMatter
	err := s.call(ctx, http.MethodGet, "/api/v1/matters/"+url.PathEscape(id), tok, nil, &m)
	return m, err
}

func (s *Server) firm(ctx context.Context, tok string) (firmProfile, error) {
	var f firmProfile
	err := s.call(ctx, http.MethodGet, "/api/v1/firm-profile", tok, nil, &f)
	return f, err
}

// stageUpload stages bytes as a core upload in chunks of the size the core names. It
// publishes nothing: an upload left staged expires on the core's own schedule.
func (s *Server) stageUpload(ctx context.Context, tok, name, mediaType, matterID string, library bool, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	body := map[string]any{"file_name": name, "size_bytes": len(data), "sha256": hex.EncodeToString(sum[:]), "media_type": mediaType}
	if matterID != "" {
		body["matter_id"] = matterID
	}
	if library {
		body["library"] = true
	}
	var up struct {
		ID         string `json:"id"`
		ChunkBytes int    `json:"chunk_size_bytes"`
	}
	if err := s.call(ctx, http.MethodPost, "/api/v1/file-uploads", tok, body, &up); err != nil {
		return "", err
	}
	chunk := up.ChunkBytes
	if chunk <= 0 {
		chunk = len(data)
	}
	for off := 0; off < len(data) || (off == 0 && len(data) == 0); off += chunk {
		end := min(off+chunk, len(data))
		hdr := map[string]string{"Content-Type": "application/octet-stream",
			"Content-Range": "bytes " + strconv.Itoa(off) + "-" + strconv.Itoa(end-1) + "/" + strconv.Itoa(len(data))}
		resp, err := s.request(ctx, http.MethodPut, "/api/v1/file-uploads/"+url.PathEscape(up.ID), tok, hdr, bytes.NewReader(data[off:end]))
		if err != nil {
			return "", err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if len(data) == 0 {
			break
		}
	}
	if err := s.call(ctx, http.MethodPost, "/api/v1/file-uploads/"+url.PathEscape(up.ID)+"/complete", tok, nil, nil); err != nil {
		return "", err
	}
	return up.ID, nil
}

// publishLibrary stages and publishes one library file (CORE-17): no matter, no contact,
// kept out of Doc Tender.
func (s *Server) publishLibrary(ctx context.Context, tok, name string, data []byte) (coreFile, error) {
	up, err := s.stageUpload(ctx, tok, name, docxMedia, "", true, data)
	if err != nil {
		return coreFile{}, err
	}
	var f coreFile
	err = s.call(ctx, http.MethodPost, "/api/v1/files", tok, map[string]any{"upload_id": up, "name": name, "library": true}, &f)
	return f, err
}

func (s *Server) markStatus(ctx context.Context, tok, fileID, status string) (coreFile, error) {
	var f coreFile
	err := s.call(ctx, http.MethodPost, "/api/v1/files/"+url.PathEscape(fileID)+"/status", tok, map[string]string{"status": status}, &f)
	return f, err
}

func (s *Server) writeEvent(ctx context.Context, tok, action, matterID, summary string) error {
	return s.call(ctx, http.MethodPost, "/api/v1/events", tok, map[string]any{"action": action, "record_type": "matter",
		"record_id": matterID, "matter_id": matterID, "summary": summary, "client_visible": false}, nil)
}

func (s *Server) docTenderStatus(ctx context.Context, tok, fileID string) (dtStatus, error) {
	var st dtStatus
	err := s.call(ctx, http.MethodGet, "/api/v1/files/"+url.PathEscape(fileID)+"/doc-tender-status", tok, nil, &st)
	return st, err
}

// zeroID names no record; a call on it is refused for scope before it is looked up.
const zeroID = "00000000-0000-7000-8000-000000000000"

// grants says which optional scopes the admin approved. The core refuses an operation
// whose scope the module lacks with scope_denied before it looks anything up, so each is
// read with a call that changes nothing: listDocCategory, and convertFile on an id that
// names no file (scope_denied without editor:run, not_found with it).
type grants struct {
	Editor    bool
	DocTender bool
}

func (s *Server) grants(ctx context.Context, tok string) grants {
	var g grants
	err := s.call(ctx, http.MethodGet, "/api/v1/doc-tender/categories?limit=1", tok, nil, nil)
	g.DocTender = !isCode(err, "scope_denied") && !isStatus(err, http.StatusForbidden)
	err = s.call(ctx, http.MethodPost, "/api/v1/files/"+zeroID+"/conversions", tok, map[string]string{"format": "pdf"}, nil)
	g.Editor = !isCode(err, "scope_denied") && !isStatus(err, http.StatusForbidden)
	return g
}

// settings reads the module's settings from the core.
func (s *Server) settings(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := s.call(ctx, http.MethodGet, "/api/v1/modules/"+ID+"/settings", "", nil, &rows); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

func (s *Server) intSetting(ctx context.Context, key string) (int, error) {
	st, err := s.settings(ctx)
	if err != nil {
		return 0, err
	}
	v, ok := st[key]
	if !ok {
		return 0, fmt.Errorf("documents: setting %s is not set", key)
	}
	return strconv.Atoi(v)
}
