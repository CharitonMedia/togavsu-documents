package documents

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Version tools on any file the viewer can see on a matter: notes and labels, compare
// of two versions' Markdown copies, and copy of one version as a new file.

var versionLabels = map[string]bool{"": true, "sent": true, "signed": true, "filed": true, "reviewed": true, "other": true}

// notReady is what compare shows for a version with no Markdown copy.
const notReady = "Markdown copy not ready"

// VersionRow is one core version with the module's note beside it.
type VersionRow struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	How         string `json:"how"`
	CreatedAt   string `json:"created_at"`
	Quarantined bool   `json:"quarantined"`
	Markdown    bool   `json:"markdown"`
	Label       string `json:"label"`
	Note        string `json:"note"`
}

// FileVersions is the version tools view of one file.
type FileVersions struct {
	FileID   string       `json:"file_id"`
	Name     string       `json:"name"`
	MatterID string       `json:"matter_id"`
	Versions []VersionRow `json:"versions"`
}

// matterFile reads a file the viewer can see that sits on a matter.
func (s *Server) matterFile(ctx context.Context, v viewer, id string) (coreFile, error) {
	f, err := s.getFile(ctx, v.Token, id)
	if err != nil {
		return f, err
	}
	if f.matter() == "" || f.TrashedAt != nil {
		return f, notFound()
	}
	return f, nil
}

func (s *Server) fileVersions(ctx context.Context, v viewer, id string) (FileVersions, []coreVersion, []coreRendition, error) {
	f, err := s.matterFile(ctx, v, id)
	if err != nil {
		return FileVersions{}, nil, nil, err
	}
	vs, err := s.versions(ctx, v.Token, id)
	if err != nil {
		return FileVersions{}, nil, nil, err
	}
	rs, err := listAll[coreRendition](ctx, s, "/api/v1/files/"+url.PathEscape(id)+"/renditions", nil, v.Token)
	if err != nil {
		return FileVersions{}, nil, nil, err
	}
	notes := map[int][2]string{}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT version_number, label, note FROM version_notes WHERE file_id = $1`, id)
	if err != nil {
		return FileVersions{}, nil, nil, err
	}
	for rows.Next() {
		var n int
		var label, note string
		if err := rows.Scan(&n, &label, &note); err != nil {
			rows.Close()
			return FileVersions{}, nil, nil, err
		}
		notes[n] = [2]string{label, note}
	}
	rows.Close()
	md := map[string]bool{}
	for _, r := range rs {
		if r.Kind == "markdown" {
			md[r.FileVersionID] = true
		}
	}
	out := FileVersions{FileID: f.ID, Name: f.Name, MatterID: f.matter(), Versions: []VersionRow{}}
	for _, ver := range vs {
		n := notes[ver.Number]
		out.Versions = append(out.Versions, VersionRow{ID: ver.ID, Number: ver.Number, SizeBytes: ver.SizeBytes, SHA256: ver.SHA256, How: ver.How,
			CreatedAt: ver.CreatedAt, Quarantined: ver.Quarantined, Markdown: md[ver.ID], Label: n[0], Note: n[1]})
	}
	sort.Slice(out.Versions, func(i, j int) bool { return out.Versions[i].Number > out.Versions[j].Number })
	return out, vs, rs, nil
}

func (s *Server) versionsAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	fv, _, _, err := s.fileVersions(r.Context(), v, r.PathValue("file_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, fv)
}

// writable checks the viewer's write access to a file with a write that changes
// nothing: updateFileMetadata with the file's own attorney_only value. The core answers
// 404 for a file the viewer can't see and 403 for one they can't change; both are 404.
func (s *Server) writable(ctx context.Context, v viewer, f coreFile) error {
	err := s.call(ctx, http.MethodPatch, "/api/v1/files/"+url.PathEscape(f.ID), v.Token, map[string]any{"attorney_only": f.AttorneyOnly}, nil)
	if isStatus(err, http.StatusForbidden) || isStatus(err, http.StatusNotFound) {
		return notFound()
	}
	return err
}

type noteInput struct {
	Label string `json:"label"`
	Note  string `json:"note"`
}

func (s *Server) saveNote(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	var in noteInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Label = strings.TrimSpace(in.Label)
	if !versionLabels[in.Label] {
		s.fail(w, r, unprocessable("the label is sent, signed, filed, reviewed or other", nil))
		return
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		s.fail(w, r, notFound())
		return
	}
	f, err := s.matterFile(ctx, v, r.PathValue("file_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.writable(ctx, v, f); err != nil {
		s.fail(w, r, err)
		return
	}
	vs, err := s.versions(ctx, v.Token, f.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	found := false
	for _, ver := range vs {
		found = found || ver.Number == n
	}
	if !found {
		s.fail(w, r, notFound())
		return
	}
	if _, err := s.c.DB.ExecContext(ctx, `INSERT INTO version_notes (file_id, version_number, label, note, updated_by, updated_at) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (file_id, version_number) DO UPDATE SET label = EXCLUDED.label, note = EXCLUDED.note, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
		f.ID, n, in.Label, strings.TrimSpace(in.Note), v.UserID, s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusOK, map[string]any{"file_id": f.ID, "version_number": n, "label": in.Label, "note": strings.TrimSpace(in.Note)})
}

