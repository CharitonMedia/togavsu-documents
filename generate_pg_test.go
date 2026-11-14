package documents

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/CharitonMedia/togavsu-documents/internal/fakecore"
)

// MOD-DOC-11: on the synthetic matter every vocabulary token resolves to its expected
// string at the fixed test clock, in the firm's time zone.
func TestPGDocumentsTokens(t *testing.T) {
	e := newEnv(t)
	admin := e.tok(e.admin)
	e.want(e.do(http.MethodPost, "/api/attorneys/"+e.attorney.ID, admin, map[string]string{"bar_number": "00001", "bar_state": "tx",
		"courtesy_title": "Ms.", "signature_line": "/s/ Jordan Q. Testlawyer"}), 200)
	res, err := e.srv.resolve(context.Background(), e.tok(e.attorney), resolveInput{MatterID: e.matter.ID, ServedPartyID: e.oppParty.ID,
		TemplateTitles: []string{"Motion to withdraw"}, FilingDate: "2026-11-03"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"matter_number": "2026-0001", "matter_name": "Sample v. Example", "matter_case_number": "CV-26-001", "matter_court": "District Court",
		"matter_county": "Testcounty", "matter_client_position": "Petitioner",
		"client_full_name": "Avery Sample", "client_first_name": "Avery", "client_last_name": "Sample", "client_also_known_as": "A. Sample",
		"client_address_line_1": "1 Test Way", "client_address_line_2": "Apt 2", "client_city": "Testville", "client_state": "TX", "client_zip": "75001",
		"opposing_party_full_name": "Casey Example", "opposing_party_role": "Respondent",
		"served_party_name": "Casey Example", "served_party_address": "5 Other St, Testville, TX 75002",
		"firm_name": "Sample Law PLLC", "firm_address": "100 Main St, Testville, TX 75000", "firm_phone": "555-0100", "firm_email": "office@example.test",
		"firm_website": "https://example.test",
		"attorney_name": "Jordan Q. Testlawyer", "attorney_last_name": "Testlawyer", "attorney_bar_number": "00001", "attorney_bar_state": "TX",
		"attorney_signature_line": "/s/ Jordan Q. Testlawyer", "attorney_courtesy_name": "Ms. Testlawyer", "attorney_for_role": "Attorney for Petitioner",
		"document_title": "Motion to withdraw", "today_long": "November 12, 2026", "filing_date_long": "3rd day of November, 2026",
		"matter_cf_judge": "Hon. Test", "matter_cf_hearing": "December 1, 2026", "client_cf_employer": "Testco",
	}
	for _, n := range VocabularyNames() {
		if _, ok := want[n]; !ok {
			t.Errorf("no expected value for %s", n)
		}
	}
	for n, w := range want {
		if got := res.Values[n].Value; got != w {
			t.Errorf("%s = %q, want %q", n, got, w)
		}
	}
	if res.CFKnown["matter_cf_old"] {
		t.Error("a retired custom field counts as known")
	}
	// A clock just before midnight UTC is still the 12th in Chicago... and just after
	// midnight UTC too.
	e.now = fixedNow.Add(10 * 3600e9) // 01:00 UTC on the 13th, 7pm the 12th in Chicago
	res, _ = e.srv.resolve(context.Background(), e.tok(e.attorney), resolveInput{MatterID: e.matter.ID})
	if res.Values["today_long"].Value != "November 12, 2026" {
		t.Errorf("today_long in the firm zone: %q", res.Values["today_long"].Value)
	}
	// With no courtesy title typed, the courtesy name is unresolved (a manual fill).
	e.want(e.do(http.MethodPost, "/api/attorneys/"+e.attorney.ID, admin, map[string]string{"bar_number": "00001"}), 200)
	res, _ = e.srv.resolve(context.Background(), e.tok(e.attorney), resolveInput{MatterID: e.matter.ID})
	if res.Values["attorney_courtesy_name"].Value != "" {
		t.Errorf("courtesy name guessed: %q", res.Values["attorney_courtesy_name"].Value)
	}
}

func (e *env) fileText(fileID string) string {
	return docText(e.t, e.currentBytes(fileID), documentPart)
}

// MOD-DOC-15, 16, 18 to 21.
func TestPGDocumentsGenerate(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	tm := e.uploadTemplate(tok, "School letter", makeDOCX(t, para("Dear {{client_full_name}},")+para("Re: {{school_name}}")+para("{{document_title}}"), nil),
		map[string]string{"attorney_only": "true"})

	// 15: unfilled manual token: 422 naming the template and token; nothing published.
	files, events := len(e.core.Files), len(e.core.EventsCopy())
	r := e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "School letter") || !strings.Contains(r.str(), "school_name") {
		t.Fatalf("refusal: %s", r.Body)
	}
	if len(e.core.Files) != files || len(e.core.EventsCopy()) != events || e.core.BulkCalls != 0 {
		t.Fatal("a refused run published or wrote an event")
	}
	if n := e.count(`SELECT count(*) FROM generation_runs WHERE outcome = 'failed'`); n != 1 {
		t.Fatalf("%d failed runs", n)
	}

	// 16: a blank typed value is refused; a typed value is never stored or logged.
	r = e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID, "values": map[string]string{"school_name": "   "}})
	e.want(r, http.StatusUnprocessableEntity)
	const secret = "Testvalue-7731"
	r = e.do(http.MethodPost, "/api/matters/"+e.matter.ID+"/generate", tok, url.Values{"template_id": {tm.ID}, "values.school_name": {secret}})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	if run.Outcome != "succeeded" || run.FileCount != 1 || len(run.ManualTokens) != 1 || run.ManualTokens[0] != "school_name" {
		t.Fatalf("run: %+v", run)
	}
	if !strings.Contains(e.fileText(run.FileIDs[0]), "Re: "+secret) {
		t.Fatal("the typed value is not in the draft")
	}
	e.assertNowhere(secret)

	// 18: the draft's core fields and include=documents.generated.
	f, _ := e.core.File(run.FileIDs[0])
	if f.MatterID == nil || *f.MatterID != e.matter.ID || f.ContactID != nil || f.Status == nil || *f.Status != "draft" || f.Origin != "firm" ||
		f.SharedWithClient || !f.AttorneyOnly || f.Source != "documents" || f.Name != "school-letter-2026-11-12.docx" {
		t.Fatalf("draft: %+v", f)
	}
	st, body := e.core.Call(http.MethodGet, "/api/v1/files?matter_id="+e.matter.ID+"&include=documents.generated", tok, nil)
	if st != 200 || !strings.Contains(string(body), `"template_id":"`+tm.ID+`"`) {
		t.Fatalf("include: %d %s", st, body)
	}
	if n := e.count(`SELECT count(*) FROM generated_files WHERE file_id = $1 AND template_id = $2`, f.ID, tm.ID); n != 1 {
		t.Fatal("no generated_files row")
	}
	if !strings.Contains(e.fileText(f.ID), "Dear Avery Sample,") || !strings.Contains(e.fileText(f.ID), "School letter") {
		t.Fatalf("draft text: %q", e.fileText(f.ID))
	}

	// 19: a second run makes a second file and leaves the first alone.
	firstVersions := e.core.FileVersions(f.ID)
	r = e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID, "values": map[string]string{"school_name": "Other"}})
	e.want(r, http.StatusCreated)
	var run2 Run
	r.json(t, &run2)
	if run2.FileIDs[0] == f.ID {
		t.Fatal("the second run reused the file")
	}
	f2, _ := e.core.File(run2.FileIDs[0])
	if f2.Name != "school-letter-2026-11-12-2.docx" {
		t.Fatalf("second name %q", f2.Name)
	}
	if after := e.core.FileVersions(f.ID); len(after) != len(firstVersions) || after[0].SHA256 != firstVersions[0].SHA256 {
		t.Fatal("the first file's versions changed")
	}

	// 20: exactly one documents.generated event per successful run.
	gen := e.events("documents.generated")
	if len(gen) != 2 || gen[0].Summary != "Generated School letter from template" || gen[0].MatterID != e.matter.ID || gen[0].RecordType != "matter" || gen[0].ClientVisible {
		t.Fatalf("events: %+v", gen)
	}

	// The drafts list and the recent panel show the runs.
	if pg := e.page("/matter/recent?matter_id="+e.matter.ID, tok); pg.Status != 200 || !strings.Contains(pg.str(), "school_name") {
		t.Fatalf("recent panel %d %s", pg.Status, pg.Body)
	}
}

