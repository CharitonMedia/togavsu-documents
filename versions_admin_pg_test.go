package documents

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/CharitonMedia/togavsu-documents/internal/fakecore"
	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
)

// MOD-DOC-33.
func TestPGDocumentsVersionTools(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.staff)
	m := e.matter.ID
	f := e.core.AddFile(fakecore.File{Name: "Motion.docx", MatterID: &m}, []byte("motion v1"), e.attorney.ID)
	v1 := e.core.FileVersions(f.ID)[0]
	v2 := e.core.AddVersion(f.ID, []byte("motion v2"), e.attorney.ID, "editor")
	v3 := e.core.AddVersion(f.ID, []byte("motion v3"), e.attorney.ID, "editor")
	e.core.AddRendition(f.ID, v1.ID, "markdown", []byte("# Motion\nThe petitioner asks.\nSigned.\n"))
	e.core.AddRendition(f.ID, v2.ID, "markdown", []byte("# Motion\nThe petitioner moves.\nSigned.\n"))

	// A note and label on version 2 show on the version list.
	e.want(e.do(http.MethodPost, "/api/files/"+f.ID+"/versions/2/note", tok, url.Values{"label": {"sent"}, "note": {"Sent to opposing counsel"}, "_return": {"/files/" + f.ID}}), http.StatusSeeOther)
	var fv FileVersions
	e.do(http.MethodGet, "/api/files/"+f.ID+"/versions", tok, nil).json(t, &fv)
	if len(fv.Versions) != 3 || fv.Versions[1].Number != 2 || fv.Versions[1].Label != "sent" || fv.Versions[1].Note != "Sent to opposing counsel" {
		t.Fatalf("versions %+v", fv.Versions)
	}
	if pg := e.page("/files/"+f.ID, tok); pg.Status != 200 || !strings.Contains(pg.str(), "Sent to opposing counsel") {
		t.Fatalf("version page %d", pg.Status)
	}
	// Without write access the write is 404, and a bad label is refused.
	ro := e.tok(e.readonly)
	if r := e.do(http.MethodPost, "/api/files/"+f.ID+"/versions/2/note", ro, map[string]string{"note": "x"}); r.Status != http.StatusNotFound {
		t.Fatalf("read-only write %d", r.Status)
	}
	e.want(e.do(http.MethodPost, "/api/files/"+f.ID+"/versions/2/note", tok, map[string]string{"label": "lost"}), http.StatusUnprocessableEntity)

	// Compare: one added and one removed line; no Markdown copy says so.
	var c Compare
	r := e.do(http.MethodGet, "/api/files/"+f.ID+"/compare?a=1&b=2", tok, nil)
	e.want(r, 200)
	r.json(t, &c)
	var add, del int
	for _, l := range c.Lines {
		switch l.Op {
		case "+":
			add++
		case "-":
			del++
		}
	}
	if add != 1 || del != 1 {
		t.Fatalf("diff %+v", c.Lines)
	}
	r = e.do(http.MethodGet, "/api/files/"+f.ID+"/compare?a=2&b=3", tok, nil)
	r.json(t, &c)
	if c.B.Ready || c.B.Message != "Markdown copy not ready" || c.B.DocTender == "" || len(c.Lines) != 0 {
		t.Fatalf("not ready %+v", c)
	}
	if pg := e.page("/files/"+f.ID+"/compare?a=2&b=3", tok); !strings.Contains(pg.str(), "Markdown copy not ready") {
		t.Fatalf("compare page:\n%s", pg.Body)
	}
	if pg := e.page("/files/"+f.ID+"/compare?a=1&b=2", tok); !strings.Contains(pg.str(), `class="add">&#43; The petitioner moves.`) {
		t.Fatalf("compare page:\n%s", pg.Body)
	}

	// Copy as new file: a second file whose v1 SHA-256 equals the source version's.
	r = e.do(http.MethodPost, "/api/files/"+f.ID+"/versions/2/copy", tok, nil)
	e.want(r, http.StatusCreated)
	var cp struct {
		FileID string `json:"file_id"`
		Name   string `json:"name"`
	}
	r.json(t, &cp)
	nf, _ := e.core.File(cp.FileID)
	if nf.Name != "Motion (from v2).docx" || nf.MatterID == nil || *nf.MatterID != m || nf.AttorneyOnly {
		t.Fatalf("copy %+v", nf)
	}
	if got := e.core.FileVersions(cp.FileID); len(got) != 1 || got[0].SHA256 != v2.SHA256 {
		t.Fatalf("copy versions %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM version_copies WHERE file_id = $1 AND source_file_id = $2 AND source_version_number = 2`, cp.FileID, f.ID); n != 1 {
		t.Fatal("no version_copies row")
	}

	// Save as template copies the current version into a library file.
	body := makeDOCX(t, para("{{matter_name}}"), nil)
	d := e.core.AddFile(fakecore.File{Name: "Good draft.docx", MatterID: &m}, body, e.attorney.ID)
	r = e.do(http.MethodPost, "/api/templates", tok, map[string]any{"mode": "from_file", "file_id": d.ID, "name": "From a draft"})
	e.want(r, http.StatusCreated)
	var tm Template
	r.json(t, &tm)
	if lib, _ := e.core.File(tm.BodyFileID); !lib.Library || sum(e.currentBytes(tm.BodyFileID)) != sum(body) {
		t.Fatal("save as template did not copy the current version into a library file")
	}
	if pg := e.page("/templates/new?mode=from_file&file_id="+d.ID, tok); pg.Status != 200 || !strings.Contains(pg.str(), "Good draft.docx") {
		t.Fatalf("save as template form %d", pg.Status)
	}
	_ = v3
}