// DiffLine is one line of a compare: " " kept, "+" added, "-" removed.
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// diffLines is a line diff by longest common subsequence.
func diffLines(a, b []string) []DiffLine {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	out := []DiffLine{}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{" ", a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{"-", a[i]})
			i++
		default:
			out = append(out, DiffLine{"+", b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{"-", a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{"+", b[j]})
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// CompareSide is one of the two versions in a compare.
type CompareSide struct {
	Number    int    `json:"number"`
	Ready     bool   `json:"ready"`
	Message   string `json:"message,omitempty"`
	DocTender string `json:"doc_tender_status,omitempty"`
}

// Compare is the result of comparing two versions.
type Compare struct {
	FileID string      `json:"file_id"`
	Name   string      `json:"name"`
	A      CompareSide `json:"a"`
	B      CompareSide `json:"b"`
	Lines  []DiffLine  `json:"lines"`
}

func (s *Server) compare(ctx context.Context, v viewer, id string, na, nb int) (Compare, error) {
	fv, vs, rs, err := s.fileVersions(ctx, v, id)
	if err != nil {
		return Compare{}, err
	}
	out := Compare{FileID: id, Name: fv.Name, Lines: []DiffLine{}}
	g := s.grants(ctx, v.Token)
	read := func(n int, side *CompareSide) ([]string, error) {
		side.Number = n
		var ver *coreVersion
		for i := range vs {
			if vs[i].Number == n {
				ver = &vs[i]
			}
		}
		if ver == nil {
			return nil, notFound()
		}
		var md *coreRendition
		for i := range rs {
			if rs[i].Kind == "markdown" && rs[i].FileVersionID == ver.ID && (md == nil || rs[i].Number > md.Number) {
				md = &rs[i]
			}
		}
		if md == nil {
			side.Message = notReady
			if g.DocTender {
				if st, err := s.docTenderStatus(ctx, v.Token, id); err == nil {
					side.DocTender = st.Label
					if side.DocTender == "" {
						side.DocTender = st.Status
					}
				}
			}
			return nil, nil
		}
		b, err := s.download(ctx, "/api/v1/files/"+url.PathEscape(id)+"/renditions/"+url.PathEscape(md.ID), v.Token)
		if err != nil {
			return nil, err
		}
		side.Ready = true
		return splitLines(string(b)), nil
	}
	la, err := read(na, &out.A)
	if err != nil {
		return out, err
	}
	lb, err := read(nb, &out.B)
	if err != nil {
		return out, err
	}
	if out.A.Ready && out.B.Ready {
		out.Lines = diffLines(la, lb)
	}
	return out, nil
}

func (s *Server) compareAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	na, err1 := strconv.Atoi(q.Get("a"))
	nb, err2 := strconv.Atoi(q.Get("b"))
	if err1 != nil || err2 != nil {
		s.fail(w, r, unprocessable("pick two versions by number (a and b)", nil))
		return
	}
	c, err := s.compare(r.Context(), v, r.PathValue("file_id"), na, nb)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// copyName is "<base> (from v<n>).<ext>".
func copyName(name string, n int) string {
	base, ext := splitName(name)
	return base + " (from v" + strconv.Itoa(n) + ")" + ext
}

// copyVersion copies one version into a new file on the same matter, with the same
// attorney_only, recorded in version_copies.
func (s *Server) copyVersion(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		s.fail(w, r, notFound())
		return
	}
	f, err := s.matterFile(ctx, v, r.PathValue("file_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	vs, err := s.versions(ctx, v.Token, f.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var ver *coreVersion
	for i := range vs {
		if vs[i].Number == n {
			ver = &vs[i]
		}
	}
	if ver == nil {
		s.fail(w, r, notFound())
		return
	}
	b, err := s.download(ctx, versionPath(f.ID, ver.ID), v.Token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name := copyName(f.Name, n)
	media := ver.MediaType
	if media == "" {
		media = "application/octet-stream"
	}
	up, err := s.stageUpload(ctx, v.Token, name, media, f.matter(), false, b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body := map[string]any{"upload_id": up, "name": name, "matter_id": f.matter(), "attorney_only": f.AttorneyOnly, "shared_with_client": false}
	if f.FolderID != nil {
		body["folder_id"] = *f.FolderID
	}
	var nf coreFile
	if err := s.call(ctx, http.MethodPost, "/api/v1/files", v.Token, body, &nf); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.c.DB.ExecContext(ctx, `INSERT INTO version_copies (file_id, source_file_id, source_version_number, created_by, created_at) VALUES ($1, $2, $3, $4, $5)`,
		nf.ID, f.ID, n, v.UserID, s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusCreated, map[string]any{"file_id": nf.ID, "name": nf.Name, "source_file_id": f.ID, "source_version_number": n})
}
