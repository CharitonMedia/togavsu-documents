package documents

import (
	"strings"
	"testing"
)

func zf(id, name, folder string, versions ...coreVersion) zipFile {
	cur := versions[len(versions)-1].ID
	return zipFile{File: coreFile{ID: id, Name: name, CurrentVersionID: &cur}, Folder: folder, Versions: versions}
}

func ver(id string, n int) coreVersion { return coreVersion{ID: id, Number: n, SHA256: "sha-" + id} }

func paths(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Path)
	}
	return out
}

func TestLayoutFoldersAndFlat(t *testing.T) {
	files := []zipFile{
		zf("f1", "Motion.docx", "Pleadings", ver("a", 1), ver("b", 2)),
		zf("f2", "Letter.docx", "Correspondence", ver("c", 1)),
		zf("f3", "Notes.txt", "", ver("d", 1)),
	}
	got := strings.Join(paths(layoutEntries(files, "current", "folders", false)), "|")
	if got != "Pleadings/Motion.docx|Correspondence/Letter.docx|Unfiled/Notes.txt" {
		t.Fatalf("folders: %s", got)
	}
	es := layoutEntries(files, "current", "folders", false)
	if es[0].VersionID != "b" || es[0].VersionNumber != 2 {
		t.Fatalf("current version not picked: %+v", es[0])
	}
	got = strings.Join(paths(layoutEntries(files, "current", "flat", false)), "|")
	if got != "Motion.docx|Letter.docx|Notes.txt" {
		t.Fatalf("flat: %s", got)
	}
}

// MOD-DOC-30: all versions with Markdown write name-v1.docx, name-v2.docx, name-v1.md
// and name-v2.md where those renditions exist.
func TestLayoutAllVersionsMarkdown(t *testing.T) {
	f := zf("f1", "name.docx", "", ver("a", 1), ver("b", 2))
	f.Renditions = []coreRendition{{ID: "ra", FileVersionID: "a", Kind: "markdown", Number: 1}, {ID: "rb", FileVersionID: "b", Kind: "markdown", Number: 1},
		{ID: "rp", FileVersionID: "b", Kind: "pdf_preview", Number: 1}}
	es := layoutEntries([]zipFile{f}, "all", "flat", true)
	got := strings.Join(paths(es), "|")
	if got != "name-v1.docx|name-v1.md|name-v2.docx|name-v2.md" {
		t.Fatalf("got %s", got)
	}
	if es[1].Kind != "markdown" || es[1].RenditionID != "ra" || es[3].RenditionID != "rb" {
		t.Fatalf("markdown entries: %+v", es)
	}
	// Only one rendition: only that version gets a .md.
	f.Renditions = f.Renditions[1:2]
	got = strings.Join(paths(layoutEntries([]zipFile{f}, "all", "flat", true)), "|")
	if got != "name-v1.docx|name-v2.docx|name-v2.md" {
		t.Fatalf("got %s", got)
	}
	// Current version with Markdown: <base>.md.
	got = strings.Join(paths(layoutEntries([]zipFile{f}, "current", "flat", true)), "|")
	if got != "name.docx|name.md" {
		t.Fatalf("got %s", got)
	}
}

func TestLayoutClashes(t *testing.T) {
	files := []zipFile{
		zf("f1", "Motion.docx", "Pleadings", ver("a", 1)),
		zf("f2", "motion.DOCX", "Pleadings", ver("b", 1)),
		zf("f3", "Motion.docx", "Pleadings", ver("c", 1)),
		zf("f4", "Motion.docx", "Other", ver("d", 1)),
	}
	got := strings.Join(paths(layoutEntries(files, "current", "folders", false)), "|")
	if got != "Pleadings/Motion.docx|Pleadings/motion-2.DOCX|Pleadings/Motion-3.docx|Other/Motion.docx" {
		t.Fatalf("got %s", got)
	}
}

// The CC-5 path rules: relative, / separated, at most 255 bytes, no leading /, no
// backslash, no . or .. segment, no control character.
func TestPathRules(t *testing.T) {
	files := []zipFile{
		zf("f1", "a/b\\c.docx", "Folder/One", ver("a", 1)),
		zf("f2", "..", "..", ver("b", 1)),
		zf("f3", "bad\x01name.docx", ".", ver("c", 1)),
		zf("f4", strings.Repeat("x", 300)+".docx", "Long", ver("d", 1)),
	}
	for _, e := range layoutEntries(files, "current", "folders", false) {
		if !validPath(e.Path) {
			t.Errorf("invalid path %q", e.Path)
		}
	}
	es := layoutEntries(files, "current", "folders", false)
	if es[0].Path != "Folder-One/a-b-c.docx" {
		t.Errorf("slashes: %q", es[0].Path)
	}
	if !strings.HasSuffix(es[3].Path, ".docx") || len(es[3].Path) > 255 {
		t.Errorf("long: %d %q", len(es[3].Path), es[3].Path[len(es[3].Path)-10:])
	}
	for _, p := range []string{"/abs", "a\\b", "a/./b", "a/../b", "a//b", "a\x00b", strings.Repeat("y", 256)} {
		if validPath(p) {
			t.Errorf("%q passed", p)
		}
	}
}

func TestZipInputCheck(t *testing.T) {
	in := zipInput{}
	if err := in.check("flat"); err != nil || in.Scope != "matter" || in.Versions != "current" || in.Layout != "flat" {
		t.Fatalf("defaults: %+v %v", in, err)
	}
	in = zipInput{Scope: "selection"}
	if err := in.check(""); err != errNothing {
		t.Fatalf("empty selection: %v", err)
	}
	in = zipInput{Status: "lost"}
	if err := in.check(""); err == nil {
		t.Fatal("bad status passed")
	}
}
