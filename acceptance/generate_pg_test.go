package acceptance

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// assemble resolves every token of a template on matter as who.
func (k *kit) assemble(who, matter string, q url.Values) documents.Assembly {
	k.t.Helper()
	var a documents.Assembly
	k.decode(k.want(k.do(who, "GET", "/api/matters/"+matter+"/assemble?"+q.Encode(), matter, nil), http.StatusOK), &a)
	return a
}

// text is a core file's current DOCX body text.
func (k *kit) text(fileID string) string {
	k.t.Helper()
	return docText(k.current(fileID), "word/document.xml")
}

// MOD-DOC-11: on the synthetic matter every vocabulary token resolves to its expected
// string, in the firm's time zone.
func TestMODDOC11Tokens(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	k.want(k.do(coretest.Admin, "POST", "/api/attorneys/"+k.Attorney.ID, "", map[string]string{"bar_number": "00001", "bar_state": "MO",
		"courtesy_title": "Ms.", "signature_line": "/s/ " + w.AttorneyName}), http.StatusOK)
	var body strings.Builder
	for _, n := range documents.VocabularyNames() {
		if !strings.Contains(n, "_cf_") {
			body.WriteString(para("{{" + n + "}}"))
		}
	}
	tm := k.template(coretest.Attorney, "Every token", docx(body.String()), nil)
	a := k.assemble(coretest.Attorney, w.Matter, url.Values{"template_id": {tm.ID}, "served_party_id": {w.RileyParty},
		"document_title": {"Motion to withdraw"}, "filing_date": {"2026-11-03"}})
	got := map[string]string{}
	for _, row := range a.Tokens {
		got[row.Name] = row.Value
	}
	chicago, _ := time.LoadLocation("America/Chicago")
	want := map[string]string{
		"matter_number": w.Number, "matter_name": "Sample v. Placeholder", "matter_case_number": "CV-26-001", "matter_court": "Circuit Court",
		"matter_county": "Testcounty", "matter_client_position": "Petitioner",
		"client_full_name": "Avery Sample", "client_first_name": "Avery", "client_last_name": "Sample",
		"client_address_line_1": "1 Test Way", "client_address_line_2": "Apt 2", "client_city": "Testville", "client_state": "MO", "client_zip": "65001",
		"opposing_party_full_name": "Riley Placeholder", "served_party_name": "Riley Placeholder", "served_party_address": "5 Other St, Testville, MO 65002",
		"firm_name": "Example Law LLC", "firm_address": "100 Main St, Testville, MO 65000", "firm_phone": "555-0100", "firm_email": "office@example.test",
		"firm_website": "https://example.test", "attorney_name": w.AttorneyName, "attorney_last_name": w.AttorneyLast, "attorney_bar_number": "00001",
		"attorney_bar_state": "MO", "attorney_signature_line": "/s/ " + w.AttorneyName, "attorney_courtesy_name": "Ms. " + w.AttorneyLast,
		"attorney_for_role": "Attorney for Petitioner", "document_title": "Motion to withdraw",
		"today_long": time.Now().In(chicago).Format("January 2, 2006"), "filing_date_long": "3rd day of November, 2026",
	}
	for n, v := range want {
		if got[n] != v {
			t.Errorf("%s = %q, want %q", n, got[n], v)
		}
	}
}