// MOD-DOC-34.
func TestPGDocumentsAttorneys(t *testing.T) {
	e := newEnv(t)
	staff := e.tok(e.staff)
	att := e.tok(e.attorney)
	admin := e.tok(e.admin)
	for _, tok := range []string{staff, att} {
		if r := e.page("/attorneys", tok); r.Status != http.StatusNotFound {
			t.Fatalf("attorneys page %d", r.Status)
		}
		if r := e.do(http.MethodPost, "/api/attorneys/"+e.attorney.ID, tok, map[string]string{"bar_number": "x"}); r.Status != http.StatusNotFound {
			t.Fatalf("attorneys write %d", r.Status)
		}
	}
	pg := e.page("/attorneys", admin)
	if pg.Status != 200 || !strings.Contains(pg.str(), "Jordan Q. Testlawyer") || strings.Contains(pg.str(), "Sam Staff") {
		t.Fatalf("attorneys page:\n%s", pg.Body)
	}
	e.want(e.do(http.MethodPost, "/api/attorneys/"+e.attorney.ID, admin, map[string]string{"bar_state": "Texas"}), http.StatusUnprocessableEntity)
	e.want(e.do(http.MethodPost, "/api/attorneys/"+e.staff.ID, admin, map[string]string{"bar_number": "1"}), http.StatusNotFound)
	e.want(e.do(http.MethodPost, "/api/attorneys/"+e.attorney.ID, admin, url.Values{"bar_number": {"00001"}, "bar_state": {"TX"}, "_return": {"/attorneys"}}), http.StatusSeeOther)
	res, err := e.srv.resolve(e.ctx(), att, resolveInput{MatterID: e.matter.ID})
	if err != nil || res.Values["attorney_bar_number"].Value != "00001" {
		t.Fatalf("bar number %q %v", res.Values["attorney_bar_number"].Value, err)
	}
	st, body := e.core.Call(http.MethodGet, "/api/v1/users/"+e.attorney.ID+"?include=documents.attorney", admin, nil)
	if st != 200 || !strings.Contains(string(body), `"bar_number":"00001"`) {
		t.Fatalf("include: %d %s", st, body)
	}
}

