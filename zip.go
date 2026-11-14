package documents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bulk download: the module picks the entries and their paths, writes the frozen
// manifest, then has the core stream the zip with createFileArchive entries (CC-5). The
// core checks each entry as the viewer, logs each as a download and streams the bytes;
// the module never holds a file.

type zipInput struct {
	Scope      string   `json:"scope"` // matter, folder or selection
	FolderID   string   `json:"folder_id"`
	FileIDs    []string `json:"file_ids"`
	Status     string   `json:"status"`
	CategoryID string   `json:"category_id"`
	Versions   string   `json:"versions"` // current or all
	Markdown   bool     `json:"with_markdown"`
	Layout     string   `json:"layout"`     // folders or flat; empty is the setting
	Quarantine string   `json:"quarantine"` // empty, include or leave_out
}

// Entry is one zip entry: a version, or a version's Markdown copy.
type Entry struct {
	FileID        string `json:"file_id"`
	VersionID     string `json:"version_id,omitempty"`
	RenditionID   string `json:"rendition_id,omitempty"`
	VersionNumber int    `json:"version_number"`
	Kind          string `json:"kind"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	Quarantined   bool   `json:"-"`
	FileName      string `json:"-"`
}

var fileStatuses = map[string]bool{"": true, "draft": true, "final": true, "executed": true, "filed": true, "received": true}

// errNothing is the empty scope: no zip, no manifest, no event.
var errNothing = &inputError{Status: http.StatusUnprocessableEntity, Code: "nothing_to_download", Msg: "Nothing to download"}

func (in *zipInput) check(defaultLayout string) error {
	if in.Scope == "" {
		in.Scope = "matter"
	}
	if in.Versions == "" {
		in.Versions = "current"
	}
	if in.Layout == "" {
		in.Layout = defaultLayout
	}
	if in.Layout == "" {
		in.Layout = "folders"
	}
	switch {
	case in.Scope != "matter" && in.Scope != "folder" && in.Scope != "selection":
		return unprocessable("scope is matter, folder or selection", nil)
	case in.Scope == "folder" && in.FolderID == "":
		return unprocessable("pick a folder", nil)
	case in.Scope == "selection" && len(in.FileIDs) == 0:
		return errNothing
	case !fileStatuses[in.Status]:
		return unprocessable("status is draft, final, executed, filed or received", nil)
	case in.Versions != "current" && in.Versions != "all":
		return unprocessable("versions is current or all", nil)
	case in.Layout != "folders" && in.Layout != "flat":
		return unprocessable("layout is folders or flat", nil)
	case in.Quarantine != "" && in.Quarantine != "include" && in.Quarantine != "leave_out":
		return unprocessable("quarantine is include or leave_out", nil)
	}
	return nil
}

// cleanSegment makes one path segment that the CC-5 path rules accept: no slash,
// backslash or control character, and never "." or "..".
func cleanSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\':
			b.WriteRune('-')
		case unicode.IsControl(r) || r == utf8.RuneError:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "_" + out
	}
	return out
}

// splitName splits "Motion.docx" into "Motion" and ".docx".
func splitName(name string) (string, string) {
	i := strings.LastIndex(name, ".")
	if i < one {
		return name, ""
	}
	return name[:i], name[i:]
}

// truncate keeps a path within 255 bytes, cutting the base and keeping the extension.
func truncate(dir, base, ext string) string {
	limit := zipPathMax - len(ext)
	if dir != "" {
		limit -= len(dir) + 1
	}
	for len(base) > limit && len(base) > 0 {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	if dir == "" {
		return base + ext
	}
	return dir + "/" + base + ext
}

// validPath reports whether a path meets the CC-5 rules.
func validPath(p string) bool {
	if p == "" || len(p) > zipPathMax || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// pathSet hands out entry paths unique without regard to case, adding -2, -3 before
// the extension on a clash, as the core does.
type pathSet map[string]bool

func (ps pathSet) add(dir, base, ext string) string {
	p := truncate(dir, base, ext)
	for n := two; ps[strings.ToLower(p)]; n++ {
		p = truncate(dir, base+"-"+strconv.Itoa(n), ext)
	}
	ps[strings.ToLower(p)] = true
	return p
}

// zipFile is one file of the scope with what its entries need.
type zipFile struct {
	File       coreFile
	Folder     string
	Versions   []coreVersion
	Renditions []coreRendition
}

// layoutEntries turns the chosen files into entries with their paths. It holds the
// naming rules and touches nothing remote, so it is tested alone.
func layoutEntries(files []zipFile, versions, layout string, markdown bool) []Entry {
	ps := pathSet{}
	var out []Entry
	for _, zf := range files {
		dir := ""
		if layout == "folders" {
			dir = "Unfiled"
			if zf.Folder != "" {
				dir = cleanSegment(zf.Folder)
			}
		}
		base, ext := splitName(cleanSegment(zf.File.Name))
		vs := append([]coreVersion(nil), zf.Versions...)
		sort.Slice(vs, func(i, j int) bool { return vs[i].Number < vs[j].Number })
		if versions == "current" {
			var cur []coreVersion
			for _, v := range vs {
				if zf.File.CurrentVersionID != nil && v.ID == *zf.File.CurrentVersionID {
					cur = []coreVersion{v}
				}
			}
			if cur == nil && len(vs) > 0 {
				cur = vs[len(vs)-1:]
			}
			vs = cur
		}
		for _, v := range vs {
			name := base
			if versions == "all" {
				name = base + "-v" + strconv.Itoa(v.Number)
			}
			out = append(out, Entry{FileID: zf.File.ID, VersionID: v.ID, VersionNumber: v.Number, Kind: "version",
				Path: ps.add(dir, name, ext), SHA256: v.SHA256, SizeBytes: v.SizeBytes, Quarantined: v.Quarantined, FileName: zf.File.Name})
			if !markdown {
				continue
			}
			var md *coreRendition
			for i := range zf.Renditions {
				r := &zf.Renditions[i]
				if r.Kind == "markdown" && r.FileVersionID == v.ID && (md == nil || r.Number > md.Number) {
					md = r
				}
			}
			if md != nil {
				out = append(out, Entry{FileID: zf.File.ID, RenditionID: md.ID, VersionNumber: v.Number, Kind: "markdown",
					Path: ps.add(dir, name, ".md"), SHA256: md.SHA256, SizeBytes: md.SizeBytes, Quarantined: v.Quarantined, FileName: zf.File.Name})
			}
		}
	}
	return out
}

// zipPlan reads the scope through the API as the viewer and lays out the entries.
func (s *Server) zipPlan(ctx context.Context, v viewer, m coreMatter, in zipInput) ([]Entry, string, error) {
	q := url.Values{"matter_id": {m.ID}}
	if in.Scope == "folder" {
		q.Set("folder_id", in.FolderID)
	}
	if in.CategoryID != "" {
		q.Set("category_id", in.CategoryID)
	}
	files, err := listAll[coreFile](ctx, s, "/api/v1/files", q, v.Token)
	if err != nil {
		return nil, "", err
	}
	folders, err := listAll[coreFolder](ctx, s, "/api/v1/folders", url.Values{"matter_id": {m.ID}}, v.Token)
	if err != nil {
		return nil, "", err
	}
	names := map[string]string{}
	for _, f := range folders {
		names[f.ID] = f.Name
	}
	label := ""
	if in.Scope == "folder" {
		var ok bool
		if label, ok = names[in.FolderID]; !ok {
			return nil, "", notFound()
		}
	}
	pick := map[string]bool{}
	for _, id := range in.FileIDs {
		pick[id] = true
	}
	var chosen []zipFile
	for _, f := range files {
		if f.TrashedAt != nil || f.matter() != m.ID {
			continue
		}
		if in.Scope == "selection" && !pick[f.ID] {
			continue
		}
		if in.Status != "" && f.status() != in.Status {
			continue
		}
		if in.CategoryID != "" && (f.CategoryID == nil || *f.CategoryID != in.CategoryID) {
			continue
		}
		zf := zipFile{File: f}
		if f.FolderID != nil {
			zf.Folder = names[*f.FolderID]
		}
		if zf.Versions, err = s.versions(ctx, v.Token, f.ID); err != nil {
			return nil, "", err
		}
		if in.Markdown {
			if zf.Renditions, err = listAll[coreRendition](ctx, s, "/api/v1/files/"+url.PathEscape(f.ID)+"/renditions", nil, v.Token); err != nil {
				return nil, "", err
			}
		}
		chosen = append(chosen, zf)
	}
	sort.SliceStable(chosen, func(i, j int) bool {
		a, b := chosen[i], chosen[j]
		if (a.Folder == "") != (b.Folder == "") {
			return a.Folder != ""
		}
		if !strings.EqualFold(a.Folder, b.Folder) {
			return strings.ToLower(a.Folder) < strings.ToLower(b.Folder)
		}
		if !strings.EqualFold(a.File.Name, b.File.Name) {
			return strings.ToLower(a.File.Name) < strings.ToLower(b.File.Name)
		}
		return a.File.ID < b.File.ID
	})
	return layoutEntries(chosen, in.Versions, in.Layout, in.Markdown), label, nil
}

// Quarantined names one file with a quarantined version in the scope.
type Quarantined struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
}

func (s *Server) zipDownload(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	var in zipInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := s.getMatter(ctx, v.Token, r.PathValue("matter_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.settings(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := in.check(st["zip_layout"]); err != nil {
		s.fail(w, r, err)
		return
	}
	entries, label, err := s.zipPlan(ctx, v, m, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Quarantined versions: ask, then include with confirm_quarantine or leave them out.
	var held []Quarantined
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Quarantined && !seen[e.FileID] {
			seen[e.FileID] = true
			held = append(held, Quarantined{FileID: e.FileID, Name: e.FileName})
		}
	}
	if len(held) > 0 && in.Quarantine == "" {
		if wantsPage(r) != "" {
			s.render(w, http.StatusConflict, "zipconfirm", pageData{Title: "Quarantined files", Context: contextToken(r), Viewer: v, Matter: &m,
				Data: map[string]any{"Held": held, "Input": in}})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{"code": "quarantine_confirmation_required",
			"message": "Some files have quarantined versions: include after confirming, or leave them out", "files": held})
		return
	}
	if in.Quarantine == "leave_out" {
		kept := entries[:0]
		for _, e := range entries {
			if !e.Quarantined {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if len(entries) == 0 {
		s.fail(w, r, errNothing)
		return
	}
	for _, e := range entries {
		if !validPath(e.Path) {
			s.fail(w, r, unprocessable("an entry path could not be made valid", nil))
			return
		}
	}
	firm, err := s.firm(ctx, v.Token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	zone, zerr := time.LoadLocation(firm.TimeZone)
	if zerr != nil || firm.TimeZone == "" {
		zone = time.UTC
	}
	zipName := cleanSegment(m.Number) + "-documents-" + s.c.Now().In(zone).Format(time.DateOnly) + ".zip"

	// The frozen manifest, before the stream starts.
	bulkID := s.id()
	if err := s.writeManifest(ctx, bulkID, v, m, in, label, zipName, entries); err != nil {
		s.fail(w, r, err)
		return
	}
	var sel []map[string]string
	files := map[string]bool{}
	for _, e := range entries {
		x := map[string]string{"file_id": e.FileID, "path": e.Path}
		if e.Kind == "markdown" {
			x["rendition_id"] = e.RenditionID
		} else {
			x["version_id"] = e.VersionID
		}
		sel = append(sel, x)
		files[e.FileID] = true
	}
	body, _ := json.Marshal(map[string]any{"entries": sel, "confirm_quarantine": in.Quarantine == "include"})
	resp, err := s.request(ctx, http.MethodPost, "/api/v1/file-archives", v.Token, map[string]string{"Content-Type": "application/json"}, strings.NewReader(string(body)))
	if err == nil && !success(resp.StatusCode) {
		err = readProblem(resp, "/api/v1/file-archives")
		resp.Body.Close()
		resp = nil
	}
	if err != nil {
		s.finishBulk(context.WithoutCancel(ctx), bulkID, "interrupted", int64(0))
		s.fail(w, r, err)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(zipName, `"`, "")+`"`)
	w.WriteHeader(http.StatusOK)
	n, err := io.Copy(w, resp.Body)
	if err == nil {
		err = ctx.Err()
	}
	bg := context.WithoutCancel(ctx)
	if err != nil {
		s.c.Log.Printf("documents: bulk %s interrupted after %d bytes: %v", bulkID, n, err)
		s.finishBulk(bg, bulkID, "interrupted", n)
		return
	}
	s.finishBulk(bg, bulkID, "complete", n)
	noun := "files"
	if len(files) == one {
		noun = "file"
	}
	if err := s.writeEvent(bg, v.Token, "documents.bulk_downloaded", m.ID, "Downloaded "+strconv.Itoa(len(files))+" "+noun+" as one zip"); err != nil {
		s.c.Log.Printf("documents: bulk %s: timeline event: %v", bulkID, err)
	}
}

func (s *Server) writeManifest(ctx context.Context, id string, v viewer, m coreMatter, in zipInput, label, zipName string, entries []Entry) error {
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO bulk_downloads (id, matter_id, actor_user_id, scope, folder_label, status_filter, category_filter, versions, with_markdown, layout, zip_name, entry_count, byte_count, status, started_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 0, 'started', $13)`,
		id, m.ID, v.UserID, in.Scope, label, in.Status, in.CategoryID, in.Versions, in.Markdown, in.Layout, zipName, len(entries), s.now()); err != nil {
		return err
	}
	for i, e := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bulk_download_items (bulk_id, position, file_id, version_number, kind, entry_path, sha256, size_bytes) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, i, e.FileID, e.VersionNumber, e.Kind, e.Path, e.SHA256, e.SizeBytes); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) finishBulk(ctx context.Context, id, status string, n int64) {
	if _, err := s.c.DB.ExecContext(ctx, `UPDATE bulk_downloads SET status = $2, byte_count = $3, finished_at = $4 WHERE id = $1`, id, status, n, s.now()); err != nil {
		s.c.Log.Printf("documents: bulk %s: %v", id, err)
	}
}

