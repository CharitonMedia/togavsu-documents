package documents

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// The template and component library: templates, components and packages. Bodies are
// core library files (CORE-17); the module keeps metadata rows that point at them.

// Template is one template row with its practice areas and slot mappings.
type Template struct {
	ID              string            `json:"id"`
	Slug            string            `json:"slug"`
	Name            string            `json:"name"`
	DocClass        string            `json:"doc_class"`
	Topics          []string          `json:"topics"`
	Description     string            `json:"description"`
	BodyFileID      string            `json:"body_file_id"`
	Output          string            `json:"output"`
	AttorneyOnly    bool              `json:"attorney_only"`
	CategoryID      string            `json:"category_id,omitempty"`
	Status          string            `json:"status"`
	PracticeAreaIDs []string          `json:"practice_area_ids"`
	Slots           map[string]string `json:"slots"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
}

// Component is a reusable piece (caption, signature block, certificate of service,
// letterhead) a slot pulls in.
type Component struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	BodyFileID  string `json:"body_file_id"`
	Status      string `json:"status"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Package is a named, ordered list of templates generated in one run.
type Package struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Status          string   `json:"status"`
	TemplateIDs     []string `json:"template_ids"`
	PracticeAreaIDs []string `json:"practice_area_ids"`
	UpdatedAt       int64    `json:"updated_at"`
}

const (
	statusActive   = "active"
	statusArchived = "archived"
)

var (
	docClasses    = map[string]bool{"pleading": true, "letter": true, "estate": true, "other": true}
	compKinds     = map[string]bool{"caption": true, "signature": true, "certificate": true, "letterhead": true, "other": true}
	outputs       = map[string]bool{"docx": true, "docx_and_pdf": true}
	slugStrip     = regexp.MustCompile(`[^a-z0-9]+`)
	errNoTemplate = notFound()
)

func slugify(name string) string {
	s := strings.Trim(slugStrip.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "template"
	}
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

// uniqueSlug finds a free slug in table: name, then name-2, name-3.
func (s *Server) uniqueSlug(ctx context.Context, table, name string) (string, error) {
	base := slugify(name)
	for n := 1; ; n++ {
		cand := base
		if n > 1 {
			cand = base + "-" + strconv.Itoa(n)
		}
		var exists bool
		if err := s.c.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE slug = $1)`, cand).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return cand, nil
		}
	}
}

func (s *Server) now() int64 { return s.c.Now().Unix() }

// Reads.

// pgMap reads Postgres arrays through database/sql.
var pgMap = pgtype.NewMap()

// textArray scans a TEXT[] column into dst.
func textArray(dst *[]string) sql.Scanner { return pgMap.SQLScanner(dst) }

func scanTemplate(row interface{ Scan(...any) error }) (Template, error) {
	var t Template
	var topics []string
	var cat sql.NullString
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.DocClass, textArray(&topics), &t.Description, &t.BodyFileID, &t.Output, &t.AttorneyOnly, &cat, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	t.Topics = topics
	if t.Topics == nil {
		t.Topics = []string{}
	}
	t.CategoryID = cat.String
	return t, err
}

const templateCols = `id, slug, name, doc_class, topics, description, body_file_id, output, attorney_only, category_id, status, created_at, updated_at`

func (s *Server) template(ctx context.Context, id string) (Template, error) {
	t, err := scanTemplate(s.c.DB.QueryRowContext(ctx, `SELECT `+templateCols+` FROM templates WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, errNoTemplate
	}
	if err != nil {
		return t, err
	}
	return t, s.templateLinks(ctx, &t)
}

