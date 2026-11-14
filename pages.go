package documents

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Page handlers. Each page reads through the same calls as the JSON endpoints, as the
// viewer, and its forms post to those endpoints with a _return page.

func (s *Server) page(w http.ResponseWriter, r *http.Request, v viewer, name, title string, m *coreMatter, data any) {
	s.render(w, http.StatusOK, name, pageData{Title: title, Context: contextToken(r), Viewer: v, Matter: m,
		Grants: s.grants(r.Context(), v.Token), Data: data, Message: r.URL.Query().Get("message")})
}

// pageMatter reads the matter a matter page is for: matter_id in the query, else the
// context token's matter. A matter the viewer can't see is 404.
func (s *Server) pageMatter(r *http.Request, v viewer) (coreMatter, error) {
	id := r.URL.Query().Get("matter_id")
	if id == "" {
		id = v.MatterID
	}
	if id == "" {
		return coreMatter{}, notFound()
	}
	return s.getMatter(r.Context(), v.Token, id)
}

func (s *Server) categories(ctx context.Context, v viewer, g grants) []docCategory {
	if !g.DocTender {
		return nil
	}
	cats, err := listAll[docCategory](ctx, s, "/api/v1/doc-tender/categories", nil, v.Token)
	if err != nil {
		return nil
	}
	var out []docCategory
	for _, c := range cats {
		if c.Active {
			out = append(out, c)
		}
	}
	return out
}

// areaList is practice areas as id and name, by name.
type area struct{ ID, Name string }

