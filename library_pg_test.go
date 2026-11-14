package documents

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// MOD-DOC-02: without editor:run the DOCX and PDF choice can't be picked; without
// doc_tender:read there is no category hint and no Doc Tender status line.
func TestPGDocumentsOptionalScopes(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	e.core.Scopes["editor:run"] = false
	e.core.Scopes["doc_tender:read"] = false
	r := e.page("/templates/new", tok)
	e.want(r, http.StatusOK)
	if !strings.Contains(r.str(), "Needs the editor permission") || !strings.Contains(r.str(), `value="docx_and_pdf" disabled`) {
		t.Fatalf("the PDF choice is offered without editor:run:\n%s", r.Body)
	}
	if strings.Contains(r.str(), "Category hint") {
		t.Fatal("the category hint shows without doc_tender:read")
	}
	r = e.do(http.MethodPost, "/api/templates", tok, multipartForm(map[string]string{"mode": "blank", "name": "Letter", "output": "docx_and_pdf"}, "", nil))
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "editor permission") {
		t.Fatalf("refusal: %s", r.Body)
	}
	cat := e.core.ID()
	e.core.Categories[cat] = "pleadings"
	r = e.do(http.MethodPost, "/api/templates", tok, multipartForm(map[string]string{"mode": "blank", "name": "Letter", "category_id": cat}, "", nil))
	e.want(r, http.StatusUnprocessableEntity)
	tm := e.uploadTemplate(tok, "Letter", makeDOCX(t, para("{{matter_number}}"), nil), nil)
	r = e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	r = e.page("/drafts", tok)
	e.want(r, http.StatusOK)
	if strings.Contains(r.str(), "Doc Tender") || strings.Contains(r.str(), "Waiting for Doc Tender") {
		t.Fatal("the Doc Tender status shows without doc_tender:read")
	}
	// With both scopes the choices come back.
	e.core.Scopes["editor:run"] = true
	e.core.Scopes["doc_tender:read"] = true
	r = e.page("/templates/new", tok)
	if strings.Contains(r.str(), "Needs the editor permission") || !strings.Contains(r.str(), "Category hint") {
		t.Fatal("choices missing with both scopes")
	}
	r = e.page("/drafts", tok)
	if !strings.Contains(r.str(), "Waiting for Doc Tender") {
		t.Fatalf("no Doc Tender status with the scope:\n%s", r.Body)
	}
}