func (s *Server) templateLinks(ctx context.Context, t *Template) error {
	t.PracticeAreaIDs = []string{}
	t.Slots = map[string]string{}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT practice_area_id FROM template_practice_areas WHERE template_id = $1 ORDER BY practice_area_id`, t.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		t.PracticeAreaIDs = append(t.PracticeAreaIDs, p)
	}
	rows.Close()
	rows, err = s.c.DB.QueryContext(ctx, `SELECT slot, component_id FROM template_slots WHERE template_id = $1`, t.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var slot, comp string
		if err := rows.Scan(&slot, &comp); err != nil {
			return err
		}
		t.Slots[slot] = comp
	}
	return rows.Err()
}

func (s *Server) templates(ctx context.Context, status string) ([]Template, error) {
	q := `SELECT ` + templateCols + ` FROM templates`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1`
		args = append(args, status)
	}
	rows, err := s.c.DB.QueryContext(ctx, q+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	for i := range out {
		if err := s.templateLinks(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Server) component(ctx context.Context, id string) (Component, error) {
	var c Component
	err := s.c.DB.QueryRowContext(ctx, `SELECT id, slug, name, category, description, body_file_id, status, updated_at FROM components WHERE id = $1`, id).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Category, &c.Description, &c.BodyFileID, &c.Status, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound()
	}
	return c, err
}

func (s *Server) components(ctx context.Context, status string) ([]Component, error) {
	q := `SELECT id, slug, name, category, description, body_file_id, status, updated_at FROM components`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1`
		args = append(args, status)
	}
	rows, err := s.c.DB.QueryContext(ctx, q+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Component
	for rows.Next() {
		var c Component
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Category, &c.Description, &c.BodyFileID, &c.Status, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Server) pkg(ctx context.Context, id string) (Package, error) {
	var p Package
	err := s.c.DB.QueryRowContext(ctx, `SELECT id, name, description, status, updated_at FROM packages WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.Status, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, notFound()
	}
	if err != nil {
		return p, err
	}
	return p, s.packageLinks(ctx, &p)
}

