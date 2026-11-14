package documents

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CharitonMedia/togavsu-documents/internal/fakecore"
	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The acceptance environment: the module in process against the fake core, with the
// module's schema migrated into its own test database (togavsu_documents) beside a
// skeleton core schema, so foreign keys and the purge refusal work as on a real core.

const testDB = "togavsu_documents"

// fixedNow is the test clock: 3pm UTC, 9am in the firm's Chicago time zone.
var fixedNow = time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t      *testing.T
	core   *fakecore.Core
	srv    *Server
	mod    *httptest.Server
	db     *sql.DB // the module's login: search_path mod_documents
	coreDB *sql.DB
	logs   *syncBuf
	now    time.Time

	admin, attorney, staff, accountant, client, readonly *fakecore.User
	family, estate                                      string
	matter                                              *fakecore.Matter
	avery, casey                                        *fakecore.Contact
	clientParty, oppParty                               *fakecore.Party
	county                                              string
}

func testURL(t *testing.T) string {
	u := os.Getenv("TOGAVSU_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TOGAVSU_TEST_DATABASE_URL is not set")
	}
	return u
}

var dbOnce sync.Once

// openDB makes sure the module's own test database exists and returns its URL.
func openDB(t *testing.T) string {
	admin := testURL(t)
	var err error
	dbOnce.Do(func() {
		var db *sql.DB
		db, err = sql.Open("pgx", admin)
		if err != nil {
			return
		}
		defer db.Close()
		var n int
		if err = db.QueryRow(`SELECT count(*) FROM pg_database WHERE datname = $1`, testDB).Scan(&n); err != nil {
			return
		}
		if n == 0 {
			_, err = db.Exec(`CREATE DATABASE ` + testDB)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + testDB
	return u.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := openDB(t)
	coreDB, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { coreDB.Close() })
	// Fresh schemas in the module's own database for each test.
	for _, q := range []string{`DROP SCHEMA IF EXISTS mod_documents CASCADE`, `DROP SCHEMA IF EXISTS core CASCADE`, fakecore.Schema, `CREATE SCHEMA mod_documents`} {
		if _, err := coreDB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	var migs []string
	for n := range files {
		if strings.HasPrefix(n, "migrations/") {
			migs = append(migs, n)
		}
	}
	sort.Strings(migs)
	conn, err := coreDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `SET search_path TO mod_documents`); err != nil {
		t.Fatal(err)
	}
	for _, n := range migs {
		if _, err := conn.ExecContext(context.Background(), string(files[n])); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	conn.Close()

	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", "mod_documents")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	e := &env{t: t, db: db, coreDB: coreDB, logs: &syncBuf{}, now: fixedNow}
	clock := func() time.Time { return e.now }
	e.core = fakecore.New(coreDB, clock)
	t.Cleanup(e.core.Close)
	e.srv = New(Config{Core: e.core.Server.URL, Token: e.core.ModuleToken, WebhookSecret: e.core.Secret, DB: db, Now: clock,
		Log: log.New(e.logs, "", 0)})
	e.mod = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.mod.Close)
	e.seed()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("module log:\n%s", e.logs.String())
		}
	})
	return e
}

