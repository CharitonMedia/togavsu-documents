// Package fakecore is an in-memory stand-in for the Togavsu core API, for the module's
// tests. It serves the core operations the Documents module calls, with the core's
// access rules that the module relies on (module token, context tokens, roles, matter
// walls, attorney-only files, optional scopes), and keeps skeleton core rows in
// Postgres so the module's foreign keys and the purge refusal behave as on a real core.
//
// It includes the core operations from docs/modules/CORE-CHANGES.md that core main
// doesn't have yet (library files CORE-17, firm profile CORE-18, user directory CORE-19,
// archive entries CORE-21). It is test scaffolding only; nothing here ships.
package fakecore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
	"github.com/jackc/pgx/v5/pgconn"
)

// Module is the module id the fake serves.
const Module = "documents"

// User is a core user.
type User struct {
	ID       string
	Name     string
	Roles    []string
	Active   bool
	ReadOnly bool // the role policy denies this user file writes
}

func (u *User) has(roles ...string) bool {
	for _, h := range u.Roles {
		for _, w := range roles {
			if h == w {
				return true
			}
		}
	}
	return false
}

func (u *User) firm() bool { return u.has("admin", "attorney", "staff") }

// Matter is a core matter.
type Matter struct {
	ID                        string         `json:"id"`
	Number                    string         `json:"number"`
	Name                      string         `json:"name"`
	PracticeAreaID            string         `json:"practice_area_id"`
	Status                    string         `json:"status"`
	CaseNumber                string         `json:"case_number"`
	Court                     string         `json:"court"`
	CountyID                  *string        `json:"county_id"`
	ClientPosition            string         `json:"client_position"`
	ResponsibleAttorneyUserID *string        `json:"responsible_attorney_user_id"`
	CustomFields              []CustomValue  `json:"custom_fields"`
	Wall                      []string       `json:"-"`
	Extra                     map[string]any `json:"-"`
}

// CustomValue is a custom field value on a record.
type CustomValue struct {
	DefinitionID string `json:"definition_id"`
	Key          string `json:"key"`
	Value        any    `json:"value"`
}

// Contact is a core contact.
type Contact struct {
	ID           string        `json:"id"`
	FullName     string        `json:"full_name"`
	GivenName    string        `json:"given_name"`
	FamilyName   string        `json:"family_name"`
	AlsoKnownAs  []string      `json:"also_known_as"`
	CustomFields []CustomValue `json:"custom_fields"`
}

// Address is a contact address.
type Address struct {
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

// Party is a matter party.
type Party struct {
	ID               string  `json:"id"`
	MatterID         string  `json:"matter_id"`
	ContactID        string  `json:"contact_id"`
	Role             string  `json:"role"`
	Primary          bool    `json:"primary"`
	ServiceAddressID *string `json:"service_address_id"`
}

// CFDef is a custom field definition.
type CFDef struct {
	ID         string   `json:"id"`
	RecordType string   `json:"record_type"`
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	FieldType  string   `json:"field_type"`
	Options    []string `json:"options"`
	Retired    bool     `json:"retired"`
}

// File is a core file.
type File struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Extension        string  `json:"extension"`
	MatterID         *string `json:"matter_id"`
	ContactID        *string `json:"contact_id"`
	FolderID         *string `json:"folder_id"`
	CategoryID       *string `json:"category_id"`
	Status           *string `json:"status"`
	Origin           string  `json:"origin"`
	AttorneyOnly     bool    `json:"attorney_only"`
	SharedWithClient bool    `json:"shared_with_client"`
	Source           string  `json:"source"`
	Library          bool    `json:"library"`
	CurrentVersionID *string `json:"current_version_id"`
	TrashedAt        *string `json:"trashed_at"`
	CreatedAt        string  `json:"created_at"`
	// CategoryByPerson is the Doc Tender person-set rule: a category set through the API
	// stays when Doc Tender processes the file.
	CategoryByPerson bool `json:"-"`
}

// Version is a file version.
type Version struct {
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
	bytes       []byte
}