func (s *Server) packageLinks(ctx context.Context, p *Package) error {
	p.TemplateIDs, p.PracticeAreaIDs = []string{}, []string{}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT template_id FROM package_templates WHERE package_id = $1 ORDER BY position`, p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		p.TemplateIDs = append(p.TemplateIDs, t)
	}
	rows.Close()
	rows, err = s.c.DB.QueryContext(ctx, `SELECT practice_area_id FROM package_practice_areas WHERE package_id = $1 ORDER BY practice_area_id`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return err
		}
		p.PracticeAreaIDs = append(p.PracticeAreaIDs, a)
	}
	return rows.Err()
}

func (s *Server) packages(ctx context.Context, status string) ([]Package, error) {
	q := `SELECT id, name, description, status, updated_at FROM packages`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1`
		args = append(args, status)
	}
	rows, err := s.c.DB.QueryContext(ctx, q+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Package
	for rows.Next() {
		var p Package
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Status, &p.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	for i := range out {
		if err := s.packageLinks(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Search and filters.

// libraryQuery is the Templates tab's search and filters.
type libraryQuery struct {
	Q              string
	PracticeAreaID string
	DocClass       string
	Status         string
}

func matchesText(q string, fields ...string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// inArea reports whether an item listing areas serves practice area area: it names it,
// or names none.
func inArea(areas []string, area string) bool {
	if area == "" || len(areas) == 0 {
		return true
	}
	for _, a := range areas {
		if a == area {
			return true
		}
	}
	return false
}

func areaNames(areas map[string]string, ids []string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, areas[id])
	}
	return out
}

func (s *Server) practiceAreas(ctx context.Context, tok string) (map[string]string, error) {
	list, err := listAll[struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}](ctx, s, "/api/v1/practice-areas", nil, tok)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range list {
		out[a.ID] = a.Name
	}
	return out, nil
}

// LibraryList is the Templates tab's content.
type LibraryList struct {
	Templates  []Template  `json:"templates"`
	Packages   []Package   `json:"packages"`
	Components []Component `json:"components"`
}

func (s *Server) library(ctx context.Context, tok string, q libraryQuery) (LibraryList, map[string]string, error) {
	if q.Status == "" {
		q.Status = statusActive
	}
	if q.Status == "any" {
		q.Status = ""
	}
	areas, err := s.practiceAreas(ctx, tok)
	if err != nil {
		return LibraryList{}, nil, err
	}
	out := LibraryList{Templates: []Template{}, Packages: []Package{}, Components: []Component{}}
	ts, err := s.templates(ctx, q.Status)
	if err != nil {
		return out, nil, err
	}
	for _, t := range ts {
		if q.DocClass != "" && t.DocClass != q.DocClass {
			continue
		}
		if !inArea(t.PracticeAreaIDs, q.PracticeAreaID) {
			continue
		}
		fields := append([]string{t.Name, t.Description}, t.Topics...)
		if !matchesText(q.Q, append(fields, areaNames(areas, t.PracticeAreaIDs)...)...) {
			continue
		}
		out.Templates = append(out.Templates, t)
	}
	ps, err := s.packages(ctx, q.Status)
	if err != nil {
		return out, nil, err
	}
	for _, p := range ps {
		if inArea(p.PracticeAreaIDs, q.PracticeAreaID) && q.DocClass == "" && matchesText(q.Q, append([]string{p.Name, p.Description}, areaNames(areas, p.PracticeAreaIDs)...)...) {
			out.Packages = append(out.Packages, p)
		}
	}
	cs, err := s.components(ctx, q.Status)
	if err != nil {
		return out, nil, err
	}
	for _, c := range cs {
		if q.PracticeAreaID == "" && q.DocClass == "" && matchesText(q.Q, c.Name, c.Description, c.Category) {
			out.Components = append(out.Components, c)
		}
	}
	return out, areas, nil
}

func (s *Server) listLibrary(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	lib, _, err := s.library(r.Context(), v.Token, libraryQuery{Q: q.Get("q"), PracticeAreaID: q.Get("practice_area_id"), DocClass: q.Get("doc_class"), Status: q.Get("status")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lib)
}

// Writes.

// templateInput is the template metadata form.
type templateInput struct {
	Mode            string   `json:"mode"` // upload, blank or from_file
	FileID          string   `json:"file_id"`
	Name            string   `json:"name"`
	DocClass        string   `json:"doc_class"`
	Topics          []string `json:"topics"`
	Description     string   `json:"description"`
	Output          string   `json:"output"`
	AttorneyOnly    bool     `json:"attorney_only"`
	CategoryID      string   `json:"category_id"`
	PracticeAreaIDs []string `json:"practice_area_ids"`
}

// checkMetadata validates the template form against the approved optional scopes.
func (s *Server) checkMetadata(ctx context.Context, v viewer, in *templateInput, g grants) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return unprocessable("a template needs a name", map[string]any{"field": "name"})
	}
	if in.DocClass == "" {
		in.DocClass = "other"
	}
	if !docClasses[in.DocClass] {
		return unprocessable("the document class is pleading, letter, estate or other", map[string]any{"field": "doc_class"})
	}
	if in.Output == "" {
		st, err := s.settings(ctx)
		if err != nil {
			return err
		}
		in.Output = st["default_output"]
		if in.Output == "docx_and_pdf" && !g.Editor {
			in.Output = "docx"
		}
	}
	if !outputs[in.Output] {
		return unprocessable("the output is docx or docx_and_pdf", map[string]any{"field": "output"})
	}
	if in.Output == "docx_and_pdf" && !g.Editor {
		return unprocessable("DOCX and PDF output needs the editor permission", map[string]any{"field": "output"})
	}
	if in.CategoryID != "" && !g.DocTender {
		return unprocessable("a category hint needs the Doc Tender permission", map[string]any{"field": "category_id"})
	}
	if in.CategoryID != "" {
		cats, err := listAll[docCategory](ctx, s, "/api/v1/doc-tender/categories", nil, v.Token)
		if err != nil {
			return err
		}
		found := false
		for _, c := range cats {
			found = found || c.ID == in.CategoryID
		}
		if !found {
			return unprocessable("the category hint is not a Doc Tender category", map[string]any{"field": "category_id"})
		}
	}
	return s.checkAreas(ctx, v.Token, in.PracticeAreaIDs)
}

func (s *Server) checkAreas(ctx context.Context, tok string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	areas, err := s.practiceAreas(ctx, tok)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := areas[id]; !ok {
			return unprocessable("the practice area "+id+" doesn't exist", map[string]any{"field": "practice_area_ids"})
		}
	}
	return nil
}

func cleanTopics(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
	}
	return out
}