// MOD-DOC-15, 16, 18, 19 and 20: an unfilled token refuses the run; a typed value is
// never stored or logged; a run publishes one draft with the core fields set; a second
// run makes a second file; each run writes one timeline event.
func TestMODDOC15to20Generate(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	tm := k.template(coretest.Attorney, "School letter", docx(para("Dear {{client_full_name}},")+para("Re: {{school_name}}")+para("{{document_title}}")),
		map[string]string{"attorney_only": "true"})

	// 15: 422 naming the template and the token; nothing published; the run is failed.
	files, events := k.Count(`SELECT count(*) FROM core.files`), k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.generated'`)
	r := k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "School letter") || !strings.Contains(r.Body, "school_name") {
		t.Fatalf("refusal %s", r.Body)
	}
	if k.Count(`SELECT count(*) FROM core.files`) != files || k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.generated'`) != events ||
		k.moduleCount(`SELECT count(*) FROM generation_runs WHERE outcome = 'failed'`) != 1 {
		t.Fatal("a refused run published, wrote an event or left no failed run")
	}

	// 16: a blank value is refused; a typed value reaches the draft and nothing else.
	k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID, "values": map[string]string{"school_name": "   "}}), http.StatusUnprocessableEntity)
	const secret = "Testvalue-7731"
	var run documents.Run
	k.decode(k.want(k.do(coretest.Attorney, "POST", "/api/matters/"+w.Matter+"/generate", w.Matter, url.Values{"template_id": {tm.ID}, "values.school_name": {secret}}), http.StatusCreated), &run)
	if run.Outcome != "succeeded" || run.FileCount != 1 || !strings.Contains(k.text(run.FileIDs[0]), "Re: "+secret) {
		t.Fatalf("run %+v", run)
	}
	k.nowhere(secret)

	// 18: the draft's core fields, and include=documents.generated.
	var f struct {
		MatterID         *string `json:"matter_id"`
		ContactID        *string `json:"contact_id"`
		Status           *string `json:"status"`
		Origin           string  `json:"origin"`
		SharedWithClient bool    `json:"shared_with_client"`
		AttorneyOnly     bool    `json:"attorney_only"`
		Source           string  `json:"source"`
	}
	k.want(k.coreJSON(coretest.Attorney, "/api/v1/files/"+run.FileIDs[0], &f), http.StatusOK)
	if f.MatterID == nil || *f.MatterID != w.Matter || f.ContactID != nil || f.Status == nil || *f.Status != "draft" || f.Origin != "firm" ||
		f.SharedWithClient || !f.AttorneyOnly || f.Source != documents.ID {
		t.Fatalf("draft %+v", f)
	}
	inc := k.coreJSON(coretest.Attorney, "/api/v1/files?matter_id="+w.Matter+"&include=documents.generated", nil)
	if inc.Code != http.StatusOK || !strings.Contains(inc.Body, tm.ID) {
		t.Fatalf("include %d %s", inc.Code, inc.Body)
	}
	if k.moduleCount(`SELECT count(*) FROM generated_files WHERE file_id = $1 AND template_id = $2`, run.FileIDs[0], tm.ID) != 1 {
		t.Fatal("no generated_files row")
	}

	// 19: a second run makes a second file; the first file's versions don't change.
	before := k.Count(`SELECT count(*) FROM core.file_versions WHERE file_id = $1`, run.FileIDs[0])
	var run2 documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID, "values": map[string]string{"school_name": "Other"}}), http.StatusCreated), &run2)
	if run2.FileIDs[0] == run.FileIDs[0] || k.Count(`SELECT count(*) FROM core.file_versions WHERE file_id = $1`, run.FileIDs[0]) != before {
		t.Fatal("the second run touched the first file")
	}

	// 20: one documents.generated event on the matter per successful run.
	if n := k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.generated' AND matter_id = $1 AND summary = 'Generated School letter from template' AND client_visible = 0`, w.Matter); n != 2 {
		t.Fatalf("%d events", n)
	}
}

