package acceptance

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// zipWorld is MOD-DOC-28's matter on the real core: four files in two folders, one
// unfiled, one trashed, and one attorney-only.
type zipWorld struct {
	matter, pleadings                                         string
	petition, answer, letter, reply, notes, trashed, strategy string
}

func (k *kit) zipWorld() zipWorld {
	k.t.Helper()
	w := k.world()
	z := zipWorld{matter: w.Matter}
	folder := func(name string) string {
		return k.id(k.Admin.Must(http.StatusCreated, "POST", "/api/v1/folders", map[string]any{"matter_id": w.Matter, "name": name}))
	}
	z.pleadings = folder("Pleadings")
	corr := folder("Correspondence")
	put := func(name, body, folder string, extra map[string]any) string {
		id := k.file(coretest.Attorney, w.Matter, name, []byte(body), extra)
		if folder != "" {
			k.Attorney.Must(http.StatusOK, "POST", "/api/v1/files/"+id+"/move", map[string]any{"folder_id": folder})
		}
		return id
	}
	z.petition = put("Petition.docx", "petition v1", z.pleadings, nil)
	k.version(coretest.Attorney, z.petition, w.Matter, "Petition.docx", []byte("petition v2"))
	k.Attorney.Must(http.StatusOK, "POST", "/api/v1/files/"+z.petition+"/status", map[string]string{"status": "final"})
	z.answer = put("Answer.docx", "answer", z.pleadings, nil)
	z.letter = put("Letter.docx", "letter", corr, nil)
	k.Attorney.Must(http.StatusOK, "POST", "/api/v1/files/"+z.letter+"/status", map[string]string{"status": "final"})
	z.reply = put("Reply.pdf", "reply", corr, nil)
	z.notes = put("Notes.txt", "notes", "", nil)
	z.trashed = put("Old.docx", "old", z.pleadings, nil)
	k.Attorney.Must(http.StatusOK, "POST", "/api/v1/files/"+z.trashed+"/trash", nil)
	z.strategy = put("Strategy.docx", "strategy", "", map[string]any{"attorney_only": true})
	return z
}

func (k *kit) zip(who, matter string, in map[string]any) coretest.Resp {
	k.t.Helper()
	return k.do(who, "POST", "/api/matters/"+matter+"/zip", matter, in)
}

