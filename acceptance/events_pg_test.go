package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// post sends a webhook body straight to the module's server with signature header sig.
func (k *kit) post(body, sig string) int {
	k.t.Helper()
	req, _ := http.NewRequest("POST", strings.TrimRight(k.Runtime.BaseURL(documents.ID), "/")+"/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(modkit.SignatureHeader, sig)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// MOD-DOC-35: the core's own delivery of file.updated verifies and is recorded once; a
// repeat of the same event id changes nothing; a bad signature or a stale timestamp is
// 401 and changes nothing.
func TestMODDOC35Events(t *testing.T) {
	k := installed(t)
	secret := k.Last(documents.ID).Secrets["togavsu_webhook_secret"]
	f := k.file(coretest.Attorney, k.Open, "Letter.docx", []byte("letter"), nil)
	k.DeliverEvents()
	base := k.moduleCount(`SELECT count(*) FROM received_events`)

	// A real file.updated from the core.
	k.Attorney.Must(http.StatusOK, "PATCH", "/api/v1/files/"+f, map[string]any{"attorney_only": true})
	if sent := k.DeliverEvents(); sent == 0 {
		t.Fatal("the core delivered no event")
	}
	if n := k.moduleCount(`SELECT count(*) FROM received_events WHERE action = 'file.updated' AND record_id = $1`, f); n != 1 {
		t.Fatalf("%d file.updated rows after the core's delivery", n)
	}
	var id string
	if err := k.ModuleDB(documents.ID).QueryRow(`SELECT event_id FROM received_events WHERE record_id = $1`, f).Scan(&id); err != nil {
		t.Fatal(err)
	}
	total := k.moduleCount(`SELECT count(*) FROM received_events`)
	if total <= base {
		t.Fatal("nothing recorded")
	}

	// The same event id again, signed with the deployment's secret: nothing changes.
	body := `{"id":"` + id + `","action":"file.updated","record_type":"file","record_id":"` + f + `"}`
	if st := k.post(body, modkit.SignWebhook(secret, time.Now(), []byte(body))); st != http.StatusOK {
		t.Fatalf("repeat %d", st)
	}
	// A bad signature and a stale timestamp are refused.
	other := `{"id":"synthetic-1","action":"file.updated","record_type":"file","record_id":"` + f + `"}`
	if st := k.post(other, modkit.SignWebhook("wrong-secret", time.Now(), []byte(other))); st != http.StatusUnauthorized {
		t.Fatalf("bad signature %d", st)
	}
	if st := k.post(other, modkit.SignWebhook(secret, time.Now().Add(-6*time.Minute), []byte(other))); st != http.StatusUnauthorized {
		t.Fatalf("stale timestamp %d", st)
	}
	if k.moduleCount(`SELECT count(*) FROM received_events`) != total {
		t.Fatal("a repeat or a refused event changed something")
	}

	// The status cache of a tracked (generated) file.
	k.needsGeneration()
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusCreated), &run)
	k.DeliverEvents()
	k.Attorney.Must(http.StatusOK, "PATCH", "/api/v1/files/"+run.FileIDs[0], map[string]any{"attorney_only": true})
	k.DeliverEvents()
	if n := k.moduleCount(`SELECT count(*) FROM generated_files WHERE file_id = $1 AND status_cache = 'stale'`, run.FileIDs[0]); n != 1 {
		t.Fatal("file.updated did not mark the cache")
	}
}

// MOD-DOC-36: purging a trashed generated file is refused naming documents; after
// Release the purge succeeds and the manifest row keeps its path and SHA-256.
func TestMODDOC36Release(t *testing.T) {
	k := installed(t)
	for _, who := range []string{coretest.Attorney, coretest.Staff} {
		if r := k.do(who, "GET", "/release", "", nil); r.Code != http.StatusNotFound {
			t.Fatalf("%s: release page %d", who, r.Code)
		}
	}
	k.needsGeneration()
	k.needs("CORE-21")
	tm := k.template(coretest.Attorney, "Letter", docx(para("{{matter_number}}")), nil)
	var run documents.Run
	k.decode(k.want(k.generate(coretest.Attorney, k.Open, map[string]any{"template_id": tm.ID}), http.StatusCreated), &run)
	fid := run.FileIDs[0]
	k.want(k.zip(coretest.Attorney, k.Open, map[string]any{"scope": "selection", "file_ids": []string{fid}}), http.StatusOK)
	k.Attorney.Must(http.StatusOK, "POST", "/api/v1/files/"+fid+"/trash", nil)
	k.DeliverEvents()
	name := "Letter"
	var fn struct {
		Name string `json:"name"`
	}
	k.coreJSON(coretest.Admin, "/api/v1/files/"+fid, &fn)
	name = fn.Name
	r := k.Admin.Do("POST", "/api/v1/files/"+fid+"/purge", map[string]string{"confirm_name": name})
	if r.Code != http.StatusConflict || !strings.Contains(r.Body, "documents") {
		t.Fatalf("purge before release %d %s", r.Code, r.Body)
	}
	k.want(k.do(coretest.Admin, "POST", "/api/release/"+fid, "", nil), http.StatusOK)
	k.Admin.Must(http.StatusNoContent, "POST", "/api/v1/files/"+fid+"/purge", map[string]string{"confirm_name": name})
	if k.moduleCount(`SELECT count(*) FROM bulk_download_items WHERE file_id IS NULL AND sha256 <> '' AND entry_path <> ''`) != 1 {
		t.Fatal("the manifest row lost its path or SHA-256")
	}
}
