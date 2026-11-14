package documents

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The static half of MOD-DOC-01: the package's own shape. The core's install checks run
// through pkg/modulekit in kit_test.go.

func manifest(t *testing.T) string {
	t.Helper()
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	m, ok := files["togavsu-module.yaml"]
	if !ok {
		t.Fatal("no togavsu-module.yaml in the package")
	}
	return string(m)
}

func TestPackageFiles(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	want := "migrations/0001_library.sql migrations/0002_generation.sql migrations/0003_downloads.sql migrations/0004_versions.sql migrations/0005_profiles_events.sql migrations/0006_views.sql togavsu-module.yaml"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("package files:\n%s\nwant\n%s", got, want)
	}
}

func TestManifestShape(t *testing.T) {
	m := manifest(t)
	for _, want := range []string{"id: documents\n", `version: "` + Version + `"`, "author: Chariton Media", "license: Same as Togavsu", "price: Free",
		"homepage: https://github.com/CharitonMedia/togavsu-documents", "events: /events"} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest lacks %q", want)
		}
	}
	if n := strings.Count(m, "  - scope: "); n != 11 {
		t.Errorf("%d scopes, want 11", n)
	}
	if n := strings.Count(m, "required: false"); n != 2 {
		t.Errorf("%d optional scopes, want 2 (editor:run, doc_tender:read)", n)
	}
	for _, s := range []string{"editor:run", "doc_tender:read"} {
		i := strings.Index(m, "scope: "+s)
		if i < 0 || !strings.HasPrefix(strings.TrimSpace(m[i+len("scope: "+s):]), "required: false") {
			t.Errorf("%s is not optional", s)
		}
	}
	for _, k := range []string{"default_output", "max_package_templates", "zip_layout", "drafts_window_days"} {
		if !strings.Contains(m, "key: "+k) {
			t.Errorf("setting %s missing", k)
		}
	}
	if n := strings.Count(regexp.MustCompile(`(?s)help:.*?\nevents:`).FindString(m), "    - id: "); n != 6 {
		t.Errorf("%d help pages, want 6", n)
	}
	if !strings.Contains(m, "heading: Documents and templates") {
		t.Error("help heading")
	}
	if !strings.Contains(m, "tab: drafts") || !strings.Contains(m, "- id: drafts\n    title: Generated drafts\n    placement: home") {
		t.Error("the widget target names the drafts tab, which must be a home tab")
	}
}

// Every path the manifest declares is served: without a context token a page answers
// 401, never the mux's 404.
func TestManifestPathsServed(t *testing.T) {
	m := manifest(t)
	h := New(Config{}).Handler()
	for _, match := range regexp.MustCompile(`(?m)^\s+(?:path|events): (/\S*)$`).FindAllStringSubmatch(m, -1) {
		p := match[1]
		method := http.MethodGet
		if p == "/events" {
			method = http.MethodPost
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", method, p, rec.Code)
		}
	}
}

// Migrations follow the module rules the core enforces at install: only CREATE TABLE,
// CREATE INDEX and CREATE VIEW, and every foreign key to a core table is ON DELETE
// RESTRICT.
func TestMigrationRules(t *testing.T) {
	files, _ := Files()
	coreFK := regexp.MustCompile(`REFERENCES core\.[a-z_]+ \(id\)( ON DELETE [A-Z ]+)?`)
	for name, b := range files {
		if !strings.HasPrefix(name, "migrations/") {
			continue
		}
		var stmts []string
		for _, line := range strings.Split(string(b), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			stmts = append(stmts, line)
		}
		for _, st := range strings.Split(strings.Join(stmts, "\n"), ";") {
			st = strings.TrimSpace(st)
			if st == "" {
				continue
			}
			if !regexp.MustCompile(`^CREATE (TABLE|INDEX|UNIQUE INDEX|VIEW) `).MatchString(st) {
				t.Errorf("%s: statement %q", name, st[:min(40, len(st))])
			}
		}
		for _, fk := range coreFK.FindAllStringSubmatch(string(b), -1) {
			if fk[1] != " ON DELETE RESTRICT" {
				t.Errorf("%s: %s", name, fk[0])
			}
		}
	}
}