// MOD-DOC-35 and 36.
func TestPGDocumentsEventsRelease(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	admin := e.tok(e.admin)
	tm := e.uploadTemplate(tok, "Letter", makeDOCX(t, para("{{matter_number}}"), nil), nil)
	r := e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	fid := run.FileIDs[0]
	e.want(e.do(http.MethodPost, "/api/drafts/"+fid+"/status", tok, map[string]string{"status": "draft"}), 200)
	e.want(e.do(http.MethodPost, "/api/files/"+fid+"/versions/1/note", tok, map[string]string{"note": "kept"}), 200)

	// 35: file.updated marks the cache once; a repeat changes nothing; a bad signature is 401.
	cache := func() (string, int64) {
		var s string
		var at int64
		_ = e.db.QueryRow(`SELECT status_cache, updated_at FROM generated_files WHERE file_id = $1`, fid).Scan(&s, &at)
		return s, at
	}
	if st := e.core.Deliver(e.mod.URL, "ev-bad", "file.updated", fid, "wrong-secret"); st != http.StatusUnauthorized {
		t.Fatalf("bad signature %d", st)
	}
	if s, _ := cache(); s != "draft" || e.count(`SELECT count(*) FROM received_events`) != 0 {
		t.Fatal("a bad signature changed something")
	}
	e.now = e.now.Add(1e9)
	if st := e.core.Deliver(e.mod.URL, "ev-1", "file.updated", fid, e.core.Secret); st != 200 {
		t.Fatalf("delivery %d", st)
	}
	s1, at1 := cache()
	if s1 != "stale" {
		t.Fatalf("cache %q", s1)
	}
	e.now = e.now.Add(1e9)
	if st := e.core.Deliver(e.mod.URL, "ev-1", "file.updated", fid, e.core.Secret); st != 200 {
		t.Fatalf("repeat %d", st)
	}
	if s2, at2 := cache(); s2 != s1 || at2 != at1 || e.count(`SELECT count(*) FROM received_events`) != 1 {
		t.Fatal("a repeated event changed something")
	}
	// The next viewer read refreshes the cache from the core.
	e.drafts(tok, "")
	if s, _ := cache(); s != "draft" {
		t.Fatalf("refreshed cache %q", s)
	}
	// A stale timestamp is refused too.
	body := []byte(`{"id":"ev-old","action":"file.updated","record_id":"` + fid + `"}`)
	req, _ := http.NewRequest(http.MethodPost, e.mod.URL+"/events", strings.NewReader(string(body)))
	req.Header.Set(modkit.SignatureHeader, modkit.SignWebhook(e.core.Secret, e.now.Add(-6*60e9), body))
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale timestamp accepted")
	}

	// A zip manifest row on the file.
	zr := e.do(http.MethodPost, "/api/matters/"+e.matter.ID+"/zip", tok, map[string]any{"scope": "selection", "file_ids": []string{fid}})
	e.want(zr, 200)
	var entry, sha string
	_ = e.db.QueryRow(`SELECT entry_path, sha256 FROM bulk_download_items WHERE file_id = $1`, fid).Scan(&entry, &sha)

	// 36: purge of a trashed generated file is refused naming documents; after Release
	// it succeeds and the manifest row keeps its path and SHA-256 with file_id null.
	e.core.Trash(fid)
	e.core.Deliver(e.mod.URL, "ev-2", "file.trashed", fid, e.core.Secret)
	if n := e.count(`SELECT count(*) FROM generated_files WHERE file_id = $1 AND trashed`, fid); n != 1 {
		t.Fatal("file.trashed not recorded")
	}
	st, b := e.core.Call(http.MethodPost, "/api/v1/files/"+fid+"/purge", admin, map[string]string{"confirm_name": "x"})
	if st != http.StatusConflict || !strings.Contains(string(b), "documents") {
		t.Fatalf("purge before release: %d %s", st, b)
	}
	for _, tk := range []string{tok, e.tok(e.staff)} {
		if r := e.page("/release", tk); r.Status != http.StatusNotFound {
			t.Fatalf("release page for non-admin %d", r.Status)
		}
	}
	pg := e.page("/release", admin)
	if pg.Status != 200 || !strings.Contains(pg.str(), "generated file") {
		t.Fatalf("release page:\n%s", pg.Body)
	}
	e.want(e.do(http.MethodPost, "/api/release/"+fid, admin, url.Values{"_return": {"/release"}}), http.StatusSeeOther)
	st, b = e.core.Call(http.MethodPost, "/api/v1/files/"+fid+"/purge", admin, map[string]string{"confirm_name": "x"})
	if st != 200 {
		t.Fatalf("purge after release: %d %s", st, b)
	}
	var path2, sha2 string
	var nullID *string
	if err := e.db.QueryRow(`SELECT entry_path, sha256, file_id FROM bulk_download_items WHERE entry_path = $1`, entry).Scan(&path2, &sha2, &nullID); err != nil {
		t.Fatal(err)
	}
	if path2 != entry || sha2 != sha || nullID != nil {
		t.Fatalf("manifest row %q %q %v", path2, sha2, nullID)
	}
	if e.count(`SELECT count(*) FROM version_notes WHERE file_id = $1`, fid) != 0 || e.count(`SELECT count(*) FROM generated_files WHERE file_id = $1`, fid) != 0 {
		t.Fatal("release left rows")
	}

	// Release of a template body needs its name typed and deletes the template.
	e.core.Trash(tm.BodyFileID)
	e.core.Deliver(e.mod.URL, "ev-3", "file.trashed", tm.BodyFileID, e.core.Secret)
	e.want(e.do(http.MethodPost, "/api/release/"+tm.BodyFileID, admin, map[string]string{"confirm_name": "Wrong"}), http.StatusUnprocessableEntity)
	e.want(e.do(http.MethodPost, "/api/release/"+tm.BodyFileID, admin, map[string]string{"confirm_name": "Letter"}), 200)
	if e.count(`SELECT count(*) FROM templates WHERE id = $1`, tm.ID) != 0 {
		t.Fatal("the template is still there")
	}
	if st, b := e.core.Call(http.MethodPost, "/api/v1/files/"+tm.BodyFileID+"/purge", admin, nil); st != 200 {
		t.Fatalf("template body purge %d %s", st, b)
	}
}