// Rendition is a version's rendition.
type Rendition struct {
	ID            string `json:"id"`
	FileVersionID string `json:"file_version_id"`
	Kind          string `json:"kind"`
	Number        int    `json:"number"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	bytes         []byte
}

// Link is a typed link between files.
type Link struct {
	From, To, Kind string
}

// Event is a timeline event a module wrote.
type Event struct {
	Action, RecordType, RecordID, MatterID, Summary string
	ClientVisible                                   bool
}

type upload struct {
	ID, Name, MatterID, MediaType, SHA256 string
	Size                                  int
	Library, Done                         bool
	data                                  []byte
}

type folder struct {
	ID       string `json:"id"`
	MatterID string `json:"matter_id"`
	Name     string `json:"name"`
}

type ctxTok struct{ UserID, MatterID string }

// Core is the fake core.
type Core struct {
	mu sync.Mutex
	// DB holds the core skeleton schema and can read the module's schema, as the core's
	// own login does for include= and views.
	DB          *sql.DB
	ModuleToken string
	Secret      string
	Server      *httptest.Server
	Now         func() time.Time

	// Scopes holds the optional scopes the admin approved.
	Scopes   map[string]bool
	Settings map[string]string
	Firm     map[string]string

	Users         map[string]*User
	Matters       map[string]*Matter
	Contacts      map[string]*Contact
	Addresses     map[string]*Address
	Parties       map[string]*Party
	Counties      map[string]string
	PracticeAreas map[string]string
	PartyRoles    map[string]string
	CFDefs        []CFDef
	Categories    map[string]string
	Folders       map[string]*folder
	Files         map[string]*File
	Versions      map[string][]*Version
	Renditions    map[string][]*Rendition
	Links         []Link
	Events        []Event
	Downloads     []string
	BulkCalls     int
	ChunkSize     int
	// FailConvert makes convertFile fail; CutArchive cuts the zip stream halfway.
	FailConvert bool
	CutArchive  bool
	// Requests counts calls by "METHOD path".
	Requests map[string]int

	uploads map[string]*upload
	tokens  map[string]ctxTok
	seq     int
}

// New starts a fake core on db, which must hold the core skeleton (see Schema).
func New(db *sql.DB, now func() time.Time) *Core {
	c := &Core{DB: db, Now: now, ModuleToken: randHex(), Secret: randHex(),
		Scopes: map[string]bool{"editor:run": true, "doc_tender:read": true}, Settings: map[string]string{},
		Firm: map[string]string{}, Users: map[string]*User{}, Matters: map[string]*Matter{}, Contacts: map[string]*Contact{},
		Addresses: map[string]*Address{}, Parties: map[string]*Party{}, Counties: map[string]string{}, PracticeAreas: map[string]string{},
		PartyRoles: map[string]string{}, Categories: map[string]string{}, Folders: map[string]*folder{}, Files: map[string]*File{},
		Versions: map[string][]*Version{}, Renditions: map[string][]*Rendition{}, Requests: map[string]int{}, ChunkSize: 1 << 20,
		uploads: map[string]*upload{}, tokens: map[string]ctxTok{}}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	return c
}

// Close stops the server.
func (c *Core) Close() { c.Server.Close() }

// Schema is the core skeleton: the core tables the module's foreign keys name.
const Schema = `
CREATE SCHEMA core;
CREATE TABLE core.users (id TEXT PRIMARY KEY);
CREATE TABLE core.matters (id TEXT PRIMARY KEY);
CREATE TABLE core.contacts (id TEXT PRIMARY KEY);
CREATE TABLE core.practice_areas (id TEXT PRIMARY KEY);
CREATE TABLE core.files (id TEXT PRIMARY KEY);
CREATE TABLE core.events (id TEXT PRIMARY KEY, action TEXT NOT NULL, matter_id TEXT, summary TEXT NOT NULL);
`

func randHex() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ID returns a new UUIDv7-shaped id.
func (c *Core) ID() string {
	c.seq++
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	ms := uint64(c.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	b[6] = 0x70 | (b[6] & 0x0f)
	b[8] = 0x80 | (b[8] & 0x3f)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (c *Core) insert(table, id string) {
	if _, err := c.DB.Exec(`INSERT INTO core.`+table+` (id) VALUES ($1) ON CONFLICT DO NOTHING`, id); err != nil {
		panic(err)
	}
}

// Seeding.

// AddUser adds a user.
func (c *Core) AddUser(name string, roles ...string) *User {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := &User{ID: c.ID(), Name: name, Roles: roles, Active: true}
	c.Users[u.ID] = u
	c.insert("users", u.ID)
	return u
}

// AddPracticeArea adds a practice area.
func (c *Core) AddPracticeArea(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.ID()
	c.PracticeAreas[id] = name
	c.insert("practice_areas", id)
	return id
}

// AddMatter adds a matter.
func (c *Core) AddMatter(m Matter) *Matter {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m.ID == "" {
		m.ID = c.ID()
	}
	if m.Status == "" {
		m.Status = "open"
	}
	if m.CustomFields == nil {
		m.CustomFields = []CustomValue{}
	}
	c.Matters[m.ID] = &m
	c.insert("matters", m.ID)
	return &m
}

// AddContact adds a contact.
func (c *Core) AddContact(k Contact) *Contact {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k.ID == "" {
		k.ID = c.ID()
	}
	if k.AlsoKnownAs == nil {
		k.AlsoKnownAs = []string{}
	}
	if k.CustomFields == nil {
		k.CustomFields = []CustomValue{}
	}
	c.Contacts[k.ID] = &k
	c.insert("contacts", k.ID)
	return &k
}

// AddAddress adds an address.
func (c *Core) AddAddress(a Address) *Address {
	c.mu.Lock()
	defer c.mu.Unlock()
	a.ID = c.ID()
	c.Addresses[a.ID] = &a
	return &a
}

// AddParty adds a matter party.
func (c *Core) AddParty(p Party) *Party {
	c.mu.Lock()
	defer c.mu.Unlock()
	p.ID = c.ID()
	c.Parties[p.ID] = &p
	return &p
}

// AddFolder adds a folder on a matter.
func (c *Core) AddFolder(matterID, name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.ID()
	c.Folders[id] = &folder{ID: id, MatterID: matterID, Name: name}
	return id
}

// AddFile adds a file with one version, as a person's upload would.
func (c *Core) AddFile(f File, data []byte, by string) *File {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addFile(f, data, by, "upload")
}

func (c *Core) addFile(f File, data []byte, by, how string) *File {
	f.ID = c.ID()
	if f.Origin == "" {
		f.Origin = "firm"
	}
	if f.Source == "" {
		f.Source = "web"
	}
	if i := strings.LastIndex(f.Name, "."); i > 0 {
		f.Extension = f.Name[i+1:]
	}
	f.CreatedAt = c.Now().UTC().Format(time.RFC3339)
	c.Files[f.ID] = &f
	c.insert("files", f.ID)
	c.addVersion(f.ID, data, by, how)
	return &f
}

// AddVersion adds a version to a file (an editor save), and returns it.
func (c *Core) AddVersion(fileID string, data []byte, by, how string) *Version {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addVersion(fileID, data, by, how)
}

func (c *Core) addVersion(fileID string, data []byte, by, how string) *Version {
	sum := sha256.Sum256(data)
	v := &Version{ID: c.ID(), FileID: fileID, Number: len(c.Versions[fileID]) + 1, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
		MediaType: "application/octet-stream", SavedBy: by, How: how, CreatedAt: c.Now().UTC().Format(time.RFC3339), bytes: append([]byte(nil), data...)}
	c.Versions[fileID] = append(c.Versions[fileID], v)
	id := v.ID
	c.Files[fileID].CurrentVersionID = &id
	return v
}

// AddRendition adds a rendition of a version.
func (c *Core) AddRendition(fileID, versionID, kind string, data []byte) *Rendition {
	c.mu.Lock()
	defer c.mu.Unlock()
	sum := sha256.Sum256(data)
	n := 1
	for _, r := range c.Renditions[fileID] {
		if r.FileVersionID == versionID && r.Kind == kind {
			n++
		}
	}
	r := &Rendition{ID: c.ID(), FileVersionID: versionID, Kind: kind, Number: n, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), bytes: data}
	c.Renditions[fileID] = append(c.Renditions[fileID], r)
	return r
}

// Quarantine marks a version quarantined.
func (c *Core) Quarantine(fileID string, number int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.Versions[fileID] {
		if v.Number == number {
			v.Quarantined = true
		}
	}
}

// Trash trashes a file.
func (c *Core) Trash(fileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.Now().UTC().Format(time.RFC3339)
	c.Files[fileID].TrashedAt = &t
}

// DocTenderProcess runs Doc Tender's categorizing on a file: it sets category unless a
// person (or a module) set one, which Doc Tender keeps.
func (c *Core) DocTenderProcess(fileID, category string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.Files[fileID]
	if f.CategoryByPerson {
		return
	}
	f.CategoryID = &category
}

// Token mints a context token for a user, optionally on a matter.
func (c *Core) Token(userID, matterID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := randHex()
	c.tokens[t] = ctxTok{UserID: userID, MatterID: matterID}
	return t
}

// File returns a copy of a file record.
func (c *Core) File(id string) (File, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.Files[id]
	if !ok {
		return File{}, false
	}
	return *f, true
}

// FileVersions returns a copy of a file's versions.
func (c *Core) FileVersions(id string) []Version {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Version
	for _, v := range c.Versions[id] {
		out = append(out, *v)
	}
	return out
}

// VersionBytes returns a version's bytes.
func (c *Core) VersionBytes(fileID string, number int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.Versions[fileID] {
		if v.Number == number {
			return v.bytes
		}
	}
	return nil
}

// MatterFiles lists the files on a matter, not trashed.
func (c *Core) MatterFiles(matterID string) []File {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []File
	for _, f := range c.Files {
		if f.MatterID != nil && *f.MatterID == matterID && f.TrashedAt == nil {
			out = append(out, *f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EventsCopy returns the events written so far.
func (c *Core) EventsCopy() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.Events...)
}

// DownloadCount is how many file.downloaded the core logged.
func (c *Core) DownloadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.Downloads)
}

// Deliver sends a signed event to the module's /events URL and returns the status.
func (c *Core) Deliver(moduleURL, eventID, action, recordID, secret string) int {
	body, _ := json.Marshal(modkit.EventPayload{ID: eventID, Action: action, RecordType: "file", RecordID: recordID,
		OccurredAt: c.Now().UTC().Format(time.RFC3339), Module: Module})
	req, _ := http.NewRequest(http.MethodPost, moduleURL+"/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(modkit.SignatureHeader, modkit.SignWebhook(secret, c.Now(), body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// HTTP.

type apiErr struct {
	status int
	code   string
	detail string
}

func (e *apiErr) Error() string { return e.detail }

func errf(status int, code, detail string) error { return &apiErr{status, code, detail} }

var (
	errNotFound = errf(http.StatusNotFound, "not_found", "not found")
	errScope    = errf(http.StatusForbidden, "scope_denied", "the module lacks the scope for this operation")
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiErr
	if !errors.As(err, &ae) {
		ae = &apiErr{http.StatusInternalServerError, "internal", err.Error()}
	}
	writeJSON(w, ae.status, map[string]any{"type": "about:blank", "title": ae.code, "status": ae.status, "code": ae.code, "detail": ae.detail})
}

func list[T any](items []T) map[string]any {
	if items == nil {
		items = []T{}
	}
	return map[string]any{"data": items, "page": map[string]any{"limit": len(items), "next_cursor": nil}}
}

// req is one request with its caller.
type req struct {
	r      *http.Request
	user   *User
	matter string
	body   []byte
}

func (q *req) decode(v any) error {
	if len(q.body) == 0 {
		return nil
	}
	if err := json.Unmarshal(q.body, v); err != nil {
		return errf(http.StatusBadRequest, "invalid_request", "the body is not valid JSON")
	}
	return nil
}

func (c *Core) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+c.ModuleToken {
		writeErr(w, errf(http.StatusUnauthorized, "unauthenticated", "bad module token"))
		return
	}
	var body []byte
	if r.Method != http.MethodPut {
		body, _ = io.ReadAll(r.Body)
	}
	c.mu.Lock()
	c.Requests[r.Method+" "+r.URL.Path]++
	q := &req{r: r, body: body}
	if t := r.Header.Get(modkit.ContextHeader); t != "" {
		ct, ok := c.tokens[t]
		if !ok {
			c.mu.Unlock()
			writeErr(w, errf(http.StatusUnauthorized, "unauthenticated", "bad context token"))
			return
		}
		q.user, q.matter = c.Users[ct.UserID], ct.MatterID
	}
	path := r.URL.Path
	// Archive streams outside the lock.
	if r.Method == http.MethodPost && path == "/api/v1/file-archives" {
		out, err := c.archive(q)
		c.mu.Unlock()
		if err != nil {
			writeErr(w, err)
			return
		}
		c.stream(w, out)
		return
	}
	status, out, raw, err := c.route(q, r.Method, path)
	c.mu.Unlock()
	switch {
	case err != nil:
		writeErr(w, err)
	case raw != nil:
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(raw)
	default:
		writeJSON(w, status, out)
	}
}

func (c *Core) stream(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/zip")
	w.WriteHeader(http.StatusOK)
	if !c.CutArchive {
		_, _ = w.Write(b)
		return
	}
	_, _ = w.Write(b[:len(b)/2])
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
		}
	}
}

func seg(path string, i int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

func (c *Core) viewerOnly(q *req) error {
	if q.user == nil {
		return errf(http.StatusUnauthorized, "unauthenticated", "this operation is a viewer call")
	}
	return nil
}

func (c *Core) route(q *req, method, path string) (int, any, []byte, error) {
	r := q.r
	qs := r.URL.Query()
	switch {
	case method == "POST" && path == "/api/v1/modules/"+Module+"/context-tokens/verify":
		if q.user == nil {
			return 0, nil, nil, errf(http.StatusUnauthorized, "unauthenticated", "no context token")
		}
		var m any
		if q.matter != "" {
			m = q.matter
		}
		return 200, map[string]any{"viewer_user_id": q.user.ID, "roles": q.user.Roles, "module": Module, "matter_id": m}, nil, nil
	case method == "GET" && path == "/api/v1/modules/"+Module+"/settings":
		var rows []map[string]string
		for k, v := range c.Settings {
			rows = append(rows, map[string]string{"key": k, "value": v})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["key"] < rows[j]["key"] })
		if rows == nil {
			rows = []map[string]string{}
		}
		return 200, rows, nil, nil
	}
	if err := c.viewerOnly(q); err != nil {
		return 0, nil, nil, err
	}
	u := q.user
	switch {
	case method == "GET" && path == "/api/v1/firm-profile":
		return 200, c.Firm, nil, nil
	case method == "GET" && path == "/api/v1/user-directory":
		return c.directory(q)
	case method == "GET" && strings.HasPrefix(path, "/api/v1/users/"):
		return c.getUser(q, seg(path, 3))
	case method == "GET" && path == "/api/v1/matters":
		var out []*Matter
		for _, m := range c.Matters {
			if c.matterVisible(u, m) && (qs.Get("status") == "" || m.Status == qs.Get("status")) &&
				(qs.Get("q") == "" || strings.Contains(strings.ToLower(m.Name+" "+m.Number), strings.ToLower(qs.Get("q")))) {
				out = append(out, m)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
		return 200, list(out), nil, nil
	case method == "GET" && strings.HasPrefix(path, "/api/v1/matters/"):
		m, err := c.matter(u, seg(path, 3))
		return 200, m, nil, err
	case method == "GET" && strings.HasPrefix(path, "/api/v1/counties/"):
		n, ok := c.Counties[seg(path, 3)]
		if !ok {
			return 0, nil, nil, errNotFound
		}
		return 200, map[string]string{"id": seg(path, 3), "name": n}, nil, nil
	case method == "GET" && path == "/api/v1/matter-parties":
		m, err := c.matter(u, qs.Get("matter_id"))
		if err != nil {
			return 0, nil, nil, err
		}
		var out []*Party
		for _, p := range c.Parties {
			if p.MatterID == m.ID {
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return 200, list(out), nil, nil
	case method == "GET" && strings.HasPrefix(path, "/api/v1/contacts/"):
		k, ok := c.Contacts[seg(path, 3)]
		if !ok || !u.firm() {
			return 0, nil, nil, errNotFound
		}
		return 200, k, nil, nil
	case method == "GET" && path == "/api/v1/contact-addresses":
		var out []*Address
		for _, a := range c.Addresses {
			if a.ContactID == qs.Get("contact_id") {
				out = append(out, a)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return 200, list(out), nil, nil
	case method == "GET" && strings.HasPrefix(path, "/api/v1/contact-addresses/"):
		a, ok := c.Addresses[seg(path, 3)]
		if !ok {
			return 0, nil, nil, errNotFound
		}
		return 200, a, nil, nil
	case method == "GET" && path == "/api/v1/party-roles":
		var out []map[string]string
		for k, l := range c.PartyRoles {
			out = append(out, map[string]string{"key": k, "label": l})
		}
		return 200, list(out), nil, nil
	case method == "GET" && path == "/api/v1/practice-areas":
		var out []map[string]string
		for id, n := range c.PracticeAreas {
			out = append(out, map[string]string{"id": id, "name": n})
		}
		return 200, list(out), nil, nil
	case method == "GET" && path == "/api/v1/custom-field-definitions":
		return 200, list(c.CFDefs), nil, nil
	case method == "GET" && path == "/api/v1/doc-tender/categories":
		if !c.Scopes["doc_tender:read"] {
			return 0, nil, nil, errScope
		}
		var out []map[string]any
		for id, k := range c.Categories {
			out = append(out, map[string]any{"id": id, "key": k, "active": true})
		}
		return 200, list(out), nil, nil
	case method == "GET" && path == "/api/v1/folders":
		if _, err := c.matter(u, qs.Get("matter_id")); err != nil {
			return 0, nil, nil, err
		}
		var out []*folder
		for _, f := range c.Folders {
			if f.MatterID == qs.Get("matter_id") {
				out = append(out, f)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return 200, list(out), nil, nil
	case method == "POST" && path == "/api/v1/events":
		var in struct {
			Action        string `json:"action"`
			RecordType    string `json:"record_type"`
			RecordID      string `json:"record_id"`
			MatterID      string `json:"matter_id"`
			Summary       string `json:"summary"`
			ClientVisible bool   `json:"client_visible"`
		}
		if err := q.decode(&in); err != nil {
			return 0, nil, nil, err
		}
		if _, err := c.matter(u, in.MatterID); err != nil {
			return 0, nil, nil, err
		}
		c.Events = append(c.Events, Event{in.Action, in.RecordType, in.RecordID, in.MatterID, in.Summary, in.ClientVisible})
		id := c.ID()
		if _, err := c.DB.Exec(`INSERT INTO core.events (id, action, matter_id, summary) VALUES ($1, $2, $3, $4)`, id, in.Action, in.MatterID, in.Summary); err != nil {
			return 0, nil, nil, err
		}
		return 201, map[string]string{"id": id}, nil, nil
	case strings.HasPrefix(path, "/api/v1/file-uploads"):
		return c.uploadRoute(q, method, path)
	case method == "POST" && path == "/api/v1/files:bulk":
		return c.bulk(q)
	case method == "POST" && path == "/api/v1/files":
		var in map[string]any
		if err := q.decode(&in); err != nil {
			return 0, nil, nil, err
		}
		f, err := c.publish(u, in)
		return 201, f, nil, err
	case method == "GET" && path == "/api/v1/files":
		return c.listFiles(q)
	case strings.HasPrefix(path, "/api/v1/files/"):
		return c.fileRoute(q, method, path)
	}
	return 0, nil, nil, errf(http.StatusNotFound, "not_found", "the fake core doesn't serve "+method+" "+path)
}

func (c *Core) directory(q *req) (int, any, []byte, error) {
	qs := q.r.URL.Query()
	ids := map[string]bool{}
	for _, v := range qs["ids"] {
		for _, id := range strings.Split(v, ",") {
			ids[id] = true
		}
	}
	var out []map[string]any
	for _, u := range c.Users {
		if len(ids) > 0 && !ids[u.ID] {
			continue
		}
		if r := qs.Get("role"); r != "" && !u.has(r) {
			continue
		}
		if qs.Get("active") == "true" && !u.Active {
			continue
		}
		out = append(out, map[string]any{"id": u.ID, "name": u.Name, "roles": u.Roles, "active": u.Active})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	return 200, list(out), nil, nil
}

// getUser serves GET /api/v1/users/{id}; include=documents.attorney reads the module's
// extension table, as the core does.
func (c *Core) getUser(q *req, id string) (int, any, []byte, error) {
	if !q.user.has("admin") {
		return 0, nil, nil, errNotFound
	}
	u, ok := c.Users[id]
	if !ok {
		return 0, nil, nil, errNotFound
	}
	out := map[string]any{"id": u.ID, "name": u.Name, "roles": u.Roles}
	if strings.Contains(q.r.URL.Query().Get("include"), "documents.attorney") {
		rows, err := c.rows(`SELECT user_id, bar_number, bar_state, courtesy_title, signature_line FROM mod_documents.attorney_profiles WHERE user_id = $1`, id)
		if err != nil {
			return 0, nil, nil, err
		}
		out["included"] = map[string]any{"documents.attorney": rows}
	}
	return 200, out, nil, nil
}

func (c *Core) rows(query string, args ...any) ([]map[string]any, error) {
	rs, err := c.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	out := []map[string]any{}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, col := range cols {
			m[col] = vals[i]
		}
		out = append(out, m)
	}
	return out, rs.Err()
}

func (c *Core) matterVisible(u *User, m *Matter) bool {
	if !u.firm() {
		return false
	}
	if len(m.Wall) == 0 {
		return true
	}
	for _, id := range m.Wall {
		if id == u.ID {
			return true
		}
	}
	return false
}

func (c *Core) matter(u *User, id string) (*Matter, error) {
	m, ok := c.Matters[id]
	if !ok || !c.matterVisible(u, m) {
		return nil, errNotFound
	}
	return m, nil
}

func (c *Core) fileVisible(u *User, f *File) bool {
	if !u.firm() {
		return false
	}
	if f.AttorneyOnly && !u.has("admin", "attorney") {
		return false
	}
	if f.MatterID != nil {
		m, ok := c.Matters[*f.MatterID]
		if !ok || !c.matterVisible(u, m) {
			return false
		}
	}
	return true
}

func (c *Core) file(u *User, id string) (*File, error) {
	f, ok := c.Files[id]
	if !ok || !c.fileVisible(u, f) {
		return nil, errNotFound
	}
	return f, nil
}

func (c *Core) writable(u *User, f *File) error {
	if u.ReadOnly {
		return errf(http.StatusForbidden, "forbidden", "this role cannot change files")
	}
	return nil
}

// fileJSON writes a file with any include= the core would add.
func (c *Core) fileJSON(f *File, include string) (map[string]any, error) {
	b, _ := json.Marshal(f)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if strings.Contains(include, "documents.generated") {
		rows, err := c.rows(`SELECT file_id, template_id, template_name, run_id, template_version_number FROM mod_documents.generated_documents WHERE file_id = $1`, f.ID)
		if err != nil {
			return nil, err
		}
		out["included"] = map[string]any{"documents.generated": rows}
	}
	return out, nil
}

func (c *Core) listFiles(q *req) (int, any, []byte, error) {
	qs := q.r.URL.Query()
	var picked []*File
	for _, f := range c.Files {
		if !c.fileVisible(q.user, f) {
			continue
		}
		if (f.TrashedAt != nil) != (qs.Get("trashed") == "true") {
			continue
		}
		if m := qs.Get("matter_id"); m != "" && (f.MatterID == nil || *f.MatterID != m) {
			continue
		}
		if fo := qs.Get("folder_id"); fo != "" && (f.FolderID == nil || *f.FolderID != fo) {
			continue
		}
		if ca := qs.Get("category_id"); ca != "" && (f.CategoryID == nil || *f.CategoryID != ca) {
			continue
		}
		if l := qs.Get("library"); l != "" && f.Library != (l == "true") {
			continue
		}
		picked = append(picked, f)
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].ID < picked[j].ID })
	var out []map[string]any
	for _, f := range picked {
		j, err := c.fileJSON(f, qs.Get("include"))
		if err != nil {
			return 0, nil, nil, err
		}
		out = append(out, j)
	}
	return 200, list(out), nil, nil
}

func (c *Core) fileRoute(q *req, method, path string) (int, any, []byte, error) {
	u := q.user
	id := seg(path, 3)
	sub := seg(path, 4)
	// convertFile on an id that names no file answers for scope before lookup.
	if method == "POST" && sub == "conversions" && !c.Scopes["editor:run"] {
		return 0, nil, nil, errScope
	}
	if method == "GET" && sub == "doc-tender-status" && !c.Scopes["doc_tender:read"] {
		return 0, nil, nil, errScope
	}
	f, err := c.file(u, id)
	if err != nil {
		return 0, nil, nil, err
	}
	switch {
	case method == "GET" && sub == "":
		j, err := c.fileJSON(f, q.r.URL.Query().Get("include"))
		return 200, j, nil, err
	case method == "PATCH" && sub == "":
		if err := c.writable(u, f); err != nil {
			return 0, nil, nil, err
		}
		var in map[string]json.RawMessage
		if err := q.decode(&in); err != nil {
			return 0, nil, nil, err
		}
		if v, ok := in["attorney_only"]; ok {
			_ = json.Unmarshal(v, &f.AttorneyOnly)
		}
		if v, ok := in["shared_with_client"]; ok {
			_ = json.Unmarshal(v, &f.SharedWithClient)
		}
		if v, ok := in["category_id"]; ok {
			var cat *string
			_ = json.Unmarshal(v, &cat)
			if cat != nil {
				if _, ok := c.Categories[*cat]; !ok {
					return 0, nil, nil, errf(422, "validation_failed", "unknown category")
				}
			}
			f.CategoryID = cat
			f.CategoryByPerson = cat != nil
		}
		if _, ok := in["matter_id"]; ok && f.Library {
			return 0, nil, nil, errf(422, "validation_failed", "a library file takes no matter")
		}
		return 200, f, nil, nil
	case method == "POST" && sub == "status":
		if err := c.writable(u, f); err != nil {
			return 0, nil, nil, err
		}
		var in struct {
			Status string `json:"status"`
		}
		if err := q.decode(&in); err != nil {
			return 0, nil, nil, err
		}
		switch in.Status {
		case "draft", "final", "executed", "filed", "received":
		default:
			return 0, nil, nil, errf(422, "validation_failed", "unknown status")
		}
		st := in.Status
		f.Status = &st
		return 200, f, nil, nil
	case method == "POST" && sub == "purge":
		if !u.has("admin") {
			return 0, nil, nil, errNotFound
		}
		if f.TrashedAt == nil {
			return 0, nil, nil, errf(409, "conflict", "only a trashed file can be purged")
		}
		if _, err := c.DB.Exec(`DELETE FROM core.files WHERE id = $1`, f.ID); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23503" {
				mod := strings.TrimPrefix(pe.SchemaName, "mod_")
				if mod == "" {
					mod = "a module"
				}
				return 0, nil, nil, errf(409, "held_by_module", "the module "+mod+" still holds this file; release it there first")
			}
			return 0, nil, nil, err
		}
		delete(c.Files, f.ID)
		return 200, map[string]string{"id": f.ID}, nil, nil
	case method == "GET" && sub == "versions" && seg(path, 5) == "":
		var out []*Version
		out = append(out, c.Versions[f.ID]...)
		return 200, list(out), nil, nil
	case method == "GET" && sub == "versions" && seg(path, 6) == "download":
		for _, v := range c.Versions[f.ID] {
			if v.ID == seg(path, 5) {
				c.Downloads = append(c.Downloads, f.ID)
				return 200, nil, v.bytes, nil
			}
		}
		return 0, nil, nil, errNotFound
	case method == "GET" && sub == "renditions" && seg(path, 5) == "":
		var out []*Rendition
		out = append(out, c.Renditions[f.ID]...)
		return 200, list(out), nil, nil
	case method == "GET" && sub == "renditions":
		for _, r := range c.Renditions[f.ID] {
			if r.ID == seg(path, 5) {
				return 200, nil, r.bytes, nil
			}
		}
		return 0, nil, nil, errNotFound
	case method == "POST" && sub == "conversions":
		if err := c.writable(u, f); err != nil {
			return 0, nil, nil, err
		}
		if c.FailConvert {
			return 0, nil, nil, errf(http.StatusBadGateway, "conversion_failed", "the converter failed")
		}
		base := strings.TrimSuffix(f.Name, "."+f.Extension)
		pdf := c.addFile(File{Name: base + ".pdf", MatterID: f.MatterID, AttorneyOnly: f.AttorneyOnly, Source: Module}, []byte("%PDF-1.7 fake "+f.ID), u.ID, "conversion")
		c.Links = append(c.Links, Link{From: pdf.ID, To: f.ID, Kind: "conversion_of"})
		return 201, map[string]any{"file": pdf, "link": map[string]string{"from_file_id": pdf.ID, "to_file_id": f.ID, "kind": "conversion_of"}}, nil, nil
	case method == "GET" && sub == "doc-tender-status":
		switch {
		case f.Library:
			return 200, map[string]any{"status": "not_processed_library", "label": "Library file, not processed"}, nil, nil
		case f.MatterID != nil && c.Matters[*f.MatterID].Status == "closed":
			return 200, map[string]any{"status": "not_processed_closed", "label": "Not processed: the matter is closed"}, nil, nil
		}
		return 200, map[string]any{"status": "queued", "label": "Waiting for Doc Tender"}, nil, nil
	}
	return 0, nil, nil, errf(http.StatusNotFound, "not_found", "the fake core doesn't serve "+method+" "+path)
}

func (c *Core) uploadRoute(q *req, method, path string) (int, any, []byte, error) {
	r := q.r
	id := seg(path, 3)
	switch {
	case method == "POST" && id == "":
		var in struct {
			FileName  string `json:"file_name"`
			MatterID  string `json:"matter_id"`
			ContactID string `json:"contact_id"`
			MediaType string `json:"media_type"`
			Size      int    `json:"size_bytes"`
			SHA256    string `json:"sha256"`
			Library   bool   `json:"library"`
		}
		if err := q.decode(&in); err != nil {
			return 0, nil, nil, err
		}
		if in.Library && (in.MatterID != "" || in.ContactID != "") {
			return 0, nil, nil, errf(422, "validation_failed", "a library file takes no matter or contact")
		}
		if in.MatterID != "" {
			if _, err := c.matter(q.user, in.MatterID); err != nil {
				return 0, nil, nil, err
			}
		}
		up := &upload{ID: c.ID(), Name: in.FileName, MatterID: in.MatterID, MediaType: in.MediaType, SHA256: in.SHA256, Size: in.Size, Library: in.Library}
		c.uploads[up.ID] = up
		return 201, map[string]any{"id": up.ID, "chunk_size_bytes": c.ChunkSize}, nil, nil
	case method == "PUT" && seg(path, 4) == "":
		up, ok := c.uploads[id]
		if !ok {
			return 0, nil, nil, errNotFound
		}
		b, _ := io.ReadAll(r.Body)
		var start int
		if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-", &start); err != nil || start != len(up.data) {
			return 0, nil, nil, errf(422, "validation_failed", "chunks arrive in order")
		}
		up.data = append(up.data, b...)
		return 200, map[string]any{"id": up.ID, "received_bytes": len(up.data)}, nil, nil
	case method == "POST" && seg(path, 4) == "complete":
		up, ok := c.uploads[id]
		if !ok {
			return 0, nil, nil, errNotFound
		}
		sum := sha256.Sum256(up.data)
		if len(up.data) != up.Size || hex.EncodeToString(sum[:]) != up.SHA256 {
			return 0, nil, nil, errf(422, "validation_failed", "size or SHA-256 doesn't match")
		}
		up.Done = true
		return 200, map[string]any{"id": up.ID, "status": "complete"}, nil, nil
	}
	return 0, nil, nil, errNotFound
}

// publish turns a completed upload into a file, as publishFile does.
func (c *Core) publish(u *User, in map[string]any) (*File, error) {
	upID, _ := in["upload_id"].(string)
	up, ok := c.uploads[upID]
	if !ok || !up.Done {
		return nil, errf(422, "validation_failed", "the upload is not complete")
	}
	name, _ := in["name"].(string)
	if name == "" {
		name = up.Name
	}
	lib, _ := in["library"].(bool)
	if lib != up.Library {
		return nil, errf(422, "validation_failed", "library must match the upload")
	}
	f := File{Name: name, Library: lib, Source: Module}
	if m, _ := in["matter_id"].(string); m != "" || up.MatterID != "" {
		if m == "" {
			m = up.MatterID
		}
		if lib {
			return nil, errf(422, "validation_failed", "a library file takes no matter")
		}
		if _, err := c.matter(u, m); err != nil {
			return nil, err
		}
		f.MatterID = &m
		f.Name = c.freeName(m, name)
	}
	if fo, _ := in["folder_id"].(string); fo != "" {
		f.FolderID = &fo
	}
	f.AttorneyOnly, _ = in["attorney_only"].(bool)
	f.SharedWithClient, _ = in["shared_with_client"].(bool)
	if u.ReadOnly {
		return nil, errf(http.StatusForbidden, "forbidden", "this role cannot add files here")
	}
	delete(c.uploads, upID)
	return c.addFile(f, up.data, u.ID, "upload"), nil
}

// freeName adds -2, -3 before the extension when the matter already has the name.
func (c *Core) freeName(matterID, name string) string {
	taken := map[string]bool{}
	for _, f := range c.Files {
		if f.MatterID != nil && *f.MatterID == matterID && f.TrashedAt == nil {
			taken[strings.ToLower(f.Name)] = true
		}
	}
	base, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 {
		base, ext = name[:i], name[i:]
	}
	out := name
	for n := 2; taken[strings.ToLower(out)]; n++ {
		out = base + "-" + strconv.Itoa(n) + ext
	}
	return out
}

// bulk publishes every item or none.
func (c *Core) bulk(q *req) (int, any, []byte, error) {
	c.BulkCalls++
	var in struct {
		Items []map[string]any `json:"items"`
	}
	if err := q.decode(&in); err != nil {
		return 0, nil, nil, err
	}
	for _, it := range in.Items {
		upID, _ := it["upload_id"].(string)
		if up, ok := c.uploads[upID]; !ok || !up.Done {
			return 0, nil, nil, errf(422, "validation_failed", "an item's upload is not complete")
		}
		if m, _ := it["matter_id"].(string); m != "" {
			if _, err := c.matter(q.user, m); err != nil {
				return 0, nil, nil, err
			}
		}
	}
	var out []*File
	for _, it := range in.Items {
		f, err := c.publish(q.user, it)
		if err != nil {
			return 0, nil, nil, err
		}
		out = append(out, f)
	}
	return 201, map[string]any{"data": out}, nil, nil
}

// archive checks every entry as the viewer and builds the zip, as createFileArchive
// with entries (CORE-21) does. Each entry logs one file.downloaded.
func (c *Core) archive(q *req) ([]byte, error) {
	if err := c.viewerOnly(q); err != nil {
		return nil, err
	}
	var in struct {
		Entries []struct {
			FileID      string `json:"file_id"`
			VersionID   string `json:"version_id"`
			RenditionID string `json:"rendition_id"`
			Path        string `json:"path"`
		} `json:"entries"`
		FileIDs           []string `json:"file_ids"`
		FolderID          *string  `json:"folder_id"`
		ConfirmQuarantine bool     `json:"confirm_quarantine"`
	}
	if err := q.decode(&in); err != nil {
		return nil, err
	}
	if len(in.Entries) == 0 || len(in.FileIDs) > 0 || in.FolderID != nil {
		return nil, errf(422, "validation_failed", "entries is exclusive with file_ids and folder_id")
	}
	seen := map[string]bool{}
	type pick struct {
		path string
		data []byte
		file string
	}
	var picks []pick
	for _, e := range in.Entries {
		if !validPath(e.Path) || seen[strings.ToLower(e.Path)] {
			return nil, errf(422, "validation_failed", "bad or repeated entry path")
		}
		seen[strings.ToLower(e.Path)] = true
		f, err := c.file(q.user, e.FileID)
		if err != nil {
			return nil, err
		}
		var data []byte
		found := false
		for _, v := range c.Versions[f.ID] {
			if e.VersionID != "" && v.ID == e.VersionID {
				if v.Quarantined && !in.ConfirmQuarantine {
					return nil, errf(409, "quarantine_confirmation_required", "a selected version is quarantined")
				}
				data, found = v.bytes, true
			}
			if e.RenditionID != "" {
				for _, r := range c.Renditions[f.ID] {
					if r.ID == e.RenditionID && r.FileVersionID == v.ID {
						if v.Quarantined && !in.ConfirmQuarantine {
							return nil, errf(409, "quarantine_confirmation_required", "a selected version is quarantined")
						}
						data, found = r.bytes, true
					}
				}
			}
		}
		if !found {
			return nil, errNotFound
		}
		picks = append(picks, pick{e.Path, data, f.ID})
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range picks {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.path, Method: zip.Store, Modified: c.Now()})
		if err != nil {
			return nil, err
		}
		_, _ = w.Write(p.data)
		c.Downloads = append(c.Downloads, p.file)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func validPath(p string) bool {
	if p == "" || len(p) > 255 || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Call makes one API call as the module, with a viewer's context token when tok is
// set, and returns the status and body.
func (c *Core) Call(method, path, tok string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, c.Server.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.ModuleToken)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set(modkit.ContextHeader, tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