// uploadedFile reads the multipart "file" field.
func uploadedFile(r *http.Request) (string, []byte, error) {
	if err := parseForm(r); err != nil {
		return "", nil, unprocessable("send the DOCX as the file field of a multipart form", nil)
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		return "", nil, unprocessable("send the DOCX as the file field of a multipart form", nil)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return h.Filename, b, err
}

// body returns the DOCX a new template or component starts from: an upload, a blank
// body, or the current version of a file the viewer can read. Anything that isn't a DOCX
// is refused here, before anything is staged.
func (s *Server) body(r *http.Request, v viewer, mode, fileID, blankText string) ([]byte, error) {
	switch mode {
	case "upload", "":
		name, b, err := uploadedFile(r)
		if err != nil {
			return nil, err
		}
		if _, err := checkUpload(name, b); err != nil {
			return nil, unprocessable(err.Error(), map[string]any{"field": "file"})
		}
		return b, nil
	case "blank":
		return blankDOCX(blankText), nil
	case "from_file":
		f, err := s.getFile(r.Context(), v.Token, fileID)
		if err != nil {
			return nil, err
		}
		_, b, err := s.currentVersion(r.Context(), v.Token, fileID)
		if err != nil {
			return nil, err
		}
		if _, err := checkUpload(f.Name+"."+strings.TrimPrefix(f.Extension, "."), b); err != nil {
			if _, err2 := checkUpload(f.Name, b); err2 != nil {
				return nil, unprocessable(err.Error(), map[string]any{"field": "file_id"})
			}
		}
		return b, nil
	}
	return nil, unprocessable("mode is upload, blank or from_file", map[string]any{"field": "mode"})
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request, v viewer) {
	var in templateInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	t, err := s.newTemplate(r, v, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusCreated, t)
}

func (s *Server) newTemplate(r *http.Request, v viewer, in templateInput) (Template, error) {
	ctx := r.Context()
	g := s.grants(ctx, v.Token)
	if err := s.checkMetadata(ctx, v, &in, g); err != nil {
		return Template{}, err
	}
	b, err := s.body(r, v, in.Mode, in.FileID, "{{document_title}}")
	if err != nil {
		return Template{}, err
	}
	slug, err := s.uniqueSlug(ctx, "templates", in.Name)
	if err != nil {
		return Template{}, err
	}
	f, err := s.publishLibrary(ctx, v.Token, slug+".docx", b)
	if err != nil {
		return Template{}, err
	}
	t := Template{ID: s.id(), Slug: slug, Name: in.Name, DocClass: in.DocClass, Topics: cleanTopics(in.Topics), Description: in.Description,
		BodyFileID: f.ID, Output: in.Output, AttorneyOnly: in.AttorneyOnly, CategoryID: in.CategoryID, Status: statusActive,
		CreatedAt: s.now(), UpdatedAt: s.now()}
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO templates (`+templateCols+`, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11, $12, $13, $14)`,
		t.ID, t.Slug, t.Name, t.DocClass, t.Topics, t.Description, t.BodyFileID, t.Output, t.AttorneyOnly, t.CategoryID, t.Status, t.CreatedAt, t.UpdatedAt, v.UserID); err != nil {
		return t, err
	}
	if err := setAreas(ctx, tx, "template_practice_areas", "template_id", t.ID, in.PracticeAreaIDs); err != nil {
		return t, err
	}
	if err := tx.Commit(); err != nil {
		return t, err
	}
	return s.template(ctx, t.ID)
}

func setAreas(ctx context.Context, tx *sql.Tx, table, key, id string, areas []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+key+` = $1`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range areas {
		if seen[a] {
			continue
		}
		seen[a] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (`+key+`, practice_area_id) VALUES ($1, $2)`, id, a); err != nil {
			return err
		}
	}
	return nil
}

// TemplateView is a template with its body's version list, newest first.
type TemplateView struct {
	Template
	Versions []coreVersion `json:"versions"`
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request, v viewer) {
	tv, err := s.templateView(r.Context(), v, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tv)
}

func (s *Server) templateView(ctx context.Context, v viewer, id string) (TemplateView, error) {
	t, err := s.template(ctx, id)
	if err != nil {
		return TemplateView{}, err
	}
	vs, err := s.versions(ctx, v.Token, t.BodyFileID)
	if err != nil {
		return TemplateView{}, err
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Number > vs[j].Number })
	return TemplateView{Template: t, Versions: vs}, nil
}

