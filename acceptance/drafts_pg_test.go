package acceptance

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

func (k *kit) drafts(who, query string) []string {
	k.t.Helper()
	var out struct {
		Data []documents.Draft `json:"data"`
	}
	k.decode(k.want(k.do(who, "GET", "/api/drafts"+query, "", nil), http.StatusOK), &out)
	var ids []string
	for _, d := range out.Data {
		ids = append(ids, d.FileID)
	}
	sort.Strings(ids)
	return ids
}

// matterPages are the module's matter-screen pages.
var matterPages = []string{"/matter", "/matter/new", "/matter/download", "/matter/versions", "/matter/recent"}

// MOD-DOC-26, the part the core serves today: a staff user off a walled matter's wall
// gets 404 on its matter tab and on every module page for that matter.
func TestMODDOC26WalledPages(t *testing.T) {
	k := installed(t)
	if r := k.Staff.Do("GET", "/modules/"+documents.ID+"/tabs/matter?matter_id="+url.QueryEscape(k.Walled), nil); r.Code != http.StatusNotFound {
		t.Errorf("core matter tab on the walled matter: %d", r.Code)
	}
	tok := k.ctx(coretest.Staff, "")
	for _, p := range matterPages {
		if r := k.doTok(k.Staff, tok, "GET", p+"?matter_id="+k.Walled, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s on the walled matter: %d", p, r.Code)
		}
	}
	for _, p := range []string{"/api/matters/" + k.Walled + "/pickers", "/api/matters/" + k.Walled + "/runs", "/api/matters/" + k.Walled + "/zips"} {
		if r := k.doTok(k.Staff, tok, "GET", p, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
	if r := k.doTok(k.Staff, tok, "POST", "/api/matters/"+k.Walled+"/generate", map[string]any{"template_id": "00000000-0000-7000-8000-000000000000"}); r.Code != http.StatusNotFound {
		t.Errorf("generate on the walled matter: %d", r.Code)
	}
	// The attorney inside the wall reaches the same pages.
	att := k.ctx(coretest.Attorney, k.Walled)
	if r := k.doTok(k.Attorney, att, "GET", "/matter/versions?matter_id="+k.Walled, nil); r.Code != http.StatusOK {
		t.Errorf("attorney on the walled matter: %d %s", r.Code, r.Body)
	}

	// Drafts on the walled matter stay out of staff's tab and widget.
	k.needsGeneration()
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	var open, walled documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusCreated), &open)
	k.decode(k.want(k.generate(coretest.Attorney, k.Walled, map[string]any{"template_id": tm.ID}), http.StatusCreated), &walled)
	if got := k.drafts(coretest.Staff, ""); len(got) != 1 || got[0] != open.FileIDs[0] {
		t.Fatalf("staff drafts %v", got)
	}
	if w := k.want(k.do(coretest.Staff, "GET", "/widget/drafts", "", nil), http.StatusOK); strings.Contains(w.Body, walled.FileIDs[0]) {
		t.Fatal("the widget shows the walled draft")
	}
	if r := k.doTok(k.Staff, tok, "POST", "/api/drafts/"+walled.FileIDs[0]+"/status", map[string]string{"status": "final"}); r.Code != http.StatusNotFound {
		t.Errorf("status write on the walled draft: %d", r.Code)
	}
}

// MOD-DOC-25: Mark final sets the core status and drops the file from the default list;
// Mark draft returns it.
func TestMODDOC25MarkFinal(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusCreated), &run)
	id := run.FileIDs[0]
	k.want(k.do(coretest.Attorney, "POST", "/api/drafts/"+id+"/status", "", map[string]string{"status": "final"}), http.StatusOK)
	if n := k.Count(`SELECT count(*) FROM core.files WHERE id = $1 AND status = 'final' AND lower(name) NOT LIKE '%final%'`, id); n != 1 {
		t.Fatal("the core status is not final, or the name changed")
	}
	if got := k.drafts(coretest.Attorney, ""); len(got) != 0 {
		t.Fatalf("drafts after final %v", got)
	}
	if got := k.drafts(coretest.Attorney, "?status=final"); len(got) != 1 {
		t.Fatalf("final filter %v", got)
	}
	k.want(k.do(coretest.Attorney, "POST", "/api/drafts/"+id+"/status", "", map[string]string{"status": "draft"}), http.StatusOK)
	if got := k.drafts(coretest.Attorney, ""); len(got) != 1 {
		t.Fatalf("drafts after draft %v", got)
	}
}

// MOD-DOC-27: the widget's click-through opens the drafts tab with matter_id, and the
// tab shows only that matter's drafts.
func TestMODDOC27WidgetLink(t *testing.T) {
	k := installed(t)
	k.needsGeneration()
	w := k.world()
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	var one, other documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, w.Matter, map[string]any{"template_id": tm.ID}), http.StatusCreated), &one)
	k.decode(k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusCreated), &other)
	wd := k.want(k.do(coretest.Attorney, "GET", "/widget/drafts", "", nil), http.StatusOK)
	if !strings.Contains(wd.Body, "/modules/documents/tabs/drafts?matter_id="+w.Matter) {
		t.Fatalf("widget link:\n%s", wd.Body)
	}
	tab := k.Tab(coretest.Attorney, documents.ID, "drafts", "")
	_ = tab
	page := k.want(k.do(coretest.Attorney, "GET", "/drafts?matter_id="+w.Matter, "", nil), http.StatusOK)
	if !strings.Contains(page.Body, one.FileIDs[0]) || strings.Contains(page.Body, other.FileIDs[0]) {
		t.Fatalf("drafts tab:\n%s", page.Body)
	}
	// Following the link in the frame is the frame bridge's navigate message.
	k.needs("CORE-09")
}
