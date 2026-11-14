package acceptance

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// MOD-DOC-37: quick-create opens a matter picker and then the assemble page; the six
// help pages are in the guide; search finds active templates for admin, attorney and
// staff.
func TestMODDOC37SearchQuickHelp(t *testing.T) {
	k := installed(t)
	var m struct {
		Number string `json:"number"`
		Name   string `json:"name"`
	}
	k.coreJSON(coretest.Admin, "/api/v1/matters/"+k.Open, &m)
	qc := k.want(k.do(coretest.Attorney, "GET", "/quick/new", "", nil), http.StatusOK)
	if !strings.Contains(qc.Body, m.Name) || !strings.Contains(qc.Body, "/m/documents/matter/new?") {
		t.Fatalf("quick-create:\n%s", qc.Body)
	}
	k.want(k.do(coretest.Attorney, "GET", "/matter/new?matter_id="+k.Open, k.Open, nil), http.StatusOK)

	var help struct {
		Data []struct {
			Title string `json:"title"`
		} `json:"data"`
	}
	r := k.coreJSON(coretest.Staff, "/api/v1/help/modules/"+documents.ID+"/pages", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("help pages %d %s", r.Code, r.Body)
	}
	_ = json.Unmarshal([]byte(r.Body), &help)
	if len(help.Data) != 6 {
		t.Fatalf("%d help pages: %s", len(help.Data), r.Body)
	}
	if g := k.Staff.Do("GET", "/help/guide", nil); g.Code != http.StatusOK || !strings.Contains(g.Body, "Documents and templates") {
		t.Fatalf("guide %d", g.Code)
	}

	k.needs("CORE-17", "CORE-20")
	k.template(coretest.Attorney, "Motion to withdraw", docx(para("{{matter_number}}")), map[string]string{"description": "Leave of court"})
	k.template(coretest.Attorney, "Engagement letter", docx(para("{{matter_number}}")), map[string]string{"topics": "intake"})
	arch := k.template(coretest.Attorney, "Withdrawn old", docx(para("x")), nil)
	k.want(k.do(coretest.Attorney, "POST", "/api/templates/"+arch.ID+"/archive", "", nil), http.StatusOK)
	for _, who := range []string{coretest.Admin, coretest.Attorney, coretest.Staff} {
		pg := k.want(k.do(who, "GET", "/search?q=withdraw", "", nil), http.StatusOK)
		if !strings.Contains(pg.Body, "Motion to withdraw") || strings.Contains(pg.Body, "Withdrawn old") || strings.Contains(pg.Body, "Engagement letter") {
			t.Fatalf("search for %s:\n%s", who, pg.Body)
		}
	}
}

var pathParam = regexp.MustCompile(`\{[a-z_]+\}`)

