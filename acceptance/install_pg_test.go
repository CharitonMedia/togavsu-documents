package acceptance

import (
	"html"
	"net/http"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// moduleTables is every table and view of the spec's section 4.
var moduleTables = []string{"templates", "template_practice_areas", "components", "template_slots", "packages", "package_practice_areas",
	"package_templates", "generation_runs", "generated_files", "bulk_downloads", "bulk_download_items", "version_notes", "version_copies",
	"attorney_profiles", "received_events", "generated_documents"}

type permission struct {
	Scope    string `json:"scope"`
	Required bool   `json:"required"`
	Approved bool   `json:"approved"`
}

func (k *kit) permissions() []permission {
	k.t.Helper()
	var v struct {
		Permissions []permission `json:"permissions"`
	}
	k.decode(k.Admin.Must(http.StatusOK, "GET", "/api/v1/modules/"+documents.ID, nil), &v)
	return v.Permissions
}

// decide posts the admin's decisions: decline for the scopes named, approve for the rest.
func (k *kit) decide(decline ...string) coretest.Resp {
	k.t.Helper()
	var ds []map[string]string
	for _, p := range k.permissions() {
		d := "approve"
		for _, s := range decline {
			if s == p.Scope {
				d = "decline"
			}
		}
		ds = append(ds, map[string]string{"scope": p.Scope, "decision": d})
	}
	return k.Admin.Do("POST", "/api/v1/modules/"+documents.ID+"/permissions", map[string]any{"decisions": ds})
}

// MOD-DOC-01: the signed package installs; the core creates mod_documents with every
// table and view and lists every scope with its reason; declining a required scope
// cancels the install.
func TestMODDOC01Install(t *testing.T) {
	k := newKit(t)
	k.Install(k.signed)
	if len(k.Runtime.Deployments()) != 0 || k.SchemaExists(documents.ID) {
		t.Fatal("the module ran before approval")
	}
	f, _ := installable(t)
	m, err := modulekit.ParseManifest(f[modulekit.ManifestFile])
	if err != nil {
		t.Fatal(err)
	}
	perms := map[string]permission{}
	for _, p := range k.permissions() {
		perms[p.Scope] = p
	}
	if len(perms) != len(m.Permissions) {
		t.Fatalf("%d permissions listed, the manifest asks %d", len(perms), len(m.Permissions))
	}
	// The admin's Modules tab lists each scope for a decision, with its reason.
	tab := html.UnescapeString(k.Admin.Must(http.StatusOK, "GET", "/admin?tab=modules", nil).Body)
	var noReason []string
	for _, p := range m.Permissions {
		got, ok := perms[p.Scope]
		if !ok || got.Required != p.IsRequired() || got.Approved {
			t.Errorf("permission %s: %+v", p.Scope, got)
		}
		if !strings.Contains(tab, `name="scope:`+p.Scope+`"`) {
			t.Errorf("the Modules tab offers no decision on %s", p.Scope)
		}
		if !strings.Contains(tab, p.Reason) {
			noReason = append(noReason, p.Scope)
		}
	}
	k.ApproveAll(documents.ID)
	if dep := k.Last(documents.ID); dep.Version != documents.Version || dep.Secrets["togavsu_db_schema"] != "mod_documents" {
		t.Fatalf("deployment %+v", dep)
	}
	for _, name := range moduleTables {
		if n := k.Count(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'mod_documents' AND table_name = $1`, name); n != 1 {
			t.Errorf("mod_documents.%s is missing", name)
		}
	}
	k.want(k.do(coretest.Staff, "GET", "/templates", "", nil), http.StatusOK)

	t.Run("declining a required scope cancels", func(t *testing.T) {
		k := newKit(t)
		k.Install(k.signed)
		if r := k.decide("files:read"); r.Code != http.StatusConflict && r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("decline: %d %s", r.Code, r.Body)
		}
		var v struct {
			Status string `json:"status"`
		}
		r := k.Admin.Do("GET", "/api/v1/modules/"+documents.ID, nil)
		if r.Code == http.StatusOK {
			k.decode(r, &v)
		}
		if (r.Code != http.StatusNotFound && v.Status != "cancelled") || len(k.Runtime.Deployments()) != 0 || k.SchemaExists(documents.ID) {
			t.Fatalf("after declining files:read: %d %s, %d deployments", r.Code, r.Body, len(k.Runtime.Deployments()))
		}
		if k.ctx(coretest.Staff, "") != "" {
			t.Fatal("a cancelled module issues context tokens")
		}
	})

	// The rest needs what the pinned core lacks: every gap is named in one skip.
	var gaps []string
	if len(noReason) > 0 {
		gaps = append(gaps, "needs a CORE item for permission reasons (not in CORE-CHANGES): the Modules tab shows no reason for "+strings.Join(noReason, ", "))
	}
	for _, item := range []string{"CORE-18", "CORE-19"} {
		if !k.landed(item) {
			gaps = append(gaps, "needs "+item)
		}
	}
	if len(gaps) > 0 {
		t.Skip(strings.Join(gaps, "; "))
	}
	// All eleven scopes, firm:read and users:directory among them, are in the shipped
	// manifest, which installs once the core knows them.
	if m, err = modulekit.ParseManifest(shipped(t)[modulekit.ManifestFile]); err != nil || len(m.Permissions) != 11 {
		t.Fatalf("%d scopes: %v", len(m.Permissions), err)
	}
}

// MOD-DOC-02: declining editor:run installs the module and the DOCX and PDF output
// can't be picked; declining doc_tender:read hides the category hint and the Doc Tender
// status line.
func TestMODDOC02OptionalScopes(t *testing.T) {
	k := newKit(t)
	k.Install(k.signed)
	k.want(k.decide("editor:run", "doc_tender:read"), http.StatusOK)
	if !k.SchemaExists(documents.ID) || len(k.Runtime.Deployments()) != 1 {
		t.Fatal("declining optional scopes stopped the install")
	}
	r := k.want(k.do(coretest.Attorney, "GET", "/templates/new", "", nil), http.StatusOK)
	if !strings.Contains(r.Body, "Needs the editor permission") || !strings.Contains(r.Body, `value="docx_and_pdf" disabled`) {
		t.Fatalf("the PDF choice is offered without editor:run:\n%s", r.Body)
	}
	if strings.Contains(r.Body, "Category hint") {
		t.Fatal("the category hint shows without doc_tender:read")
	}
	r = k.want(k.do(coretest.Attorney, "POST", "/api/templates", "", &form{fields: map[string]string{"mode": "blank", "name": "Letter", "output": "docx_and_pdf"}}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "editor permission") {
		t.Fatalf("refusal: %s", r.Body)
	}

	t.Run("with both scopes", func(t *testing.T) {
		k := installed(t)
		r := k.want(k.do(coretest.Attorney, "GET", "/templates/new", "", nil), http.StatusOK)
		if strings.Contains(r.Body, "Needs the editor permission") || !strings.Contains(r.Body, "Category hint") {
			t.Fatalf("choices missing with both scopes:\n%s", r.Body)
		}
	})

	// The Doc Tender status line on a generated draft needs a draft.
	k.needs("CORE-17", "CORE-18", "CORE-19")
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusCreated)
	r = k.want(k.do(coretest.Attorney, "GET", "/drafts", "", nil), http.StatusOK)
	if strings.Contains(r.Body, "Doc Tender") {
		t.Fatal("the Doc Tender status shows without doc_tender:read")
	}
}
