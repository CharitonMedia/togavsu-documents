package acceptance

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// MOD-DOC-33: version notes and labels, compare, and copy as new file, on a matter file
// with three versions.
func TestMODDOC33VersionTools(t *testing.T) {
	k := installed(t)
	f := k.file(coretest.Attorney, k.Open, "Motion.docx", []byte("motion v1"), nil)
	k.version(coretest.Attorney, f, k.Open, "Motion.docx", []byte("motion v2"))
	k.version(coretest.Attorney, f, k.Open, "Motion.docx", []byte("motion v3"))

	// A note and label on version 2 show on the version list and the page.
	k.want(k.do(coretest.Staff, "POST", "/api/files/"+f+"/versions/2/note", "", url.Values{"label": {"sent"}, "note": {"Sent to opposing counsel"}, "_return": {"/files/" + f}}), http.StatusSeeOther)
	var fv documents.FileVersions
	k.decode(k.want(k.do(coretest.Staff, "GET", "/api/files/"+f+"/versions", "", nil), http.StatusOK), &fv)
	if len(fv.Versions) != 3 || fv.Versions[1].Number != 2 || fv.Versions[1].Label != "sent" || fv.Versions[1].Note != "Sent to opposing counsel" {
		t.Fatalf("versions %+v", fv.Versions)
	}
	if pg := k.do(coretest.Staff, "GET", "/files/"+f, "", nil); pg.Code != http.StatusOK || !strings.Contains(pg.Body, "Sent to opposing counsel") {
		t.Fatalf("version page %d", pg.Code)
	}
	k.want(k.do(coretest.Staff, "POST", "/api/files/"+f+"/versions/2/note", "", map[string]string{"label": "lost"}), http.StatusUnprocessableEntity)
	// A viewer without access to the file gets 404 on the write: a staff member off an
	// attorney-only file.
	secret := k.file(coretest.Attorney, k.Open, "Strategy.docx", []byte("strategy"), map[string]any{"attorney_only": true})
	if r := k.do(coretest.Staff, "POST", "/api/files/"+secret+"/versions/1/note", "", map[string]string{"note": "x"}); r.Code != http.StatusNotFound {
		t.Fatalf("write without access: %d %s", r.Code, r.Body)
	}

	// A version with no Markdown copy says so.
	var c documents.Compare
	k.decode(k.want(k.do(coretest.Staff, "GET", "/api/files/"+f+"/compare?a=2&b=3", "", nil), http.StatusOK), &c)
	if c.B.Ready || c.B.Message != "Markdown copy not ready" || len(c.Lines) != 0 {
		t.Fatalf("not ready %+v", c)
	}
	if pg := k.do(coretest.Staff, "GET", "/files/"+f+"/compare?a=2&b=3", "", nil); !strings.Contains(pg.Body, "Markdown copy not ready") {
		t.Fatalf("compare page:\n%s", pg.Body)
	}

	// Copy as new file: a second file on the matter whose version 1 has the source's SHA-256.
	var cp struct {
		FileID string `json:"file_id"`
		Name   string `json:"name"`
	}
	k.decode(k.want(k.do(coretest.Staff, "POST", "/api/files/"+f+"/versions/2/copy", "", nil), http.StatusCreated), &cp)
	if cp.Name != "Motion (from v2).docx" || k.Count(`SELECT count(*) FROM core.files WHERE id = $1 AND matter_id = $2`, cp.FileID, k.Open) != 1 {
		t.Fatalf("copy %+v", cp)
	}
	if string(k.current(cp.FileID)) != "motion v2" || k.Count(`SELECT count(*) FROM core.file_versions WHERE file_id = $1`, cp.FileID) != 1 {
		t.Fatal("the copy's version 1 is not the source version")
	}
	if k.moduleCount(`SELECT count(*) FROM version_copies WHERE file_id = $1 AND source_file_id = $2 AND source_version_number = 2`, cp.FileID, f) != 1 {
		t.Fatal("no version_copies row")
	}

	// Compare of two versions with Markdown copies needs Markdown renditions in the test.
	k.needs("CORE-29 rendition stand-in")
	k.decode(k.want(k.do(coretest.Staff, "GET", "/api/files/"+f+"/compare?a=1&b=2", "", nil), http.StatusOK), &c)
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
}

// MOD-DOC-34: attorney profiles are the admin's; a saved bar number fills
// attorney_bar_number and comes back on the user with include=documents.attorney.
func TestMODDOC34Attorneys(t *testing.T) {
	k := installed(t)
	for _, who := range []string{coretest.Staff, coretest.Attorney} {
		if r := k.do(who, "GET", "/attorneys", "", nil); r.Code != http.StatusNotFound {
			t.Fatalf("%s: attorneys page %d", who, r.Code)
		}
		if r := k.do(who, "POST", "/api/attorneys/"+k.Attorney.ID, "", map[string]string{"bar_number": "x"}); r.Code != http.StatusNotFound {
			t.Fatalf("%s: attorneys write %d", who, r.Code)
		}
	}
	k.needs("CORE-19")
	pg := k.want(k.do(coretest.Admin, "GET", "/attorneys", "", nil), http.StatusOK)
	if !strings.Contains(pg.Body, k.Attorney.Name) || strings.Contains(pg.Body, k.Staff.Name) {
		t.Fatalf("attorneys page:\n%s", pg.Body)
	}
	k.want(k.do(coretest.Admin, "POST", "/api/attorneys/"+k.Attorney.ID, "", map[string]string{"bar_state": "Missouri"}), http.StatusUnprocessableEntity)
	k.want(k.do(coretest.Admin, "POST", "/api/attorneys/"+k.Staff.ID, "", map[string]string{"bar_number": "1"}), http.StatusNotFound)
	k.want(k.do(coretest.Admin, "POST", "/api/attorneys/"+k.Attorney.ID, "", map[string]string{"bar_number": "00001", "bar_state": "MO"}), http.StatusOK)
	inc := k.coreJSON(coretest.Admin, "/api/v1/users/"+k.Attorney.ID+"?include=documents.attorney", nil)
	if inc.Code != http.StatusOK || !strings.Contains(inc.Body, `"bar_number":"00001"`) {
		t.Fatalf("include %d %s", inc.Code, inc.Body)
	}
	k.needsGeneration()
	tm := k.template(coretest.Attorney, "Signature", docx(para("{{attorney_bar_number}}")), nil)
	a := k.assemble(coretest.Attorney, k.Open, url.Values{"template_id": {tm.ID}})
	for _, row := range a.Tokens {
		if row.Name == "attorney_bar_number" && row.Value != "00001" {
			t.Fatalf("attorney_bar_number %q", row.Value)
		}
	}
}