// MOD-DOC-03 to 09.
func TestPGDocumentsLibrary(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)

	// 03: an upload makes a library file and a template row; Doc Tender doesn't process it.
	tm := e.uploadTemplate(tok, "Motion to withdraw", makeDOCX(t, para("{{document_title}} for {{client_full_name}}"), nil),
		map[string]string{"topics": "withdraw, counsel", "description": "Attorney withdrawal", "practice_area_ids": e.family})
	f, ok := e.core.File(tm.BodyFileID)
	if !ok || !f.Library || f.MatterID != nil || f.ContactID != nil || f.Name != "motion-to-withdraw.docx" {
		t.Fatalf("body file: %+v", f)
	}
	if n := e.count(`SELECT count(*) FROM templates WHERE id = $1 AND body_file_id = $2`, tm.ID, f.ID); n != 1 {
		t.Fatal("no template row")
	}
	st, body := e.core.Call(http.MethodGet, "/api/v1/files/"+f.ID+"/doc-tender-status", tok, nil)
	if st != 200 || !strings.Contains(string(body), "not_processed_library") {
		t.Fatalf("doc tender status %d %s", st, body)
	}

	// 04: a PDF, or a zip without word/document.xml, is refused; nothing published.
	files := len(e.core.Files)
	rows := e.count(`SELECT count(*) FROM templates`)
	r := e.do(http.MethodPost, "/api/templates", tok, multipartForm(map[string]string{"mode": "upload", "name": "PDF"}, "motion.pdf", []byte("%PDF-1.7")))
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "only DOCX files") {
		t.Fatalf("pdf reason: %s", r.Body)
	}
	d, _ := readDOCX(blankDOCX("x"))
	d.names = []string{"[Content_Types].xml"}
	noDoc, _ := d.bytes()
	r = e.do(http.MethodPost, "/api/templates", tok, multipartForm(map[string]string{"mode": "upload", "name": "Empty"}, "empty.docx", noDoc))
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "word/document.xml") {
		t.Fatalf("zip reason: %s", r.Body)
	}
	if len(e.core.Files) != files || e.count(`SELECT count(*) FROM templates`) != rows || e.core.Requests["POST /api/v1/file-uploads"] != 1 {
		t.Fatal("a refused upload published a file, staged an upload or wrote a row")
	}

	// 05: blank template.
	r = e.do(http.MethodPost, "/api/templates", tok, url.Values{"mode": {"blank"}, "name": {"Blank letter"}})
	e.want(r, http.StatusCreated)
	var blank Template
	r.json(t, &blank)
	if got := docText(t, e.currentBytes(blank.BodyFileID), documentPart); got != "{{document_title}}" {
		t.Fatalf("blank body = %q", got)
	}

	// 06: an editor save shows as the newest version and the next run records it.
	e.core.AddVersion(tm.BodyFileID, makeDOCX(t, para("{{document_title}} v2 for {{client_full_name}}"), nil), e.attorney.ID, "editor")
	var tv TemplateView
	r = e.do(http.MethodGet, "/api/templates/"+tm.ID, tok, nil)
	e.want(r, 200)
	r.json(t, &tv)
	if len(tv.Versions) != 2 || tv.Versions[0].Number != 2 || tv.Versions[0].How != "editor" {
		t.Fatalf("versions: %+v", tv.Versions)
	}
	if !strings.Contains(e.page("/templates/"+tm.ID, tok).str(), "v2") {
		t.Fatal("the template page lacks v2")
	}
	r = e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	if n := e.count(`SELECT template_version_number FROM generated_files WHERE file_id = $1`, run.FileIDs[0]); n != 2 {
		t.Fatalf("template_version_number = %d", n)
	}

	// 07: editing name, topics or areas adds no file version.
	r = e.do(http.MethodPatch, "/api/templates/"+tm.ID, tok, map[string]any{"name": "Motion to withdraw as counsel", "topics": []string{"withdraw"}, "practice_area_ids": []string{e.estate}})
	e.want(r, 200)
	if n := len(e.core.FileVersions(tm.BodyFileID)); n != 2 {
		t.Fatalf("metadata edit added a version: %d", n)
	}
	var edited Template
	r.json(t, &edited)
	if edited.Name != "Motion to withdraw as counsel" || len(edited.PracticeAreaIDs) != 1 || edited.PracticeAreaIDs[0] != e.estate {
		t.Fatalf("edit: %+v", edited)
	}

	// 08: archived template or package: 409, nothing written.
	r = e.do(http.MethodPost, "/api/packages", tok, map[string]any{"name": "Withdrawal set", "template_ids": []string{tm.ID, blank.ID}})
	e.want(r, http.StatusCreated)
	var pk Package
	r.json(t, &pk)
	e.want(e.do(http.MethodPost, "/api/templates/"+tm.ID+"/archive", tok, nil), 200)
	before := e.count(`SELECT count(*) FROM generation_runs`)
	uploads := e.core.Requests["POST /api/v1/file-uploads"]
	e.want(e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID}), http.StatusConflict)
	e.want(e.generate(tok, e.matter.ID, map[string]any{"package_id": pk.ID}), http.StatusConflict)
	e.want(e.do(http.MethodPost, "/api/templates/"+tm.ID+"/unarchive", tok, nil), 200)
	e.want(e.do(http.MethodPost, "/api/packages/"+pk.ID+"/archive", tok, nil), 200)
	e.want(e.generate(tok, e.matter.ID, map[string]any{"package_id": pk.ID}), http.StatusConflict)
	if e.count(`SELECT count(*) FROM generation_runs`) != before || e.core.Requests["POST /api/v1/file-uploads"] != uploads {
		t.Fatal("a refused archived run wrote a row or staged an upload")
	}
	// Archived items leave the pickers.
	var p Pickers
	e.do(http.MethodGet, "/api/matters/"+e.matter.ID+"/pickers", tok, nil).json(t, &p)
	for _, x := range p.Packages {
		if x.ID == pk.ID {
			t.Fatal("an archived package is in the picker")
		}
	}

	// 09: Check template on known, manual-only and slot tokens; unmapped slot refused.
	chk := e.uploadTemplate(tok, "Checked", makeDOCX(t, para("{{client_full_name}}")+para("{{school_name}}")+para("{{slot:caption}}"), nil), nil)
	var rep CheckReport
	r = e.do(http.MethodGet, "/api/templates/"+chk.ID+"/check", tok, nil)
	e.want(r, 200)
	r.json(t, &rep)
	if len(rep.Known) != 1 || rep.Known[0].Name != "client_full_name" || len(rep.Manual) != 1 || rep.Manual[0] != "school_name" ||
		len(rep.Slots) != 1 || rep.Slots[0].Slot != "caption" || rep.Slots[0].Mapped {
		t.Fatalf("check: %+v", rep)
	}
	r = e.do(http.MethodPost, "/api/templates/"+chk.ID+"/slots", tok, map[string]any{"slots": map[string]string{}})
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "caption") {
		t.Fatalf("unmapped refusal: %s", r.Body)
	}
	comp := e.uploadComponent(tok, "Caption", "caption", makeDOCX(t, para("IN THE {{matter_court}}"), nil))
	e.want(e.do(http.MethodPost, "/api/templates/"+chk.ID+"/slots", tok, url.Values{"slots.caption": {comp.ID}}), 200)
	r = e.do(http.MethodGet, "/api/templates/"+chk.ID+"/check", tok, nil)
	r.json(t, &rep)
	if !rep.Slots[0].Mapped || len(rep.Known) != 2 {
		t.Fatalf("after mapping: %+v", rep)
	}
	// The page shows the same report.
	if pg := e.page("/templates/"+chk.ID, tok); pg.Status != 200 || !strings.Contains(pg.str(), "school_name") {
		t.Fatalf("template page %d", pg.Status)
	}

	// The Templates tab searches and filters.
	pg := e.page("/templates?q=withdraw", tok)
	e.want(pg, 200)
	if !strings.Contains(pg.str(), "Motion to withdraw as counsel") || strings.Contains(pg.str(), "Blank letter") {
		t.Fatalf("search:\n%s", pg.Body)
	}
	var lib LibraryList
	e.do(http.MethodGet, "/api/library?practice_area_id="+e.estate, tok, nil).json(t, &lib)
	for _, x := range lib.Templates {
		if x.ID != tm.ID && len(x.PracticeAreaIDs) > 0 {
			t.Fatalf("area filter let %s through", x.Name)
		}
	}
}