// MOD-DOC-37 and 38.
func TestPGDocumentsAccess(t *testing.T) {
	e := newEnv(t)
	att := e.tok(e.attorney)
	tm := e.uploadTemplate(att, "Motion to withdraw", makeDOCX(t, para("{{matter_number}}"), nil), map[string]string{"description": "Leave of court"})
	e.uploadTemplate(att, "Engagement letter", makeDOCX(t, para("{{matter_number}}"), nil), map[string]string{"topics": "intake"})
	arch := e.uploadTemplate(att, "Withdrawn old", makeDOCX(t, para("x"), nil), nil)
	e.want(e.do(http.MethodPost, "/api/templates/"+arch.ID+"/archive", att, nil), 200)

	// 37: search for admin, attorney and staff; quick-create; help pages in the manifest.
	for _, u := range []*fakecore.User{e.admin, e.attorney, e.staff} {
		pg := e.page("/search?q=withdraw", e.tok(u))
		if pg.Status != 200 || !strings.Contains(pg.str(), "Motion to withdraw") || strings.Contains(pg.str(), "Withdrawn old") || strings.Contains(pg.str(), "Engagement letter") {
			t.Fatalf("search for %s:\n%s", u.Name, pg.Body)
		}
		var hits struct{ Data []map[string]string }
		e.do(http.MethodGet, "/api/search?q=intake", e.tok(u), nil).json(t, &hits)
		if len(hits.Data) != 1 || hits.Data[0]["title"] != "Engagement letter" {
			t.Fatalf("topic search %+v", hits)
		}
	}
	qc := e.page("/quick/new", att)
	link := "/m/documents/matter/new?"
	if qc.Status != 200 || !strings.Contains(qc.str(), "2026-0001 Sample v. Example") || !strings.Contains(qc.str(), link) {
		t.Fatalf("quick-create:\n%s", qc.Body)
	}
	pick := e.page("/matter/new?matter_id="+e.matter.ID, att)
	if pick.Status != 200 || !strings.Contains(pick.str(), "Motion to withdraw") || strings.Contains(pick.str(), "Withdrawn old") {
		t.Fatalf("picker:\n%s", pick.Body)
	}
	asm := e.page("/matter/new?matter_id="+e.matter.ID+"&template_id="+tm.ID, att)
	if asm.Status != 200 || !strings.Contains(asm.str(), "2026-0001") || !strings.Contains(asm.str(), "Matter") {
		t.Fatalf("assemble:\n%s", asm.Body)
	}

	// 38: accountant and client get 404 on every page and endpoint.
	for _, u := range []*fakecore.User{e.accountant, e.client} {
		tok := e.tok(u)
		for _, rt := range e.srv.routes() {
			if rt.Path == "/api/openapi.json" || rt.Path == "/api/{$}" {
				continue
			}
			p := pathParamFill.Replace(rt.Path)
			var r resp
			if rt.Page {
				r = e.page(p, tok)
			} else {
				r = e.do(rt.Method, p, tok, map[string]any{})
			}
			if r.Status != http.StatusNotFound {
				t.Errorf("%s %s %s = %d", u.Name, rt.Method, p, r.Status)
			}
		}
	}
	// No context token at all is 401.
	e.want(e.do(http.MethodGet, "/api/library", "", nil), http.StatusUnauthorized)
}