func entryNames(b string) string {
	var out []string
	for n := range zipParts([]byte(b)) {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

func (k *kit) lastBulk(who, matter string) documents.Bulk {
	k.t.Helper()
	var out struct {
		Data []documents.Bulk `json:"data"`
	}
	k.decode(k.want(k.do(who, "GET", "/api/matters/"+matter+"/zips", matter, nil), http.StatusOK), &out)
	if len(out.Data) == 0 {
		k.t.Fatal("no zip manifest")
	}
	return out.Data[0]
}

// MOD-DOC-28, 29, 31 and 32: whole-matter, folder, selection and status zips, nothing
// to download, quarantine, and a cut stream.
func TestMODDOC28to32Zip(t *testing.T) {
	k := installed(t)
	k.needs("CORE-21", "CORE-18")
	z := k.zipWorld()

	// 28: the whole matter as staff holds exactly the five visible current versions.
	downloads := k.Count(`SELECT count(*) FROM core.events WHERE action = 'file.downloaded'`)
	r := k.want(k.zip(coretest.Staff, z.matter, map[string]any{}), http.StatusOK)
	if got := entryNames(r.Body); got != "Correspondence/Letter.docx|Correspondence/Reply.pdf|Pleadings/Answer.docx|Pleadings/Petition.docx|Unfiled/Notes.txt" {
		t.Fatalf("entries %s", got)
	}
	parts := zipParts([]byte(r.Body))
	b := k.lastBulk(coretest.Staff, z.matter)
	if b.Status != "complete" || b.EntryCount != 5 || len(b.Items) != 5 {
		t.Fatalf("manifest %+v", b)
	}
	for _, it := range b.Items {
		if sum(parts[it.Path]) != it.SHA256 || (it.Path == "Pleadings/Petition.docx" && it.VersionNumber != 2) {
			t.Fatalf("manifest row %+v", it)
		}
	}
	if n := k.Count(`SELECT count(*) FROM core.events WHERE action = 'file.downloaded'`) - downloads; n != 5 {
		t.Fatalf("%d file.downloaded", n)
	}
	if n := k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.bulk_downloaded' AND matter_id = $1`, z.matter); n != 1 {
		t.Fatalf("%d bulk events", n)
	}

	// 29: folder, selection and the final filter give exactly their files.
	if got := entryNames(k.want(k.zip(coretest.Staff, z.matter, map[string]any{"scope": "folder", "folder_id": z.pleadings}), http.StatusOK).Body); got != "Pleadings/Answer.docx|Pleadings/Petition.docx" {
		t.Fatalf("folder %s", got)
	}
	if got := entryNames(k.want(k.zip(coretest.Staff, z.matter, map[string]any{"scope": "selection", "file_ids": []string{z.reply, z.notes}, "layout": "flat"}), http.StatusOK).Body); got != "Notes.txt|Reply.pdf" {
		t.Fatalf("selection %s", got)
	}
	if got := entryNames(k.want(k.zip(coretest.Staff, z.matter, map[string]any{"status": "final"}), http.StatusOK).Body); got != "Correspondence/Letter.docx|Pleadings/Petition.docx" {
		t.Fatalf("final %s", got)
	}

	// 31: an empty scope writes nothing; a quarantined version left out is absent.
	rows := k.moduleCount(`SELECT count(*) FROM bulk_downloads`)
	r = k.want(k.zip(coretest.Staff, z.matter, map[string]any{"status": "executed"}), http.StatusUnprocessableEntity)
	if !strings.Contains(r.Body, "Nothing to download") || k.moduleCount(`SELECT count(*) FROM bulk_downloads`) != rows {
		t.Fatalf("empty scope %s", r.Body)
	}
	var vs struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	k.coreJSON(coretest.Admin, "/api/v1/files/"+z.letter+"/versions", &vs)
	k.Admin.Must(http.StatusOK, "POST", "/api/v1/files/"+z.letter+"/versions/"+vs.Data[0].ID+"/quarantine", map[string]any{"quarantined": true})
	k.want(k.zip(coretest.Staff, z.matter, map[string]any{}), http.StatusConflict)
	r = k.want(k.zip(coretest.Staff, z.matter, map[string]any{"quarantine": "leave_out"}), http.StatusOK)
	if strings.Contains(entryNames(r.Body), "Letter.docx") {
		t.Fatal("the quarantined version is in the zip")
	}
	for _, it := range k.lastBulk(coretest.Staff, z.matter).Items {
		if it.FileID == z.letter {
			t.Fatal("the quarantined version is in the manifest")
		}
	}

	// 32: the test cuts the stream mid-zip: the manifest is interrupted, no summary event.
	events := k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.bulk_downloaded'`)
	tok := k.ctx(coretest.Staff, z.matter)
	req, _ := http.NewRequest("POST", k.URL+at("/api/matters/"+z.matter+"/zip", tok), strings.NewReader(`{"quarantine":"leave_out"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.CopyN(io.Discard, res.Body, 64)
	res.Body.Close()
	if b := k.lastBulk(coretest.Staff, z.matter); b.Status != "interrupted" {
		t.Fatalf("cut manifest %s", b.Status)
	}
	if k.Count(`SELECT count(*) FROM core.events WHERE action = 'documents.bulk_downloaded'`) != events {
		t.Fatal("a cut zip wrote a summary event")
	}
}

// MOD-DOC-30: all versions with Markdown on a file with two versions writes name-v1.docx,
// name-v2.docx, name-v1.md and name-v2.md.
func TestMODDOC30ZipVersions(t *testing.T) {
	k := installed(t)
	k.needs("CORE-21", "CORE-18", "CORE-29 rendition stand-in")
	f := k.file(coretest.Attorney, k.Open, "name.docx", []byte("one"), nil)
	k.version(coretest.Attorney, f, k.Open, "name.docx", []byte("two"))
	r := k.want(k.zip(coretest.Staff, k.Open, map[string]any{"versions": "all", "with_markdown": true, "layout": "flat"}), http.StatusOK)
	if got := entryNames(r.Body); got != "name-v1.docx|name-v1.md|name-v2.docx|name-v2.md" {
		t.Fatalf("entries %s", got)
	}
}
