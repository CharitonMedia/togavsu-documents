package documents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/CharitonMedia/togavsu-documents/internal/fakecore"
)

func (e *env) drafts(tok, query string) []Draft {
	e.t.Helper()
	r := e.do(http.MethodGet, "/api/drafts"+query, tok, nil)
	e.want(r, 200)
	var out struct{ Data []Draft }
	r.json(e.t, &out)
	return out.Data
}

func draftIDs(ds []Draft) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.FileID)
	}
	sort.Strings(out)
	return out
}

// MOD-DOC-25 to 27.
func TestPGDocumentsDrafts(t *testing.T) {
	e := newEnv(t)
	att := e.tok(e.attorney)
	walled := e.core.AddMatter(fakecore.Matter{Number: "2026-0002", Name: "Walled v. Matter", PracticeAreaID: e.family, Wall: []string{e.attorney.ID, e.admin.ID}})
	tm := e.uploadTemplate(att, "Letter", makeDOCX(t, para("{{matter_number}}"), nil), nil)
	var open, closed Run
	r := e.generate(att, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	r.json(t, &open)
	r = e.generate(att, walled.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	r.json(t, &closed)

	// 25: Mark final removes the file from the default list; Mark draft returns it.
	if got := draftIDs(e.drafts(att, "")); len(got) != 2 {
		t.Fatalf("drafts %v", got)
	}
	e.want(e.do(http.MethodPost, "/api/drafts/"+open.FileIDs[0]+"/status", att, map[string]string{"status": "final"}), 200)
	if f, _ := e.core.File(open.FileIDs[0]); *f.Status != "final" {
		t.Fatal("status not final in the core")
	}
	if got := draftIDs(e.drafts(att, "")); len(got) != 1 || got[0] != closed.FileIDs[0] {
		t.Fatalf("after final %v", got)
	}
	if got := draftIDs(e.drafts(att, "?status=final")); len(got) != 1 || got[0] != open.FileIDs[0] {
		t.Fatalf("final filter %v", got)
	}
	if f, _ := e.core.File(open.FileIDs[0]); strings.Contains(strings.ToLower(f.Name), "final") {
		t.Fatal("the status entered the name")
	}
	r = e.do(http.MethodPost, "/api/drafts/"+open.FileIDs[0]+"/status", att, url.Values{"status": {"draft"}, "_return": {"/drafts"}})
	if r.Status != http.StatusSeeOther {
		t.Fatalf("form post: %d", r.Status)
	}
	if got := draftIDs(e.drafts(att, "")); len(got) != 2 {
		t.Fatalf("after draft %v", got)
	}

	// 26: staff off the wall sees none of the walled matter's drafts, and its pages 404.
	staff := e.tok(e.staff)
	if got := draftIDs(e.drafts(staff, "")); len(got) != 1 || got[0] != open.FileIDs[0] {
		t.Fatalf("staff drafts %v", got)
	}
	w := e.page("/widget/drafts", staff)
	if w.Status != 200 || strings.Contains(w.str(), walled.ID) {
		t.Fatalf("widget shows the walled matter:\n%s", w.Body)
	}
	for _, p := range []string{"/matter", "/matter/new", "/matter/download", "/matter/versions", "/matter/recent", "/drafts"} {
		if r := e.page(p+"?matter_id="+walled.ID, staff); r.Status != http.StatusNotFound {
			t.Errorf("%s on the walled matter: %d", p, r.Status)
		}
	}
	if r := e.do(http.MethodPost, "/api/matters/"+walled.ID+"/generate", staff, map[string]any{"template_id": tm.ID}); r.Status != http.StatusNotFound {
		t.Errorf("generate on the walled matter: %d", r.Status)
	}
	if r := e.do(http.MethodPost, "/api/drafts/"+closed.FileIDs[0]+"/status", staff, map[string]string{"status": "final"}); r.Status != http.StatusNotFound {
		t.Errorf("status write on the walled draft: %d", r.Status)
	}

	// 27: the widget's click-through names the drafts tab with matter_id, and the tab
	// shows only that matter's drafts.
	w = e.page("/widget/drafts", att)
	link := `/modules/documents/tabs/drafts?matter_id=` + e.matter.ID
	if !strings.Contains(w.str(), link) || !strings.Contains(w.str(), `data-navigate="/modules/documents/tabs/drafts"`) {
		t.Fatalf("widget link:\n%s", w.Body)
	}
	if got := draftIDs(e.drafts(att, "?matter_id="+e.matter.ID)); len(got) != 1 || got[0] != open.FileIDs[0] {
		t.Fatalf("matter filter %v", got)
	}
	tab := e.page("/drafts?matter_id="+e.matter.ID, att)
	if tab.Status != 200 || strings.Contains(tab.str(), closed.FileIDs[0]) || !strings.Contains(tab.str(), open.FileIDs[0]) {
		t.Fatalf("drafts tab:\n%s", tab.Body)
	}

	// The widget window: an old draft drops out.
	e.now = fixedNow.Add(31 * 24 * 3600e9)
	if w := e.page("/widget/drafts", att); !strings.Contains(w.str(), "No drafts in the last 30 days") {
		t.Fatalf("widget window:\n%s", w.Body)
	}
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type zipWorld struct {
	pleadA, pleadB, corrA, corrB, unfiled, trashed, attOnly *fakecore.File
	pleadings                                               string
}

// zipWorld is MOD-DOC-28's matter: four files in two folders, one unfiled, one trashed,
// and one attorney-only.
func (e *env) zipWorld() zipWorld {
	c := e.core
	m := e.matter.ID
	pl := c.AddFolder(m, "Pleadings")
	co := c.AddFolder(m, "Correspondence")
	final := "final"
	z := zipWorld{pleadings: pl}
	z.pleadA = c.AddFile(fakecore.File{Name: "Petition.docx", MatterID: &m, FolderID: &pl, Status: &final}, []byte("petition v1"), e.attorney.ID)
	c.AddVersion(z.pleadA.ID, []byte("petition v2"), e.attorney.ID, "editor")
	z.pleadB = c.AddFile(fakecore.File{Name: "Answer.docx", MatterID: &m, FolderID: &pl}, []byte("answer"), e.attorney.ID)
	z.corrA = c.AddFile(fakecore.File{Name: "Letter.docx", MatterID: &m, FolderID: &co, Status: &final}, []byte("letter"), e.attorney.ID)
	z.corrB = c.AddFile(fakecore.File{Name: "Reply.pdf", MatterID: &m, FolderID: &co}, []byte("reply"), e.attorney.ID)
	z.unfiled = c.AddFile(fakecore.File{Name: "Notes.txt", MatterID: &m}, []byte("notes"), e.attorney.ID)
	z.trashed = c.AddFile(fakecore.File{Name: "Old.docx", MatterID: &m, FolderID: &pl}, []byte("old"), e.attorney.ID)
	c.Trash(z.trashed.ID)
	z.attOnly = c.AddFile(fakecore.File{Name: "Strategy.docx", MatterID: &m, AttorneyOnly: true}, []byte("strategy"), e.attorney.ID)
	return z
}

func (e *env) zip(tok string, in map[string]any) resp {
	e.now = e.now.Add(1e9) // a second per zip, so manifests order by started_at
	return e.do(http.MethodPost, "/api/matters/"+e.matter.ID+"/zip", tok, in)
}

func (e *env) lastBulk() Bulk {
	e.t.Helper()
	var id string
	if err := e.db.QueryRow(`SELECT id FROM bulk_downloads ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	b, err := e.srv.bulk(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func names(m map[string][]byte) string {
	var out []string
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

// MOD-DOC-28, 29, 31, 32.
func TestPGDocumentsZip(t *testing.T) {
	e := newEnv(t)
	z := e.zipWorld()
	staff := e.tok(e.staff)

	// 28: whole matter as staff.
	before := e.core.DownloadCount()
	r := e.zip(staff, map[string]any{})
	e.want(r, 200)
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="2026-0001-documents-2026-11-12.zip"`) {
		t.Fatalf("zip name: %s", cd)
	}
	entries := zipEntries(t, r.Body)
	if got := names(entries); got != "Correspondence/Letter.docx|Correspondence/Reply.pdf|Pleadings/Answer.docx|Pleadings/Petition.docx|Unfiled/Notes.txt" {
		t.Fatalf("entries %s", got)
	}
	if string(entries["Pleadings/Petition.docx"]) != "petition v2" {
		t.Fatal("not the current version")
	}
	b := e.lastBulk()
	if b.Status != "complete" || b.EntryCount != 5 || len(b.Items) != 5 || b.ByteCount != int64(len(r.Body)) {
		t.Fatalf("manifest %+v", b)
	}
	for _, it := range b.Items {
		data, ok := entries[it.Path]
		if !ok || sum(data) != it.SHA256 {
			t.Fatalf("manifest row %+v does not match the zip", it)
		}
		if it.Path == "Pleadings/Petition.docx" && it.VersionNumber != 2 {
			t.Fatalf("version number %d", it.VersionNumber)
		}
	}
	if n := e.core.DownloadCount() - before; n != 5 {
		t.Fatalf("%d file.downloaded, want 5", n)
	}
	bd := e.events("documents.bulk_downloaded")
	if len(bd) != 1 || bd[0].Summary != "Downloaded 5 files as one zip" || bd[0].RecordType != "matter" {
		t.Fatalf("events %+v", bd)
	}
	// The attorney sees the attorney-only file too.
	if got := names(zipEntries(t, e.zip(e.tok(e.attorney), map[string]any{}).Body)); !strings.Contains(got, "Unfiled/Strategy.docx") {
		t.Fatalf("attorney zip %s", got)
	}

	// 29: folder scope, a selection, and a status filter each give exactly their files.
	if got := names(zipEntries(t, e.zip(staff, map[string]any{"scope": "folder", "folder_id": z.pleadings}).Body)); got != "Pleadings/Answer.docx|Pleadings/Petition.docx" {
		t.Fatalf("folder %s", got)
	}
	if got := names(zipEntries(t, e.zip(staff, map[string]any{"scope": "selection", "file_ids": []string{z.corrB.ID, z.unfiled.ID}, "layout": "flat"}).Body)); got != "Notes.txt|Reply.pdf" {
		t.Fatalf("selection %s", got)
	}
	if got := names(zipEntries(t, e.zip(staff, map[string]any{"status": "final"}).Body)); got != "Correspondence/Letter.docx|Pleadings/Petition.docx" {
		t.Fatalf("final %s", got)
	}
	// Form posts read the same fields.
	r = e.do(http.MethodPost, "/api/matters/"+e.matter.ID+"/zip", staff, url.Values{"scope": {"selection"}, "file_ids": {z.unfiled.ID}, "_return": {"/matter/download"}})
	e.want(r, 200)
	if got := names(zipEntries(t, r.Body)); got != "Unfiled/Notes.txt" {
		t.Fatalf("form selection %s", got)
	}

	// 31: an empty scope says Nothing to download and writes nothing.
	rows, events := e.count(`SELECT count(*) FROM bulk_downloads`), len(e.events("documents.bulk_downloaded"))
	r = e.zip(staff, map[string]any{"status": "executed"})
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "Nothing to download") {
		t.Fatalf("empty: %s", r.Body)
	}
	if e.count(`SELECT count(*) FROM bulk_downloads`) != rows || len(e.events("documents.bulk_downloaded")) != events {
		t.Fatal("an empty scope wrote a manifest or an event")
	}
	// A quarantined version: asked first; Leave them out drops it from zip and manifest.
	e.core.Quarantine(z.corrA.ID, 1)
	r = e.zip(staff, map[string]any{})
	e.want(r, http.StatusConflict)
	if !strings.Contains(r.str(), "Letter.docx") {
		t.Fatalf("quarantine ask: %s", r.Body)
	}
	pg := e.do(http.MethodPost, "/api/matters/"+e.matter.ID+"/zip", staff, url.Values{"_return": {"/matter/download"}})
	if pg.Status != http.StatusConflict || !strings.Contains(pg.str(), "Include after confirming") || !strings.Contains(pg.str(), "Leave them out") {
		t.Fatalf("quarantine page %d:\n%s", pg.Status, pg.Body)
	}
	r = e.zip(staff, map[string]any{"quarantine": "leave_out"})
	e.want(r, 200)
	if got := names(zipEntries(t, r.Body)); strings.Contains(got, "Letter.docx") {
		t.Fatalf("left out: %s", got)
	}
	for _, it := range e.lastBulk().Items {
		if it.FileID == z.corrA.ID {
			t.Fatal("the quarantined file is in the manifest")
		}
	}
	r = e.zip(staff, map[string]any{"quarantine": "include"})
	e.want(r, 200)
	if got := names(zipEntries(t, r.Body)); !strings.Contains(got, "Correspondence/Letter.docx") {
		t.Fatalf("included: %s", got)
	}

	// 32: a cut stream leaves the manifest interrupted and writes no summary event.
	events = len(e.events("documents.bulk_downloaded"))
	e.core.CutArchive = true
	_ = e.zip(staff, map[string]any{"quarantine": "leave_out"})
	e.core.CutArchive = false
	if b := e.lastBulk(); b.Status != "interrupted" {
		t.Fatalf("cut manifest %s", b.Status)
	}
	if len(e.events("documents.bulk_downloaded")) != events {
		t.Fatal("a cut zip wrote a summary event")
	}
}

// MOD-DOC-30 against the core: all versions with Markdown.
func TestPGDocumentsZipVersions(t *testing.T) {
	e := newEnv(t)
	m := e.matter.ID
	f := e.core.AddFile(fakecore.File{Name: "name.docx", MatterID: &m}, []byte("one"), e.attorney.ID)
	v1 := e.core.FileVersions(f.ID)[0]
	v2 := e.core.AddVersion(f.ID, []byte("two"), e.attorney.ID, "editor")
	e.core.AddRendition(f.ID, v1.ID, "markdown", []byte("# one"))
	e.core.AddRendition(f.ID, v2.ID, "markdown", []byte("# two"))
	r := e.zip(e.tok(e.staff), map[string]any{"versions": "all", "with_markdown": true, "layout": "flat"})
	e.want(r, 200)
	ents := zipEntries(t, r.Body)
	if got := names(ents); got != "name-v1.docx|name-v1.md|name-v2.docx|name-v2.md" {
		t.Fatalf("entries %s", got)
	}
	if string(ents["name-v2.md"]) != "# two" {
		t.Fatal("wrong rendition")
	}
	if pg := e.page("/matter/download?matter_id="+m, e.tok(e.staff)); pg.Status != 200 || !strings.Contains(pg.str(), "Markdown copies") {
		t.Fatalf("download page %d", pg.Status)
	}
}