// updateTemplate edits metadata only: it touches module rows and adds no file version.
func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	t, err := s.template(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in := templateInput{Name: t.Name, DocClass: t.DocClass, Topics: t.Topics, Description: t.Description, Output: t.Output,
		AttorneyOnly: t.AttorneyOnly, CategoryID: t.CategoryID, PracticeAreaIDs: t.PracticeAreaIDs}
	if r.Header.Get("Content-Type") != "application/json" {
		// A form sends every field; an unticked box is absent, so it reads as false.
		in.AttorneyOnly = false
	}
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.checkMetadata(ctx, v, &in, s.grants(ctx, v.Token)); err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE templates SET name = $2, doc_class = $3, topics = $4, description = $5, output = $6, attorney_only = $7,
category_id = NULLIF($8, ''), updated_at = $9 WHERE id = $1`, t.ID, in.Name, in.DocClass, cleanTopics(in.Topics), in.Description, in.Output, in.AttorneyOnly, in.CategoryID, s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := setAreas(ctx, tx, "template_practice_areas", "template_id", t.ID, in.PracticeAreaIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, r, err)
		return
	}
	t, err = s.template(ctx, t.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusOK, t)
}

// setStatus archives or unarchives a template, component or package. Archived items
// leave every picker; their files and history stay.
func (s *Server) setStatus(table, status string) func(http.ResponseWriter, *http.Request, viewer) {
	return func(w http.ResponseWriter, r *http.Request, v viewer) {
		res, err := s.c.DB.ExecContext(r.Context(), `UPDATE `+table+` SET status = $2, updated_at = $3 WHERE id = $1`, r.PathValue("id"), status, s.now())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			s.fail(w, r, notFound())
			return
		}
		s.done(w, r, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": status})
	}
}

// Check template.

// TokenReport is one token Check template found.
type TokenReport struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// SlotReport is one slot and its mapping.
type SlotReport struct {
	Slot        string `json:"slot"`
	ComponentID string `json:"component_id,omitempty"`
	Mapped      bool   `json:"mapped"`
}

// CheckReport is what Check template reports.
type CheckReport struct {
	Known    []TokenReport `json:"known"`
	Manual   []string      `json:"manual"`
	Slots    []SlotReport  `json:"slots"`
	Problems []string      `json:"problems"`
}

// cfKnown lists the custom field tokens defined in the firm's settings.
func (s *Server) cfKnown(ctx context.Context, tok string) (map[string]bool, error) {
	defs, err := listAll[coreCFDef](ctx, s, "/api/v1/custom-field-definitions", nil, tok)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, d := range defs {
		if d.Retired {
			continue
		}
		switch d.RecordType {
		case "matter":
			out["matter_cf_"+d.Key] = true
		case "contact":
			out["client_cf_"+d.Key] = true
		}
	}
	return out, nil
}

func (s *Server) checkTemplate(ctx context.Context, v viewer, t Template) (CheckReport, error) {
	rep := CheckReport{Known: []TokenReport{}, Manual: []string{}, Slots: []SlotReport{}, Problems: []string{}}
	_, b, err := s.currentVersion(ctx, v.Token, t.BodyFileID)
	if err != nil {
		return rep, err
	}
	d, err := readDOCX(b)
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
		return rep, nil
	}
	defs, err := s.cfKnown(ctx, v.Token)
	if err != nil {
		return rep, err
	}
	names := d.tokens()
	slots, err := d.slots()
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
	}
	for _, slot := range slots {
		comp, ok := t.Slots[slot]
		rep.Slots = append(rep.Slots, SlotReport{Slot: slot, ComponentID: comp, Mapped: ok})
		if !ok {
			rep.Problems = append(rep.Problems, "slot "+slot+" is not mapped to a component")
			continue
		}
		c, err := s.component(ctx, comp)
		if err != nil {
			rep.Problems = append(rep.Problems, "slot "+slot+" maps to a component that no longer exists")
			continue
		}
		if c.Status != statusActive {
			rep.Problems = append(rep.Problems, "slot "+slot+" maps to the archived component "+c.Name)
		}
		_, cb, err := s.currentVersion(ctx, v.Token, c.BodyFileID)
		if err != nil {
			return rep, err
		}
		cd, err := readDOCX(cb)
		if err != nil {
			rep.Problems = append(rep.Problems, "component "+c.Name+": "+err.Error())
			continue
		}
		if _, err := cd.componentBody(); err != nil {
			rep.Problems = append(rep.Problems, "component "+c.Name+": "+err.Error())
			continue
		}
		names = append(names, cd.tokens()...)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if isKnown(n, defs) {
			src := vocabulary[n]
			if src == "" {
				src = srcCustom
			}
			rep.Known = append(rep.Known, TokenReport{Name: n, Source: src})
		} else {
			rep.Manual = append(rep.Manual, n)
		}
	}
	return rep, nil
}

func (s *Server) checkTemplateHandler(w http.ResponseWriter, r *http.Request, v viewer) {
	t, err := s.template(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rep, err := s.checkTemplate(r.Context(), v, t)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// saveSlots maps the template's slots to components. Every slot in the body must be
// mapped, to an active component whose parts a slot can copy.
func (s *Server) saveSlots(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	t, err := s.template(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Slots map[string]string `json:"slots"`
	}
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	_, b, err := s.currentVersion(ctx, v.Token, t.BodyFileID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := readDOCX(b)
	if err != nil {
		s.fail(w, r, unprocessable(err.Error(), nil))
		return
	}
	slots, err := d.slots()
	if err != nil {
		s.fail(w, r, unprocessable(err.Error(), nil))
		return
	}
	inBody := map[string]bool{}
	var unmapped []string
	for _, slot := range slots {
		inBody[slot] = true
		if strings.TrimSpace(in.Slots[slot]) == "" {
			unmapped = append(unmapped, slot)
		}
	}
	if len(unmapped) > 0 {
		s.fail(w, r, unprocessable("every slot needs a component: "+strings.Join(unmapped, ", ")+" is not mapped", map[string]any{"unmapped": unmapped}))
		return
	}
	for slot, comp := range in.Slots {
		if !inBody[slot] {
			if comp == "" {
				continue
			}
			s.fail(w, r, unprocessable("the template has no slot "+slot, map[string]any{"slot": slot}))
			return
		}
		c, err := s.component(ctx, comp)
		if err != nil {
			s.fail(w, r, unprocessable("slot "+slot+" maps to a component that doesn't exist", map[string]any{"slot": slot}))
			return
		}
		if c.Status != statusActive {
			s.fail(w, r, unprocessable("slot "+slot+" maps to an archived component", map[string]any{"slot": slot}))
			return
		}
	}
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM template_slots WHERE template_id = $1`, t.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, slot := range slots {
		if _, err := tx.ExecContext(ctx, `INSERT INTO template_slots (template_id, slot, component_id) VALUES ($1, $2, $3)`, t.ID, slot, in.Slots[slot]); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE templates SET updated_at = $2 WHERE id = $1`, t.ID, s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, r, err)
		return
	}
	t, _ = s.template(ctx, t.ID)
	s.done(w, r, http.StatusOK, t)
}

// Components.

type componentInput struct {
	Mode        string `json:"mode"`
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

func (s *Server) createComponent(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	var in componentInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Category == "" {
		in.Category = "other"
	}
	if in.Name == "" || !compKinds[in.Category] {
		s.fail(w, r, unprocessable("a component needs a name and a category (caption, signature, certificate, letterhead or other)", nil))
		return
	}
	b, err := s.body(r, v, in.Mode, in.FileID, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := checkComponentBytes(b); err != nil {
		s.fail(w, r, err)
		return
	}
	slug, err := s.uniqueSlug(ctx, "components", in.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := s.publishLibrary(ctx, v.Token, slug+".docx", b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c := Component{ID: s.id(), Slug: slug, Name: in.Name, Category: in.Category, Description: in.Description, BodyFileID: f.ID, Status: statusActive, UpdatedAt: s.now()}
	if _, err := s.c.DB.ExecContext(ctx, `INSERT INTO components (id, slug, name, category, description, body_file_id, status, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`, c.ID, c.Slug, c.Name, c.Category, c.Description, c.BodyFileID, c.Status, v.UserID, c.UpdatedAt); err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusCreated, c)
}

// checkComponentBytes refuses a component body a slot can't copy, naming the part.
func checkComponentBytes(b []byte) error {
	d, err := readDOCX(b)
	if err != nil {
		return unprocessable(err.Error(), nil)
	}
	if _, err := d.componentBody(); err != nil {
		var pe *partError
		if errors.As(err, &pe) {
			return unprocessable(err.Error(), map[string]any{"part": pe.Part})
		}
		return unprocessable(err.Error(), nil)
	}
	return nil
}

func (s *Server) getComponent(w http.ResponseWriter, r *http.Request, v viewer) {
	c, err := s.component(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	vs, err := s.versions(r.Context(), v.Token, c.BodyFileID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Number > vs[j].Number })
	writeJSON(w, http.StatusOK, map[string]any{"component": c, "versions": vs})
}

func (s *Server) updateComponent(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	c, err := s.component(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in := componentInput{Name: c.Name, Category: c.Category, Description: c.Description}
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" || !compKinds[in.Category] {
		s.fail(w, r, unprocessable("a component needs a name and a category", nil))
		return
	}
	// The body may have changed in the editor since it was saved; check it again.
	_, b, err := s.currentVersion(ctx, v.Token, c.BodyFileID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := checkComponentBytes(b); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.c.DB.ExecContext(ctx, `UPDATE components SET name = $2, category = $3, description = $4, updated_at = $5 WHERE id = $1`,
		c.ID, strings.TrimSpace(in.Name), in.Category, in.Description, s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	c, _ = s.component(ctx, c.ID)
	s.done(w, r, http.StatusOK, c)
}

// Packages.

type packageInput struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	TemplateIDs     []string `json:"template_ids"`
	PracticeAreaIDs []string `json:"practice_area_ids"`
}

func (s *Server) savePackage(ctx context.Context, v viewer, id string, in packageInput) (Package, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Package{}, unprocessable("a package needs a name", nil)
	}
	if len(in.TemplateIDs) == 0 {
		return Package{}, unprocessable("a package needs at least one template", nil)
	}
	limit, err := s.intSetting(ctx, "max_package_templates")
	if err != nil {
		return Package{}, err
	}
	if len(in.TemplateIDs) > limit {
		return Package{}, unprocessable("a package holds at most "+strconv.Itoa(limit)+" templates", map[string]any{"limit": limit})
	}
	seen := map[string]bool{}
	for _, t := range in.TemplateIDs {
		if seen[t] {
			return Package{}, unprocessable("a package lists each template once", nil)
		}
		seen[t] = true
		if _, err := s.template(ctx, t); err != nil {
			return Package{}, unprocessable("the template "+t+" doesn't exist", nil)
		}
	}
	if err := s.checkAreas(ctx, v.Token, in.PracticeAreaIDs); err != nil {
		return Package{}, err
	}
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		return Package{}, err
	}
	defer tx.Rollback()
	if id == "" {
		id = s.id()
		if _, err := tx.ExecContext(ctx, `INSERT INTO packages (id, name, description, status, created_by, created_at, updated_at) VALUES ($1, $2, $3, 'active', $4, $5, $5)`,
			id, in.Name, in.Description, v.UserID, s.now()); err != nil {
			return Package{}, err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE packages SET name = $2, description = $3, updated_at = $4 WHERE id = $1`, id, in.Name, in.Description, s.now()); err != nil {
		return Package{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM package_templates WHERE package_id = $1`, id); err != nil {
		return Package{}, err
	}
	for i, t := range in.TemplateIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO package_templates (package_id, position, template_id) VALUES ($1, $2, $3)`, id, i, t); err != nil {
			return Package{}, err
		}
	}
	if err := setAreas(ctx, tx, "package_practice_areas", "package_id", id, in.PracticeAreaIDs); err != nil {
		return Package{}, err
	}
	if err := tx.Commit(); err != nil {
		return Package{}, err
	}
	return s.pkg(ctx, id)
}

func (s *Server) createPackage(w http.ResponseWriter, r *http.Request, v viewer) {
	var in packageInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.savePackage(r.Context(), v, "", in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusCreated, p)
}

func (s *Server) updatePackage(w http.ResponseWriter, r *http.Request, v viewer) {
	p, err := s.pkg(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in := packageInput{Name: p.Name, Description: p.Description, TemplateIDs: p.TemplateIDs, PracticeAreaIDs: p.PracticeAreaIDs}
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	p, err = s.savePackage(r.Context(), v, p.ID, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusOK, p)
}

func (s *Server) getPackage(w http.ResponseWriter, r *http.Request, v viewer) {
	p, err := s.pkg(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// searchTemplates is the Templates search scope: active templates whose name, topics or
// description match.
func (s *Server) searchTemplates(ctx context.Context, q string) ([]Template, error) {
	ts, err := s.templates(ctx, statusActive)
	if err != nil {
		return nil, err
	}
	out := []Template{}
	if strings.TrimSpace(q) == "" {
		return out, nil
	}
	for _, t := range ts {
		if matchesText(q, append([]string{t.Name, t.Description}, t.Topics...)...) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Server) searchAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	ts, err := s.searchTemplates(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var hits []map[string]string
	for _, t := range ts {
		hits = append(hits, map[string]string{"id": t.ID, "title": t.Name, "description": t.Description,
			"link": "/modules/" + ID + "/tabs/templates?" + url.Values{"template_id": {t.ID}}.Encode()})
	}
	if hits == nil {
		hits = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": hits})
}
