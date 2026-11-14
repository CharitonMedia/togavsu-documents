package documents

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// route is one page or JSON endpoint. Every JSON endpoint is listed in the module's
// OpenAPI document at /api/openapi.json, so an agent reaches what a person reaches.
type route struct {
	Method  string
	Path    string
	Summary string
	Admin   bool
	Page    bool
	h       func(http.ResponseWriter, *http.Request, viewer)
	handler http.HandlerFunc
}

func (s *Server) routes() []route {
	rs := []route{
		// Pages.
		{Method: "GET", Path: "/templates", Summary: "Templates tab", Page: true, h: s.templatesPage},
		{Method: "GET", Path: "/templates/new", Summary: "New template form", Page: true, h: s.newTemplatePage},
		{Method: "GET", Path: "/templates/{id}", Summary: "Template page", Page: true, h: s.templatePage},
		{Method: "GET", Path: "/components/{id}", Summary: "Component page", Page: true, h: s.componentPage},
		{Method: "GET", Path: "/packages/{id}", Summary: "Package page", Page: true, h: s.packagePage},
		{Method: "GET", Path: "/drafts", Summary: "Drafts tab", Page: true, h: s.draftsPage},
		{Method: "GET", Path: "/widget/drafts", Summary: "Drafts widget", Page: true, h: s.widgetPage},
		{Method: "GET", Path: "/matter", Summary: "Matter tab", Page: true, h: s.matterPage},
		{Method: "GET", Path: "/matter/new", Summary: "New from template: pick and assemble", Page: true, h: s.assemblePage},
		{Method: "GET", Path: "/matter/download", Summary: "Download files as zip", Page: true, h: s.downloadPage},
		{Method: "GET", Path: "/matter/versions", Summary: "Version tools: pick a file", Page: true, h: s.versionsPickPage},
		{Method: "GET", Path: "/matter/recent", Summary: "Recently generated panel", Page: true, h: s.recentPage},
		{Method: "GET", Path: "/files/{file_id}", Summary: "Version tools for one file", Page: true, h: s.fileVersionsPage},
		{Method: "GET", Path: "/files/{file_id}/compare", Summary: "Compare two versions", Page: true, h: s.comparePage},
		{Method: "GET", Path: "/quick/new", Summary: "Quick-create: pick a matter", Page: true, h: s.quickPage},
		{Method: "GET", Path: "/search", Summary: "Template search", Page: true, h: s.searchPage},
		{Method: "GET", Path: "/attorneys", Summary: "Attorney profiles", Admin: true, Page: true, h: s.attorneysPage},
		{Method: "GET", Path: "/release", Summary: "Release trashed files", Admin: true, Page: true, h: s.releasePage},

		// Library.
		{Method: "GET", Path: "/api/library", Summary: "List templates, packages and components (q, practice_area_id, doc_class, status)", h: s.listLibrary},
		{Method: "GET", Path: "/api/search", Summary: "Search active templates by name, topics or description (q)", h: s.searchAPI},
		{Method: "POST", Path: "/api/templates", Summary: "Create a template: mode upload (multipart file), blank, or from_file (file_id)", h: s.createTemplate},
		{Method: "GET", Path: "/api/templates/{id}", Summary: "Get a template with its body's versions, newest first", h: s.getTemplate},
		{Method: "PATCH", Path: "/api/templates/{id}", Summary: "Edit template metadata; adds no file version", h: s.updateTemplate},
		{Method: "POST", Path: "/api/templates/{id}", Summary: "Edit template metadata (form post)", h: s.updateTemplate},
		{Method: "POST", Path: "/api/templates/{id}/archive", Summary: "Archive a template", h: s.setStatus("templates", statusArchived)},
		{Method: "POST", Path: "/api/templates/{id}/unarchive", Summary: "Unarchive a template", h: s.setStatus("templates", statusActive)},
		{Method: "GET", Path: "/api/templates/{id}/check", Summary: "Check template: tokens, slots and component problems", h: s.checkTemplateHandler},
		{Method: "POST", Path: "/api/templates/{id}/slots", Summary: "Save slot mappings (slots.<name>=component id); every slot must be mapped", h: s.saveSlots},
		{Method: "POST", Path: "/api/components", Summary: "Create a component: mode upload (multipart file) or blank", h: s.createComponent},
		{Method: "GET", Path: "/api/components/{id}", Summary: "Get a component", h: s.getComponent},
		{Method: "PATCH", Path: "/api/components/{id}", Summary: "Edit a component", h: s.updateComponent},
		{Method: "POST", Path: "/api/components/{id}", Summary: "Edit a component (form post)", h: s.updateComponent},
		{Method: "POST", Path: "/api/components/{id}/archive", Summary: "Archive a component", h: s.setStatus("components", statusArchived)},
		{Method: "POST", Path: "/api/components/{id}/unarchive", Summary: "Unarchive a component", h: s.setStatus("components", statusActive)},
		{Method: "POST", Path: "/api/packages", Summary: "Create a package: an ordered list of templates", h: s.createPackage},
		{Method: "GET", Path: "/api/packages/{id}", Summary: "Get a package", h: s.getPackage},
		{Method: "PATCH", Path: "/api/packages/{id}", Summary: "Edit a package", h: s.updatePackage},
		{Method: "POST", Path: "/api/packages/{id}", Summary: "Edit a package (form post)", h: s.updatePackage},
		{Method: "POST", Path: "/api/packages/{id}/archive", Summary: "Archive a package", h: s.setStatus("packages", statusArchived)},
		{Method: "POST", Path: "/api/packages/{id}/unarchive", Summary: "Unarchive a package", h: s.setStatus("packages", statusActive)},

		// Generate.
		{Method: "GET", Path: "/api/matters/{matter_id}/pickers", Summary: "Active templates and packages for the matter's practice area", h: s.pickersAPI},
		{Method: "GET", Path: "/api/matters/{matter_id}/assemble", Summary: "Resolve every token of a template or package on the matter", h: s.assembleAPI},
		{Method: "POST", Path: "/api/matters/{matter_id}/generate", Summary: "Generate drafts from a template or package (values.<token> for typed values)", h: s.generateAPI},
		{Method: "GET", Path: "/api/matters/{matter_id}/runs", Summary: "The matter's generation runs, newest first", h: s.runsAPI},
		{Method: "POST", Path: "/api/runs/{id}/retry-pdf", Summary: "Retry the PDFs of a pdf_failed run", h: s.retryPDF},

		// Drafts.
		{Method: "GET", Path: "/api/drafts", Summary: "Generated files by status (draft or final), filtered by matter_id", h: s.draftsAPI},
		{Method: "GET", Path: "/api/widget/drafts", Summary: "The widget's drafts: top eight of the last drafts_window_days", h: s.widgetAPI},
		{Method: "POST", Path: "/api/drafts/{file_id}/status", Summary: "Mark a generated file final or draft", h: s.setDraftStatus},

		// Zips.
		{Method: "POST", Path: "/api/matters/{matter_id}/zip", Summary: "Download the matter's files as one zip (scope, folder_id, file_ids, status, category_id, versions, with_markdown, layout, quarantine)", h: s.zipDownload},
		{Method: "GET", Path: "/api/matters/{matter_id}/zips", Summary: "The matter's zip manifests, newest first", h: s.bulksAPI},

		// Version tools.
		{Method: "GET", Path: "/api/files/{file_id}/versions", Summary: "A file's versions with notes and labels", h: s.versionsAPI},
		{Method: "POST", Path: "/api/files/{file_id}/versions/{number}/note", Summary: "Save a version's note and label", h: s.saveNote},
		{Method: "GET", Path: "/api/files/{file_id}/compare", Summary: "Line diff of two versions' Markdown copies (a, b)", h: s.compareAPI},
		{Method: "POST", Path: "/api/files/{file_id}/versions/{number}/copy", Summary: "Copy a version as a new file on the matter", h: s.copyVersion},

		// Admin.
		{Method: "GET", Path: "/api/attorneys", Summary: "Attorney profiles", Admin: true, h: s.attorneysAPI},
		{Method: "POST", Path: "/api/attorneys/{user_id}", Summary: "Save an attorney profile", Admin: true, h: s.saveAttorney},
		{Method: "GET", Path: "/api/release", Summary: "Trashed files the module holds", Admin: true, h: s.heldAPI},
		{Method: "POST", Path: "/api/release/{file_id}", Summary: "Release a trashed file so the core can purge it (confirm_name for a template or component body)", Admin: true, h: s.release},
	}
	for i := range rs {
		rs[i].handler = s.guard(rs[i].Admin, rs[i].h)
	}
	rs = append(rs, route{Method: "GET", Path: "/api/openapi.json", Summary: "This document", handler: s.openapi},
		route{Method: "GET", Path: "/api/{$}", Summary: "API index", handler: s.openapi})
	return rs
}

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// openapi describes every JSON endpoint. Each takes the context token in the
// X-Togavsu-Context-Token header (or ?context=) and answers JSON.
func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	paths := map[string]map[string]any{}
	for _, rt := range s.routes() {
		if rt.Page || !strings.HasPrefix(rt.Path, "/api/") || rt.Path == "/api/{$}" || rt.Path == "/api/openapi.json" {
			continue
		}
		p := Prefix + rt.Path
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		var params []map[string]any
		for _, m := range pathParam.FindAllStringSubmatch(rt.Path, -1) {
			params = append(params, map[string]any{"name": m[firstGroup], "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
		}
		op := map[string]any{"summary": rt.Summary, "responses": map[string]any{"200": map[string]string{"description": "JSON"}}}
		if params != nil {
			op["parameters"] = params
		}
		if rt.Admin {
			op["x-roles"] = []string{"admin"}
		} else {
			op["x-roles"] = []string{"admin", "attorney", "staff"}
		}
		paths[p][strings.ToLower(rt.Method)] = op
	}
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]string{"title": "Togavsu Documents and templates", "version": Version},
		"components": map[string]any{"securitySchemes": map[string]any{"context": map[string]string{
			"type": "apiKey", "in": "header", "name": "X-Togavsu-Context-Token"}}},
		"security": []map[string][]string{{"context": {}}},
		"paths":    paths,
	})
}