// MOD-DOC-10: a component with a hyperlink or a numbered list is refused at save, naming
// the part.
func TestPGDocumentsComponentParts(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.staff)
	for part, body := range map[string]string{
		"hyperlink": `<w:p><w:hyperlink r:id="rId9"><w:r><w:t>site</w:t></w:r></w:hyperlink></w:p>`,
		"numbering": `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`,
	} {
		r := e.do(http.MethodPost, "/api/components", tok, multipartForm(map[string]string{"mode": "upload", "name": "Bad " + part}, "bad.docx", makeDOCX(t, body, nil)))
		e.want(r, http.StatusUnprocessableEntity)
		if !strings.Contains(r.str(), `"part":"`+part+`"`) {
			t.Fatalf("%s: %s", part, r.Body)
		}
	}
	if n := e.count(`SELECT count(*) FROM components`); n != 0 {
		t.Fatal("a refused component wrote a row")
	}
}

// MOD-DOC-24: a package over max_package_templates can't be saved.
func TestPGDocumentsPackageLimit(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	e.core.Settings["max_package_templates"] = "2"
	var ids []string
	for _, n := range []string{"One", "Two", "Three"} {
		ids = append(ids, e.uploadTemplate(tok, n, makeDOCX(t, para(n), nil), nil).ID)
	}
	r := e.do(http.MethodPost, "/api/packages", tok, map[string]any{"name": "Too big", "template_ids": ids})
	e.want(r, http.StatusUnprocessableEntity)
	e.want(e.do(http.MethodPost, "/api/packages", tok, map[string]any{"name": "Fits", "template_ids": ids[:2]}), http.StatusCreated)
	if n := e.count(`SELECT count(*) FROM packages`); n != 1 {
		t.Fatalf("%d packages", n)
	}
}
