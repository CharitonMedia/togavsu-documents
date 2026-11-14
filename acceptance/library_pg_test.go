package acceptance

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// MOD-DOC-03: uploading a DOCX makes a library file (no matter, no contact, library
// true) and a template row; Doc Tender reports not_processed_library and opens no
// exception.
func TestMODDOC03LibraryFile(t *testing.T) {
	k := installed(t)
	k.needs("CORE-17")
	tm := k.template(coretest.Attorney, "Motion to withdraw", docx(para("{{document_title}} for {{client_full_name}}")),
		map[string]string{"topics": "withdraw, counsel", "practice_area_ids": k.PracticeArea})
	var f struct {
		Library   bool    `json:"library"`
		MatterID  *string `json:"matter_id"`
		ContactID *string `json:"contact_id"`
		Name      string  `json:"name"`
	}
	k.want(k.coreJSON(coretest.Attorney, "/api/v1/files/"+tm.BodyFileID, &f), http.StatusOK)
	if !f.Library || f.MatterID != nil || f.ContactID != nil || f.Name != "motion-to-withdraw.docx" {
		t.Fatalf("body file %+v", f)
	}
	if n := k.moduleCount(`SELECT count(*) FROM templates WHERE id = $1 AND body_file_id = $2`, tm.ID, tm.BodyFileID); n != 1 {
		t.Fatal("no template row")
	}
	st := k.coreJSON(coretest.Admin, "/api/v1/files/"+tm.BodyFileID+"/doc-tender-status", nil)
	if st.Code != http.StatusOK || !strings.Contains(st.Body, "not_processed_library") {
		t.Fatalf("doc tender status %d %s", st.Code, st.Body)
	}
	if ex := k.coreJSON(coretest.Admin, "/api/v1/doc-tender/exceptions", nil); strings.Contains(ex.Body, tm.BodyFileID) {
		t.Fatal("Doc Tender opened an exception on a library file")
	}
}

// MOD-DOC-04: a PDF, or a zip without word/document.xml, is refused with 422 naming the
// reason; nothing is staged or published and no row is written.
func TestMODDOC04NotDOCX(t *testing.T) {
	k := installed(t)
	files, uploads := k.Count(`SELECT count(*) FROM core.files`), k.Count(`SELECT count(*) FROM core.file_uploads`)
	r := k.want(k.do(coretest.Attorney, "POST", "/api/templates", "", &form{fields: map[string]string{"mode": "upload", "name": "PDF"}, fileName: "motion.pdf", data: []byte("%PDF-1.7 synthetic")}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "only DOCX files") {
		t.Fatalf("pdf reason: %s", r.Body)
	}
	noDoc := zipOnly(map[string]string{"[Content_Types].xml": "<Types/>"})
	r = k.want(k.do(coretest.Attorney, "POST", "/api/templates", "", &form{fields: map[string]string{"mode": "upload", "name": "Empty"}, fileName: "empty.docx", data: noDoc}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "word/document.xml") {
		t.Fatalf("zip reason: %s", r.Body)
	}
	if k.Count(`SELECT count(*) FROM core.files`) != files || k.Count(`SELECT count(*) FROM core.file_uploads`) != uploads || k.moduleCount(`SELECT count(*) FROM templates`) != 0 {
		t.Fatal("a refused upload staged or published a file, or wrote a row")
	}
}

// MOD-DOC-05: a blank template's file holds {{document_title}} and the core editor opens it.
func TestMODDOC05Blank(t *testing.T) {
	k := installed(t)
	k.needs("CORE-17")
	var blank documents.Template
	k.decode(k.want(k.do(coretest.Attorney, "POST", "/api/templates", "", url.Values{"mode": {"blank"}, "name": {"Blank letter"}}), http.StatusCreated), &blank)
	if got := docText(k.current(blank.BodyFileID), "word/document.xml"); got != "{{document_title}}" {
		t.Fatalf("blank body %q", got)
	}
	k.As(coretest.Attorney).Must(http.StatusOK, "GET", "/files/"+blank.BodyFileID+"/edit", nil)
	// Edit body opens the editor through the frame bridge.
	k.needs("CORE-09")
}

// MOD-DOC-06: an editor save on the body shows as the newest version and the next run
// records that version number.
func TestMODDOC06BodyVersion(t *testing.T) {
	k := installed(t)
	k.needs("CORE-17")
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{document_title}}")), nil)
	k.version(coretest.Attorney, tm.BodyFileID, "", "letter.docx", docx(para("{{document_title}} v2")))
	var tv documents.TemplateView
	k.decode(k.want(k.do(coretest.Attorney, "GET", "/api/templates/"+tm.ID, "", nil), http.StatusOK), &tv)
	if len(tv.Versions) != 2 || tv.Versions[0].Number != 2 {
		t.Fatalf("versions %+v", tv.Versions)
	}
	if pg := k.do(coretest.Attorney, "GET", "/templates/"+tm.ID, "", nil); !strings.Contains(pg.Body, "v2") {
		t.Fatal("the template page lacks v2")
	}
	k.needs("CORE-18", "CORE-19")
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID, "values": map[string]string{"document_title": "Letter"}}), http.StatusCreated), &run)
	if n := k.moduleCount(`SELECT template_version_number FROM generated_files WHERE file_id = $1`, run.FileIDs[0]); n != 2 {
		t.Fatalf("template_version_number %d", n)
	}
}