var pathParamFill = strings.NewReplacer("{id}", zeroID, "{matter_id}", zeroID, "{file_id}", zeroID, "{number}", "1", "{user_id}", zeroID)

// Every page draws for an admin on the synthetic matter, so no page template has an
// error that only shows at run time.
func TestPGDocumentsPagesDraw(t *testing.T) {
	e := newEnv(t)
	admin := e.tok(e.admin)
	m := e.matter.ID
	tm := e.uploadTemplate(admin, "Letter", makeDOCX(t, para("{{matter_number}} {{slot:sig}}"), nil), nil)
	comp := e.uploadComponent(admin, "Sig", "signature", makeDOCX(t, para("{{attorney_name}}"), nil))
	var pk Package
	e.do(http.MethodPost, "/api/packages", admin, map[string]any{"name": "Set", "template_ids": []string{tm.ID}}).json(t, &pk)
	f := e.core.AddFile(fakecore.File{Name: "Doc.docx", MatterID: &m}, []byte("x"), e.admin.ID)
	e.core.Trash(f.ID)
	e.core.Deliver(e.mod.URL, "ev-t", "file.trashed", f.ID, e.core.Secret)
	g := e.core.AddFile(fakecore.File{Name: "Kept.docx", MatterID: &m}, []byte("y"), e.admin.ID)
	e.want(e.do(http.MethodPost, "/api/files/"+g.ID+"/versions/1/note", admin, map[string]string{"note": "n"}), 200)
	e.core.Trash(g.ID)
	e.core.Deliver(e.mod.URL, "ev-g", "file.trashed", g.ID, e.core.Secret)
	pages := []string{"/templates", "/templates/new", "/templates/new?mode=blank", "/templates/" + tm.ID, "/components/" + comp.ID, "/packages/" + pk.ID,
		"/drafts", "/drafts?status=final", "/widget/drafts", "/matter?matter_id=" + m, "/matter/new?matter_id=" + m, "/matter/download?matter_id=" + m,
		"/matter/versions?matter_id=" + m, "/matter/recent?matter_id=" + m, "/quick/new", "/search?q=letter", "/attorneys", "/release"}
	for _, p := range pages {
		r := e.page(p, admin)
		if r.Status != 200 || !strings.Contains(r.str(), "</html>") {
			t.Errorf("%s: %d\n%s", p, r.Status, r.Body)
		}
		if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("%s: CSP %q", p, csp)
		}
	}
	// A matter page takes the matter from the context token too.
	if r := e.page("/matter", e.core.Token(e.admin.ID, m)); r.Status != 200 {
		t.Errorf("matter from context token: %d", r.Status)
	}
	// The OpenAPI document lists every JSON endpoint.
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	r := e.do(http.MethodGet, "/api/openapi.json", "", nil)
	e.want(r, 200)
	r.json(t, &doc)
	for _, rt := range e.srv.routes() {
		if rt.Page || rt.Path == "/api/openapi.json" || rt.Path == "/api/{$}" {
			continue
		}
		if _, ok := doc.Paths[Prefix+rt.Path][strings.ToLower(rt.Method)]; !ok {
			t.Errorf("OpenAPI lacks %s %s", rt.Method, rt.Path)
		}
	}
	for _, name := range []string{"style.css", "app.js"} {
		if r := e.do(http.MethodGet, "/static/"+name, "", nil); r.Status != 200 {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	if r := e.do(http.MethodGet, "/static/layout.html", "", nil); r.Status != http.StatusNotFound {
		t.Error("a page template is served as static")
	}
}