// nowhere fails the test if s is in any module table, the module's log or core.events.
func (k *kit) nowhere(s string) {
	k.t.Helper()
	rows, err := k.ModuleDB(documents.ID).Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'mod_documents' AND table_type = 'BASE TABLE'`)
	if err != nil {
		k.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		if n := k.moduleCount(`SELECT count(*) FROM `+tb+` x WHERE x::text LIKE $1`, "%"+s+"%"); n != 0 {
			k.t.Fatalf("%s holds the typed value", tb)
		}
	}
	if strings.Contains(k.logs.String(), s) {
		k.t.Fatal("the module log holds the typed value")
	}
	if n := k.Count(`SELECT count(*) FROM core.events x WHERE x::text LIKE $1`, "%"+s+"%"); n != 0 {
		k.t.Fatal("core.events holds the typed value")
	}
}

// MOD-DOC-17: with two client parties the page asks which, and the chosen client fills.
func TestMODDOC17ClientChoice(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	blake := k.id(k.Admin.Must(http.StatusCreated, "POST", "/api/v1/contacts", map[string]any{"kind": "person", "given_name": "Blake", "family_name": "Sample"}))
	k.Admin.Must(http.StatusCreated, "POST", "/api/v1/matter-parties", map[string]any{"matter_id": w.Matter, "contact_id": blake, "role": "client"})
	tm := k.template(coretest.Attorney, "Engagement", docx(para("Client: {{client_full_name}}")), nil)
	pg := k.want(k.do(coretest.Attorney, "GET", "/matter/new?matter_id="+w.Matter+"&template_id="+tm.ID, w.Matter, nil), http.StatusOK)
	if !strings.Contains(pg.Body, "Client who fills the client tokens") || !strings.Contains(pg.Body, "Blake Sample") {
		t.Fatalf("assemble page:\n%s", pg.Body)
	}
	r := k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "client_choice_required") {
		t.Fatalf("no choice refusal %s", r.Body)
	}
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID, "client_contact_id": blake}), http.StatusCreated), &run)
	if got := k.text(run.FileIDs[0]); got != "Client: Blake Sample" {
		t.Fatalf("text %q", got)
	}
}

// MOD-DOC-12, 13 and 14 on the real core: split runs, escaping and line breaks, header
// and footer tokens, and a slot filled from a component.
func TestMODDOC12to14Fill(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	split := `<w:p><w:r><w:rPr><w:b/></w:rPr><w:t>{{client_</w:t></w:r><w:r><w:t>full_</w:t></w:r><w:r><w:t>name}}</w:t></w:r></w:p>`
	hdr := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:hdr ` + wNS + `>` + para("{{matter_number}}") + `</w:hdr>`
	ftr := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:ftr ` + wNS + `>` + para("{{firm_name}}") + `</w:ftr>`
	comp := k.component(coretest.Attorney, "Caption", "caption", docx(para("IN THE {{matter_court}}")))
	tm := k.template(coretest.Attorney, "Filled", docx(split+para("{{company}}")+para("{{note}}")+para("{{slot:caption}}"),
		map[string]string{"word/header1.xml": hdr, "word/footer1.xml": ftr}), nil)
	k.want(k.do(coretest.Attorney, "POST", "/api/templates/"+tm.ID+"/slots", "", map[string]any{"slots": map[string]string{"caption": comp.ID}}), http.StatusOK)
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID,
		"values": map[string]string{"company": "R&D <Co>", "note": "Line 1\nLine 2"}}), http.StatusCreated), &run)
	b := k.current(run.FileIDs[0])
	doc := string(zipParts(b)["word/document.xml"])
	if !strings.Contains(doc, `<w:b/></w:rPr><w:t xml:space="preserve">Avery Sample</w:t>`) && !strings.Contains(doc, `<w:b/></w:rPr><w:t>Avery Sample</w:t>`) {
		t.Errorf("split run:\n%s", doc)
	}
	if !strings.Contains(doc, "R&amp;D &lt;Co&gt;") || !strings.Contains(doc, "<w:br/>") || !strings.Contains(docText(b, "word/document.xml"), "IN THE Circuit Court") {
		t.Errorf("escaping, break or slot:\n%s", doc)
	}
	if docText(b, "word/header1.xml") != w.Number || docText(b, "word/footer1.xml") != "Example Law LLC" {
		t.Errorf("header %q footer %q", docText(b, "word/header1.xml"), docText(b, "word/footer1.xml"))
	}
}

// MOD-DOC-21: with docx_and_pdf a run makes a PDF linked to its DOCX by conversion_of; a
// failed conversion leaves the run pdf_failed and Retry PDF then makes the PDF.
func TestMODDOC21PDF(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	tm := k.template(coretest.Attorney, "Notice", docx(para("{{matter_name}}")), map[string]string{"output": "docx_and_pdf"})
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID}), http.StatusCreated), &run)
	if run.Outcome != "succeeded" || len(run.PDFFileIDs) != 1 {
		t.Fatalf("run %+v", run)
	}
	if n := k.Count(`SELECT count(*) FROM core.file_links WHERE from_file_id = $1 AND to_file_id = $2 AND kind = 'conversion_of'`, run.PDFFileIDs[0], run.FileIDs[0]); n != 1 {
		t.Fatal("no conversion_of link")
	}
	// The failure half needs a converter the test can make fail.
	k.needs("CORE-29 converter stand-in")
}

// MOD-DOC-22: a template's category hint is set on its output and Doc Tender keeps it.
func TestMODDOC22CategoryHint(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	cat := k.id(k.Admin.Must(http.StatusCreated, "POST", "/api/v1/doc-tender/categories", map[string]any{"key": "petitions", "description": "Petitions", "tier": "primary", "folder_name": "Pleadings"}))
	tm := k.template(coretest.Attorney, "Petition", docx(para("{{matter_name}}")), map[string]string{"category_id": cat})
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID}), http.StatusCreated), &run)
	var f struct {
		CategoryID *string `json:"category_id"`
	}
	k.coreJSON(coretest.Attorney, "/api/v1/files/"+run.FileIDs[0], &f)
	if f.CategoryID == nil || *f.CategoryID != cat {
		t.Fatalf("category %v", f.CategoryID)
	}
	// Doc Tender keeping it (the person-set rule) needs Doc Tender to run in the test.
	k.needs("CORE-29 Doc Tender run")
}

// MOD-DOC-23: a package whose third template's component holds a picture is refused
// naming that template and "picture"; fixed, it publishes three drafts in one call.
func TestMODDOC23Package(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	one := k.template(coretest.Attorney, "Will", docx(para("Will of {{client_full_name}}")), nil)
	two := k.template(coretest.Attorney, "Power of attorney", docx(para("POA for {{client_full_name}}")), nil)
	three := k.template(coretest.Attorney, "Directive", docx(para("{{slot:signature}}")+para("Directive {{matter_number}}")), nil)
	sig := k.component(coretest.Attorney, "Signature block", "signature", docx(para("{{attorney_name}}")))
	k.want(k.do(coretest.Attorney, "POST", "/api/templates/"+three.ID+"/slots", "", map[string]any{"slots": map[string]string{"signature": sig.ID}}), http.StatusOK)
	k.version(coretest.Attorney, sig.BodyFileID, "", "signature-block.docx", docx(`<w:p><w:r><w:drawing><wp:inline/></w:drawing></w:r></w:p>`))
	var pk documents.Package
	k.decode(k.want(k.do(coretest.Attorney, "POST", "/api/packages", "", map[string]any{"name": "Estate plan", "template_ids": []string{one.ID, two.ID, three.ID}}), http.StatusCreated), &pk)
	files := k.Count(`SELECT count(*) FROM core.files`)
	r := k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"package_id": pk.ID}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "Directive") || !strings.Contains(r.Body, "picture") || k.Count(`SELECT count(*) FROM core.files`) != files {
		t.Fatalf("refusal %s", r.Body)
	}
	k.version(coretest.Attorney, sig.BodyFileID, "", "signature-block.docx", docx(para("{{attorney_name}}")))
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"package_id": pk.ID}), http.StatusCreated), &run)
	if run.FileCount != 3 || k.Count(`SELECT count(*) FROM core.files WHERE matter_id = $1 AND status = 'draft'`, w.Matter) != 3 {
		t.Fatalf("run %+v", run)
	}
	if n := k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.generated' AND summary = 'Generated 3 documents from package Estate plan'`); n != 1 {
		t.Fatalf("%d package events", n)
	}
}