// seed writes the synthetic firm and matter. Every name and number is made up.
func (e *env) seed() {
	c := e.core
	c.Settings = map[string]string{"default_output": "docx", "max_package_templates": "25", "zip_layout": "folders", "drafts_window_days": "30"}
	c.Firm = map[string]string{"name": "Sample Law PLLC", "short_name": "Sample Law", "address": "100 Main St, Testville, TX 75000", "phone": "555-0100",
		"email": "office@example.test", "website": "https://example.test", "time_zone": "America/Chicago"}
	e.admin = c.AddUser("Robin Admin", "admin")
	e.attorney = c.AddUser("Jordan Q. Testlawyer", "attorney")
	e.staff = c.AddUser("Sam Staff", "staff")
	e.accountant = c.AddUser("Alex Ledger", "accountant")
	e.client = c.AddUser("Avery Sample", "client")
	e.readonly = c.AddUser("Riley Reader", "staff")
	e.readonly.ReadOnly = true
	e.family = c.AddPracticeArea("Family law")
	e.estate = c.AddPracticeArea("Estate planning")
	e.county = c.ID()
	c.Counties[e.county] = "Testcounty"
	c.PartyRoles["client"] = "Client"
	c.PartyRoles["opposing_party"] = "Respondent"
	c.CFDefs = []fakecore.CFDef{
		{ID: c.ID(), RecordType: "matter", Key: "judge", Label: "Judge", FieldType: "text"},
		{ID: c.ID(), RecordType: "matter", Key: "hearing", Label: "Hearing", FieldType: "date"},
		{ID: c.ID(), RecordType: "contact", Key: "employer", Label: "Employer", FieldType: "text"},
		{ID: c.ID(), RecordType: "matter", Key: "old", Label: "Old", FieldType: "text", Retired: true},
	}
	e.avery = c.AddContact(fakecore.Contact{FullName: "Avery Sample", GivenName: "Avery", FamilyName: "Sample", AlsoKnownAs: []string{"A. Sample"},
		CustomFields: []fakecore.CustomValue{{Key: "employer", Value: "Testco"}}})
	e.casey = c.AddContact(fakecore.Contact{FullName: "Casey Example", GivenName: "Casey", FamilyName: "Example"})
	old := "2020-01-01"
	c.AddAddress(fakecore.Address{ContactID: e.avery.ID, Line1: "9 Old Rd", City: "Oldtown", Region: "TX", PostalCode: "75999", EffectiveTo: &old})
	c.AddAddress(fakecore.Address{ContactID: e.avery.ID, Primary: true, Line1: "1 Test Way", Line2: "Apt 2", City: "Testville", Region: "TX", PostalCode: "75001"})
	c.AddAddress(fakecore.Address{ContactID: e.casey.ID, Primary: true, Line1: "5 Other St", City: "Testville", Region: "TX", PostalCode: "75002"})
	att := e.attorney.ID
	county := e.county
	e.matter = c.AddMatter(fakecore.Matter{Number: "2026-0001", Name: "Sample v. Example", PracticeAreaID: e.family, CaseNumber: "CV-26-001",
		Court: "District Court", CountyID: &county, ClientPosition: "Petitioner", ResponsibleAttorneyUserID: &att,
		CustomFields: []fakecore.CustomValue{{Key: "judge", Value: "Hon. Test"}, {Key: "hearing", Value: "2026-12-01"}}})
	e.clientParty = c.AddParty(fakecore.Party{MatterID: e.matter.ID, ContactID: e.avery.ID, Role: "client", Primary: true})
	e.oppParty = c.AddParty(fakecore.Party{MatterID: e.matter.ID, ContactID: e.casey.ID, Role: "opposing_party", Primary: true})
}

func (e *env) tok(u *fakecore.User) string { return e.core.Token(u.ID, "") }

// resp is one answer from the module.
type resp struct {
	Status int
	Body   []byte
	Header http.Header
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("not JSON (%d): %s", r.Status, r.Body)
	}
}

func (r resp) str() string { return string(r.Body) }

// do calls the module as a viewer. body is JSON, url.Values (a form) or *multipartBody.
func (e *env) do(method, path, tok string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		rd, ct = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	case *multipartBody:
		rd, ct = &b.buf, b.ct
	default:
		j, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(j), "application/json"
	}
	req, err := http.NewRequest(method, e.mod.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if tok != "" {
		req.Header.Set(modkit.ContextHeader, tok)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Body: b, Header: res.Header}
}

// page loads a page the way the core frames it: ?context= on the URL.
func (e *env) page(path, tok string) resp {
	e.t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return e.do(http.MethodGet, path+sep+"context="+url.QueryEscape(tok), "", nil)
}

type multipartBody struct {
	buf bytes.Buffer
	ct  string
}

func multipartForm(fields map[string]string, fileName string, data []byte) *multipartBody {
	m := &multipartBody{}
	w := multipart.NewWriter(&m.buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if fileName != "" {
		fw, _ := w.CreateFormFile("file", fileName)
		_, _ = fw.Write(data)
	}
	w.Close()
	m.ct = w.FormDataContentType()
	return m
}

func (e *env) want(r resp, status int) {
	e.t.Helper()
	if r.Status != status {
		e.t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

// uploadTemplate creates a template from DOCX bytes and returns it.
func (e *env) uploadTemplate(tok, name string, body []byte, fields map[string]string) Template {
	e.t.Helper()
	f := map[string]string{"mode": "upload", "name": name}
	for k, v := range fields {
		f[k] = v
	}
	r := e.do(http.MethodPost, "/api/templates", tok, multipartForm(f, slugify(name)+".docx", body))
	e.want(r, http.StatusCreated)
	var t Template
	r.json(e.t, &t)
	return t
}

func (e *env) uploadComponent(tok, name, kind string, body []byte) Component {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/components", tok, multipartForm(map[string]string{"mode": "upload", "name": name, "category": kind}, slugify(name)+".docx", body))
	e.want(r, http.StatusCreated)
	var c Component
	r.json(e.t, &c)
	return c
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (e *env) generate(tok, matterID string, in map[string]any) resp {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/matters/"+matterID+"/generate", tok, in)
}

func (e *env) events(action string) []fakecore.Event {
	var out []fakecore.Event
	for _, ev := range e.core.EventsCopy() {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

// currentBytes is a file's current version bytes in the core.
func (e *env) currentBytes(fileID string) []byte {
	vs := e.core.FileVersions(fileID)
	return e.core.VersionBytes(fileID, vs[len(vs)-1].Number)
}