// assertNowhere scans every module table, the module's log and the core's events for s.
func (e *env) assertNowhere(s string) {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'mod_documents' AND table_type = 'BASE TABLE'`)
	if err != nil {
		e.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 10 {
		e.t.Fatalf("only %d module tables", len(tables))
	}
	for _, tb := range tables {
		if n := e.count(`SELECT count(*) FROM mod_documents.`+tb+` x WHERE x::text LIKE $1`, "%"+s+"%"); n != 0 {
			e.t.Fatalf("%s holds the typed value", tb)
		}
	}
	if strings.Contains(e.logs.String(), s) {
		e.t.Fatal("the module log holds the typed value")
	}
	var n int
	if err := e.coreDB.QueryRow(`SELECT count(*) FROM core.events x WHERE x::text LIKE $1`, "%"+s+"%").Scan(&n); err != nil || n != 0 {
		e.t.Fatalf("core.events holds the typed value (%d, %v)", n, err)
	}
	for _, ev := range e.core.EventsCopy() {
		if strings.Contains(ev.Summary, s) {
			e.t.Fatal("an event summary holds the typed value")
		}
	}
}

// MOD-DOC-21: PDF beside the DOCX; a converter failure leaves the DOCX and marks the
// run pdf_failed; Retry PDF makes it.
func TestPGDocumentsPDF(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	tm := e.uploadTemplate(tok, "Notice", makeDOCX(t, para("{{matter_name}}"), nil), map[string]string{"output": "docx_and_pdf"})
	r := e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	if run.Outcome != "succeeded" || len(run.PDFFileIDs) != 1 {
		t.Fatalf("run: %+v", run)
	}
	e.assertLinked(run.PDFFileIDs[0], run.FileIDs[0])

	e.core.FailConvert = true
	r = e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	r.json(t, &run)
	if run.Outcome != "pdf_failed" || len(run.PDFFileIDs) != 0 {
		t.Fatalf("failed run: %+v", run)
	}
	if f, ok := e.core.File(run.FileIDs[0]); !ok || f.TrashedAt != nil {
		t.Fatal("the DOCX did not stay")
	}
	pg := e.page("/matter?matter_id="+e.matter.ID, tok)
	if !strings.Contains(pg.str(), "Retry PDF") {
		t.Fatalf("no Retry PDF button:\n%s", pg.Body)
	}
	e.core.FailConvert = false
	r = e.do(http.MethodPost, "/api/runs/"+run.ID+"/retry-pdf", tok, nil)
	e.want(r, 200)
	r.json(t, &run)
	if run.Outcome != "succeeded" || len(run.PDFFileIDs) != 1 {
		t.Fatalf("after retry: %+v", run)
	}
	e.assertLinked(run.PDFFileIDs[0], run.FileIDs[0])
}

func (e *env) assertLinked(pdf, docx string) {
	e.t.Helper()
	for _, l := range e.core.Links {
		if l.From == pdf && l.To == docx && l.Kind == "conversion_of" {
			return
		}
	}
	e.t.Fatalf("no conversion_of link from %s to %s", pdf, docx)
}

// MOD-DOC-22: the template's category hint is set on its output, and Doc Tender keeps
// it (the person-set rule), in the fake core.
func TestPGDocumentsCategoryHint(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	cat, other := e.core.ID(), e.core.ID()
	e.core.Categories[cat] = "pleadings"
	e.core.Categories[other] = "correspondence"
	tm := e.uploadTemplate(tok, "Petition", makeDOCX(t, para("{{matter_name}}"), nil), map[string]string{"category_id": cat})
	r := e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	f, _ := e.core.File(run.FileIDs[0])
	if f.CategoryID == nil || *f.CategoryID != cat {
		t.Fatalf("category: %v", f.CategoryID)
	}
	e.core.DocTenderProcess(f.ID, other)
	f, _ = e.core.File(f.ID)
	if *f.CategoryID != cat {
		t.Fatal("Doc Tender replaced the hinted category")
	}
}

// MOD-DOC-17: with two client parties the page asks which; the chosen client fills.
func TestPGDocumentsClientChoice(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	blake := e.core.AddContact(fakecore.Contact{FullName: "Blake Sample", GivenName: "Blake", FamilyName: "Sample"})
	e.core.AddParty(fakecore.Party{MatterID: e.matter.ID, ContactID: blake.ID, Role: "client"})
	tm := e.uploadTemplate(tok, "Engagement", makeDOCX(t, para("Client: {{client_full_name}}"), nil), nil)
	pg := e.page("/matter/new?matter_id="+e.matter.ID+"&template_id="+tm.ID, tok)
	e.want(pg, 200)
	if !strings.Contains(pg.str(), "Client who fills the client tokens") || !strings.Contains(pg.str(), "Blake Sample") || !strings.Contains(pg.str(), "Avery Sample (primary)") {
		t.Fatalf("assemble page:\n%s", pg.Body)
	}
	r := e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID})
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "client_choice_required") {
		t.Fatalf("no choice refusal: %s", r.Body)
	}
	r = e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID, "client_contact_id": blake.ID})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	if got := e.fileText(run.FileIDs[0]); got != "Client: Blake Sample" {
		t.Fatalf("text %q", got)
	}
	var principal string
	_ = e.db.QueryRow(`SELECT principal_contact_id FROM generation_runs WHERE id = $1`, run.ID).Scan(&principal)
	if principal != blake.ID {
		t.Fatalf("principal %q", principal)
	}
}

// MOD-DOC-23: a package whose third template maps a slot to a component holding a
// picture is refused naming that template and "picture", publishing nothing; fixed, the
// run publishes three drafts in one bulkFile call.
func TestPGDocumentsPackages(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	one := e.uploadTemplate(tok, "Will", makeDOCX(t, para("Will of {{client_full_name}}"), nil), nil)
	two := e.uploadTemplate(tok, "Power of attorney", makeDOCX(t, para("POA for {{client_full_name}}"), nil), nil)
	three := e.uploadTemplate(tok, "Directive", makeDOCX(t, para("{{slot:signature}}")+para("Directive {{matter_number}}"), nil), nil)
	sig := e.uploadComponent(tok, "Signature block", "signature", makeDOCX(t, para("{{attorney_name}}"), nil))
	e.want(e.do(http.MethodPost, "/api/templates/"+three.ID+"/slots", tok, map[string]any{"slots": map[string]string{"signature": sig.ID}}), 200)
	// An editor save puts a picture in the component after it was saved.
	e.core.AddVersion(sig.BodyFileID, makeDOCX(t, `<w:p><w:r><w:drawing><wp:inline/></w:drawing></w:r></w:p>`, nil), e.attorney.ID, "editor")
	r := e.do(http.MethodPost, "/api/packages", tok, map[string]any{"name": "Estate plan", "template_ids": []string{one.ID, two.ID, three.ID}})
	e.want(r, http.StatusCreated)
	var pk Package
	r.json(t, &pk)

	files := len(e.core.Files)
	r = e.generate(tok, e.matter.ID, map[string]any{"package_id": pk.ID})
	e.want(r, http.StatusUnprocessableEntity)
	if !strings.Contains(r.str(), "Directive") || !strings.Contains(r.str(), "picture") || strings.Contains(r.str(), "Will:") {
		t.Fatalf("refusal: %s", r.Body)
	}
	if len(e.core.Files) != files || e.core.BulkCalls != 0 || len(e.events("documents.generated")) != 0 {
		t.Fatal("a refused package published something")
	}

	e.core.AddVersion(sig.BodyFileID, makeDOCX(t, para("{{attorney_name}}"), nil), e.attorney.ID, "editor")
	r = e.generate(tok, e.matter.ID, map[string]any{"package_id": pk.ID})
	e.want(r, http.StatusCreated)
	var run Run
	r.json(t, &run)
	if run.FileCount != 3 || e.core.BulkCalls != 1 {
		t.Fatalf("run %+v, %d bulk calls", run, e.core.BulkCalls)
	}
	for _, id := range run.FileIDs {
		if f, _ := e.core.File(id); f.Status == nil || *f.Status != "draft" {
			t.Fatalf("%s is not a draft", f.Name)
		}
	}
	if got := e.fileText(run.FileIDs[2]); got != "Jordan Q. Testlawyer\nDirective 2026-0001" {
		t.Fatalf("directive text %q", got)
	}
	var comps string
	_ = e.db.QueryRow(`SELECT component_versions::text FROM generated_files WHERE file_id = $1`, run.FileIDs[2]).Scan(&comps)
	if comps != `{"signature": 3}` {
		t.Fatalf("component versions %s", comps)
	}
	gen := e.events("documents.generated")
	if len(gen) != 1 || gen[0].Summary != "Generated 3 documents from package Estate plan" {
		t.Fatalf("events %+v", gen)
	}
}

// A closed matter still generates, with the Doc Tender note.
func TestPGDocumentsClosedMatter(t *testing.T) {
	e := newEnv(t)
	tok := e.tok(e.attorney)
	e.matter.Status = "closed"
	tm := e.uploadTemplate(tok, "Closing letter", makeDOCX(t, para("{{matter_name}}"), nil), nil)
	pg := e.page("/matter/new?matter_id="+e.matter.ID+"&template_id="+tm.ID, tok)
	if !strings.Contains(pg.str(), "Doc Tender holds new files on closed matters") {
		t.Fatalf("no closed note:\n%s", pg.Body)
	}
	e.want(e.generate(tok, e.matter.ID, map[string]any{"template_id": tm.ID}), http.StatusCreated)
}