// Bulk is one manifest with its entries.
type Bulk struct {
	ID         string  `json:"id"`
	MatterID   string  `json:"matter_id"`
	Scope      string  `json:"scope"`
	ZipName    string  `json:"zip_name"`
	EntryCount int     `json:"entry_count"`
	ByteCount  int64   `json:"byte_count"`
	Status     string  `json:"status"`
	StartedAt  int64   `json:"started_at"`
	Items      []Entry `json:"items"`
}

func (s *Server) bulk(ctx context.Context, id string) (Bulk, error) {
	var b Bulk
	err := s.c.DB.QueryRowContext(ctx, `SELECT id, matter_id, scope, zip_name, entry_count, byte_count, status, started_at FROM bulk_downloads WHERE id = $1`, id).
		Scan(&b.ID, &b.MatterID, &b.Scope, &b.ZipName, &b.EntryCount, &b.ByteCount, &b.Status, &b.StartedAt)
	if err != nil {
		return b, notFound()
	}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT COALESCE(file_id, ''), version_number, kind, entry_path, sha256, size_bytes FROM bulk_download_items WHERE bulk_id = $1 ORDER BY position`, id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	b.Items = []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.FileID, &e.VersionNumber, &e.Kind, &e.Path, &e.SHA256, &e.SizeBytes); err != nil {
			return b, err
		}
		b.Items = append(b.Items, e)
	}
	return b, rows.Err()
}

// bulksAPI lists a matter's manifests, newest first.
func (s *Server) bulksAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	if _, err := s.getMatter(ctx, v.Token, r.PathValue("matter_id")); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT id FROM bulk_downloads WHERE matter_id = $1 ORDER BY started_at DESC, id DESC LIMIT 50`, r.PathValue("matter_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []Bulk{}
	for _, id := range ids {
		b, err := s.bulk(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