// MOD-DOC-07: editing name, topics or practice areas adds no file version.
func TestMODDOC07Metadata(t *testing.T) {
	k := installed(t)
	k.needs("CORE-17")
	tm := k.template(coretest.Attorney, "Letter", docx(para("x")), nil)
	var edited documents.Template
	k.decode(k.want(k.do(coretest.Attorney, "PATCH", "/api/templates/"+tm.ID, "", map[string]any{"name": "Engagement letter", "topics": []string{"intake"},
		"practice_area_ids": []string{k.PracticeArea}}), http.StatusOK), &edited)
	if edited.Name != "Engagement letter" || len(edited.PracticeAreaIDs) != 1 {
		t.Fatalf("edit %+v", edited)
	}
	if n := k.Count(`SELECT count(*) FROM core.file_versions WHERE file_id = $1`, tm.BodyFileID); n != 1 {
		t.Fatalf("a metadata edit added a version: %d", n)
	}
}

// MOD-DOC-08: generating from an archived template or package returns 409 and writes
// nothing.
func TestMODDOC08Archived(t *testing.T) {
	k := installed(t)
	k.needs("CORE-17")
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	var pk documents.Package
	k.decode(k.want(k.do(coretest.Attorney, "POST", "/api/packages", "", map[string]any{"name": "Set", "template_ids": []string{tm.ID}}), http.StatusCreated), &pk)
	k.want(k.do(coretest.Attorney, "POST", "/api/templates/"+tm.ID+"/archive", "", nil), http.StatusOK)
	files := k.Count(`SELECT count(*) FROM core.files`)
	k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusConflict)
	k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"package_id": pk.ID}), http.StatusConflict)
	k.want(k.do(coretest.Attorney, "POST", "/api/templates/"+tm.ID+"/unarchive", "", nil), http.StatusOK)
	k.want(k.do(coretest.Attorney, "POST", "/api/packages/"+pk.ID+"/archive", "", nil), http.StatusOK)
	k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"package_id": pk.ID}), http.StatusConflict)
	if k.moduleCount(`SELECT count(*) FROM generation_runs`) != 0 || k.Count(`SELECT count(*) FROM core.files`) != files {
		t.Fatal("a refused archived run wrote something")
	}
}

// MOD-DOC-09: Check template reports one known token, one manual-only token and one
// slot; saving with the slot unmapped returns 422.
func TestMODDOC09Check(t *testing.T) {
	k := installed(t)
	k.needs("CORE-17")
	tm := k.template(coretest.Attorney, "Checked", docx(para("{{client_full_name}}")+para("{{school_name}}")+para("{{slot:caption}}")), nil)
	var rep documents.CheckReport
	k.decode(k.want(k.do(coretest.Attorney, "GET", "/api/templates/"+tm.ID+"/check", "", nil), http.StatusOK), &rep)
	if len(rep.Known) != 1 || rep.Known[0].Name != "client_full_name" || len(rep.Manual) != 1 || rep.Manual[0] != "school_name" ||
		len(rep.Slots) != 1 || rep.Slots[0].Slot != "caption" || rep.Slots[0].Mapped {
		t.Fatalf("check %+v", rep)
	}
	r := k.want(k.do(coretest.Attorney, "POST", "/api/templates/"+tm.ID+"/slots", "", map[string]any{"slots": map[string]string{}}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "caption") {
		t.Fatalf("unmapped refusal %s", r.Body)
	}
}

// MOD-DOC-10: a component holding a hyperlink returns 422 naming "hyperlink"; one
// holding a numbered list names "numbering". Nothing is written.
func TestMODDOC10ComponentParts(t *testing.T) {
	k := installed(t)
	uploads := k.Count(`SELECT count(*) FROM core.file_uploads`)
	for part, body := range map[string]string{
		"hyperlink": `<w:p><w:hyperlink r:id="rId9"><w:r><w:t>site</w:t></w:r></w:hyperlink></w:p>`,
		"numbering": `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`,
	} {
		r := k.want(k.do(coretest.Staff, "POST", "/api/components", "", &form{fields: map[string]string{"mode": "upload", "name": "Bad " + part, "category": "caption"},
			fileName: "bad.docx", data: docx(body)}), http.StatusUnprocessableEntity)
		if !strings.Contains(r.Body, `"part":"`+part+`"`) {
			t.Fatalf("%s: %s", part, r.Body)
		}
	}
	if k.moduleCount(`SELECT count(*) FROM components`) != 0 || k.Count(`SELECT count(*) FROM core.file_uploads`) != uploads {
		t.Fatal("a refused component wrote a row or staged an upload")
	}
}

// MOD-DOC-24: a package over max_package_templates can't be saved.
func TestMODDOC24PackageLimit(t *testing.T) {
	k := installed(t)
	k.Admin.Must(http.StatusOK, "PUT", "/api/v1/modules/"+documents.ID+"/settings", map[string]string{"max_package_templates": strconv.Itoa(2)})
	k.needs("CORE-17")
	var ids []string
	for _, n := range []string{"One", "Two", "Three"} {
		ids = append(ids, k.template(coretest.Attorney, n, docx(para(n)), nil).ID)
	}
	k.want(k.do(coretest.Attorney, "POST", "/api/packages", "", map[string]any{"name": "Too big", "template_ids": ids}), http.StatusUnprocessableEntity)
	k.want(k.do(coretest.Attorney, "POST", "/api/packages", "", map[string]any{"name": "Fits", "template_ids": ids[:2]}), http.StatusCreated)
	if n := k.moduleCount(`SELECT count(*) FROM packages`); n != 1 {
		t.Fatalf("%d packages", n)
	}
}