func areaList(m map[string]string) []area {
	var out []area
	for id, n := range m {
		out = append(out, area{id, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) templatesPage(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	lq := libraryQuery{Q: q.Get("q"), PracticeAreaID: q.Get("practice_area_id"), DocClass: q.Get("doc_class"), Status: q.Get("status")}
	lib, areas, err := s.library(r.Context(), v.Token, lq)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "templates", "Templates", nil, map[string]any{"Lib": lib, "Areas": areaList(areas), "Query": lq, "Selected": q.Get("template_id")})
}

func (s *Server) newTemplatePage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	areas, err := s.practiceAreas(ctx, v.Token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	mode := q.Get("mode")
	if mode == "" {
		mode = "upload"
	}
	var source string
	if id := q.Get("file_id"); id != "" {
		f, err := s.getFile(ctx, v.Token, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		source = f.Name
		mode = "from_file"
	}
	g := s.grants(ctx, v.Token)
	s.page(w, r, v, "template_new", "New template", nil, map[string]any{"Mode": mode, "FileID": q.Get("file_id"), "Source": source,
		"Areas": areaList(areas), "Categories": s.categories(ctx, v, g)})
}

func (s *Server) templatePage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	tv, err := s.templateView(ctx, v, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rep, err := s.checkTemplate(ctx, v, tv.Template)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	comps, err := s.components(ctx, statusActive)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	areas, err := s.practiceAreas(ctx, v.Token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	g := s.grants(ctx, v.Token)
	s.page(w, r, v, "template", tv.Name, nil, map[string]any{"T": tv, "Check": rep, "Components": comps, "Areas": areaList(areas),
		"Categories": s.categories(ctx, v, g)})
}

func (s *Server) componentPage(w http.ResponseWriter, r *http.Request, v viewer) {
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
	s.page(w, r, v, "component", c.Name, nil, map[string]any{"C": c, "Versions": vs})
}

func (s *Server) packagePage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	p, err := s.pkg(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ts, err := s.templates(ctx, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names := map[string]string{}
	for _, t := range ts {
		names[t.ID] = t.Name
	}
	var members []Template
	for _, id := range p.TemplateIDs {
		for _, t := range ts {
			if t.ID == id {
				members = append(members, t)
			}
		}
	}
	s.page(w, r, v, "package", p.Name, nil, map[string]any{"P": p, "Members": members, "All": ts, "IDs": strings.Join(p.TemplateIDs, ",")})
}

func (s *Server) draftsPage(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	st := q.Get("status")
	if st != "final" {
		st = "draft"
	}
	var m *coreMatter
	if id := q.Get("matter_id"); id != "" {
		mm, err := s.getMatter(r.Context(), v.Token, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		m = &mm
	}
	dq := draftQuery{Status: st}
	if m != nil {
		dq.MatterID = m.ID
	}
	list, err := s.drafts(r.Context(), v, dq)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "drafts", "Drafts", m, map[string]any{"Drafts": list, "Status": st})
}

func (s *Server) widgetPage(w http.ResponseWriter, r *http.Request, v viewer) {
	list, days, err := s.widgetDrafts(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "widget", "Drafts", nil, map[string]any{"Drafts": list, "Days": days})
}

// matterFiles lists the matter's files the viewer can see, not trashed.
func (s *Server) matterFiles(ctx context.Context, v viewer, matterID string) ([]coreFile, error) {
	fs, err := listAll[coreFile](ctx, s, "/api/v1/files", url.Values{"matter_id": {matterID}}, v.Token)
	if err != nil {
		return nil, err
	}
	var out []coreFile
	for _, f := range fs {
		if f.TrashedAt == nil {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (s *Server) matterPage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	m, err := s.pageMatter(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.pickers(ctx, m)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	runs, err := s.recentRuns(ctx, m.ID, runsMatterRows)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	files, err := s.matterFiles(ctx, v, m.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "matter", "Documents", &m, map[string]any{"Pickers": p, "Runs": runs, "Files": files})
}

func (s *Server) assemblePage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	m, err := s.pageMatter(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	in := generateInput{TemplateID: q.Get("template_id"), PackageID: q.Get("package_id"), ClientContactID: q.Get("client_contact_id"),
		ServedPartyID: q.Get("served_party_id"), DocumentTitle: q.Get("document_title"), FilingDate: q.Get("filing_date")}
	if in.TemplateID == "" && in.PackageID == "" {
		p, err := s.pickers(ctx, m)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.page(w, r, v, "pick", "New from template", &m, p)
		return
	}
	a, _, _, err := s.assembly(ctx, v, m.ID, in, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "assemble", "Assemble", &m, map[string]any{"A": a, "In": in})
}

func (s *Server) downloadPage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	m, err := s.pageMatter(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	folders, err := listAll[coreFolder](ctx, s, "/api/v1/folders", url.Values{"matter_id": {m.ID}}, v.Token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	files, err := s.matterFiles(ctx, v, m.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.settings(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	g := s.grants(ctx, v.Token)
	s.page(w, r, v, "download", "Download files as zip", &m, map[string]any{"Folders": folders, "Files": files, "Layout": st["zip_layout"],
		"Categories": s.categories(ctx, v, g)})
}

func (s *Server) versionsPickPage(w http.ResponseWriter, r *http.Request, v viewer) {
	m, err := s.pageMatter(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	files, err := s.matterFiles(r.Context(), v, m.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "versions_pick", "Version tools", &m, files)
}

func (s *Server) recentPage(w http.ResponseWriter, r *http.Request, v viewer) {
	m, err := s.pageMatter(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	runs, err := s.recentRuns(r.Context(), m.ID, runsRecentRows)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "recent", "Recently generated", &m, runs)
}

func (s *Server) fileVersionsPage(w http.ResponseWriter, r *http.Request, v viewer) {
	fv, _, _, err := s.fileVersions(r.Context(), v, r.PathValue("file_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := s.getMatter(r.Context(), v.Token, fv.MatterID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "file", fv.Name, &m, fv)
}

func (s *Server) comparePage(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	na, err1 := strconv.Atoi(q.Get("a"))
	nb, err2 := strconv.Atoi(q.Get("b"))
	if err1 != nil || err2 != nil {
		s.fail(w, r, unprocessable("pick two versions to compare", nil))
		return
	}
	c, err := s.compare(r.Context(), v, r.PathValue("file_id"), na, nb)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "compare", "Compare", nil, c)
}

func (s *Server) quickPage(w http.ResponseWriter, r *http.Request, v viewer) {
	q := url.Values{"limit": {"50"}, "status": {"open"}}
	if t := strings.TrimSpace(r.URL.Query().Get("q")); t != "" {
		q.Set("q", t)
	}
	var page struct {
		Data []coreMatter `json:"data"`
	}
	if err := s.call(r.Context(), http.MethodGet, "/api/v1/matters?"+q.Encode(), v.Token, nil, &page); err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "quick", "New from template", nil, map[string]any{"Matters": page.Data, "Q": r.URL.Query().Get("q")})
}

func (s *Server) searchPage(w http.ResponseWriter, r *http.Request, v viewer) {
	ts, err := s.searchTemplates(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "search", "Templates", nil, map[string]any{"Templates": ts, "Q": r.URL.Query().Get("q")})
}

func (s *Server) attorneysPage(w http.ResponseWriter, r *http.Request, v viewer) {
	list, err := s.attorneys(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "attorneys", "Attorney profiles", nil, list)
}

func (s *Server) releasePage(w http.ResponseWriter, r *http.Request, v viewer) {
	list, err := s.held(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, r, v, "release", "Release trashed files", nil, list)
}