// MOD-DOC-38: accountant and client viewers get 404 on every module page and endpoint,
// and the widget isn't offered to them.
func TestMODDOC38NoAccess(t *testing.T) {
	k := installed(t)
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	k.decode(k.want(k.doTok(k.Admin, "", "GET", "/api/openapi.json", nil), http.StatusOK), &doc)
	pages := append([]string{"/templates", "/templates/new", "/drafts", "/widget/drafts", "/quick/new", "/search", "/attorneys", "/release"}, matterPages...)
	const zero = "00000000-0000-7000-8000-000000000000"
	for _, who := range []string{coretest.Accountant, coretest.ClientRole} {
		u := k.As(who)
		// The module's page inside each tab the core frames for them.
		for _, tab := range []string{"templates", "drafts"} {
			if r := k.tabOrNone(who, tab, ""); r != 0 && r != http.StatusNotFound {
				t.Errorf("%s: %s tab's page %d", who, tab, r)
			}
		}
		if r := k.tabOrNone(who, "matter", k.Open); r != 0 && r != http.StatusNotFound {
			t.Errorf("%s: matter tab's page %d", who, r)
		}
		if r := u.Do("GET", "/api/v1/widgets", nil); strings.Contains(r.Body, documents.ID+".drafts") || strings.Contains(r.Body, `"module_id":"documents"`) {
			t.Errorf("%s is offered the widget: %s", who, r.Body)
		}
		// With a context token where the core issues one, every page and endpoint is 404.
		tok := k.ctx(who, "")
		if tok == "" {
			t.Logf("the core issues %s no context token", who)
			continue
		}
		checked := 0
		for _, p := range pages {
			if r := k.doTok(u, tok, "GET", p+"?matter_id="+k.Open, nil); r.Code != http.StatusNotFound {
				t.Errorf("%s: GET %s %d", who, p, r.Code)
			}
		}
		for path, ops := range doc.Paths {
			p := pathParam.ReplaceAllString(strings.TrimPrefix(path, "/m/"+documents.ID), zero)
			if p == "/api/openapi.json" || p == "/api/" {
				continue
			}
			for method := range ops {
				var body any
				if method != "get" {
					body = map[string]any{}
				}
				if r := k.doTok(u, tok, strings.ToUpper(method), p, body); r.Code != http.StatusNotFound {
					t.Errorf("%s: %s %s %d", who, strings.ToUpper(method), p, r.Code)
				}
				checked++
			}
		}
		t.Logf("%s: %d pages and %d endpoints answer 404", who, len(pages), checked)
	}
}

// MOD-DOC-39: uninstall keeping data locks mod_documents and a reinstall reads it;
// deleting data drops it after a backup and typed confirmation. Core files stay.
func TestMODDOC39Lifecycle(t *testing.T) {
	k := installed(t)
	f := k.file(coretest.Attorney, k.Open, "Motion.docx", []byte("motion"), nil)
	k.want(k.do(coretest.Staff, "POST", "/api/files/"+f+"/versions/1/note", "", map[string]string{"note": "kept across reinstall"}), http.StatusOK)
	role := k.Last(documents.ID).Secrets["togavsu_db_role"]

	k.Uninstall(documents.ID, false)
	if !k.SchemaExists(documents.ID) || len(k.Backups.Made()) != 0 {
		t.Fatal("keep data dropped the schema or took a backup")
	}
	if n := k.Count(`SELECT count(*) FROM pg_roles WHERE rolname = $1 AND NOT rolcanlogin`, role); n != 1 {
		t.Fatal("the module's login is not locked")
	}
	if r := k.Staff.Do("GET", "/modules/"+documents.ID+"/tabs/templates", nil); r.Code != http.StatusNotFound {
		t.Fatalf("the tab outlived uninstall: %d", r.Code)
	}

	k.Install(k.signed)
	k.ApproveAll(documents.ID)
	var fv documents.FileVersions
	k.decode(k.want(k.do(coretest.Staff, "GET", "/api/files/"+f+"/versions", "", nil), http.StatusOK), &fv)
	if len(fv.Versions) != 1 || fv.Versions[0].Note != "kept across reinstall" {
		t.Fatalf("after reinstall %+v", fv.Versions)
	}

	k.Uninstall(documents.ID, true)
	if k.SchemaExists(documents.ID) || len(k.Backups.Made()) != 1 {
		t.Fatalf("delete data left the schema or took %d backups", len(k.Backups.Made()))
	}
	if k.Count(`SELECT count(*) FROM core.files WHERE id = $1`, f) != 1 {
		t.Fatal("a core file went with the module")
	}
	// Library and generated files staying needs them first.
	k.needs("CORE-17")
}

// tabOrNone opens the module's page in a tab the core draws for who, or reports 0 when
// the core draws none for them.
func (k *kit) tabOrNone(who, tab, matter string) int {
	k.t.Helper()
	p := "/modules/" + documents.ID + "/tabs/" + tab
	if matter != "" {
		p += "?matter_id=" + matter
	}
	if r := k.As(who).Do("GET", p, nil); r.Code != http.StatusOK {
		return 0
	}
	return k.Tab(who, documents.ID, tab, matter).Code
}
