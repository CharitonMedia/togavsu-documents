package acceptance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// The acceptance suite on the real composed core (pkg/modulekit/coretest). The module is
// installed from its signed package, approved on the admin's Modules tab and served in
// this process from the deployment's own token, webhook secret and database login.
//
// A criterion whose core item hasn't landed in the pinned core is written in full and
// skipped with the item it needs. The skip is decided by asking the running core (its
// OpenAPI document, or the install checks) whether the item is there, so each criterion
// runs on its own once module-core lands the item and the pin moves.

type kit struct {
	*coretest.Core
	t      *testing.T
	logs   *syncBuffer
	api    map[string]any
	signed []byte
	priv   ed25519.PrivateKey
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newKit starts the core, serves the module and builds its signed package with a
// throwaway key, without installing it.
func newKit(t *testing.T) *kit {
	t.Helper()
	c := coretest.New(t)
	k := &kit{Core: c, t: t, logs: &syncBuffer{}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("module log:\n%s", k.logs.String())
		}
	})
	c.Serve(documents.ID, func(m coretest.Module) http.Handler {
		return documents.New(documents.Config{Core: m.CoreURL, Token: m.Token, WebhookSecret: m.WebhookSecret, DB: m.DB,
			SkewSeconds: 300, Log: log.New(k.logs, "", 0)}).Handler()
	})
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	k.priv = priv
	k.signed, _, err = modulekit.BuildInstallable(priv, shipped(t))
	if err != nil {
		t.Fatal(err)
	}
	c.TrustKey("Documents test key", pub)
	r := c.NewClient().Do("GET", "/api/v1/openapi.json", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("openapi: %d", r.Code)
	}
	if err := json.Unmarshal([]byte(r.Body), &k.api); err != nil {
		t.Fatal(err)
	}
	return k
}

// installed is newKit with the module installed and every permission approved.
func installed(t *testing.T) *kit {
	t.Helper()
	k := newKit(t)
	k.Install(k.signed)
	k.ApproveAll(documents.ID)
	return k
}

// has reports whether the core serves the operation at method and path.
func (k *kit) has(method, path string) bool {
	paths, _ := k.api["paths"].(map[string]any)
	ops, _ := paths[path].(map[string]any)
	_, ok := ops[strings.ToLower(method)]
	return ok
}

// field reports whether the request body of the operation names field.
func (k *kit) field(method, path, field string) bool {
	paths, _ := k.api["paths"].(map[string]any)
	ops, _ := paths[path].(map[string]any)
	op, _ := ops[strings.ToLower(method)].(map[string]any)
	raw, _ := json.Marshal(op["requestBody"])
	if strings.Contains(string(raw), `"`+field+`"`) {
		return true
	}
	// A body schema by reference: look the component up.
	var ref struct {
		Content map[string]struct {
			Schema struct {
				Ref string `json:"$ref"`
			} `json:"schema"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &ref)
	for _, c := range ref.Content {
		name := c.Schema.Ref[strings.LastIndex(c.Schema.Ref, "/")+1:]
		comps, _ := k.api["components"].(map[string]any)
		schemas, _ := comps["schemas"].(map[string]any)
		s, _ := json.Marshal(schemas[name])
		if strings.Contains(string(s), `"`+field+`"`) {
			return true
		}
	}
	return false
}

// needs skips the test unless the pinned core has every CORE item named.
func (k *kit) needs(items ...string) {
	k.t.Helper()
	for _, item := range items {
		if !k.landed(item) {
			k.t.Skip("needs " + item)
		}
	}
}

func (k *kit) landed(item string) bool {
	switch item {
	case "CORE-08":
		// The sidebar fields pending.yaml lists for CORE-08 install once nothing holds
		// them back.
		_, held := installable(k.t)
		for _, h := range held {
			if h.Pending.Core == item {
				return false
			}
		}
		return true
	case "CORE-18":
		return k.has("GET", "/api/v1/firm-profile")
	case "CORE-19":
		return k.has("GET", "/api/v1/user-directory")
	case "CORE-17":
		return k.field("POST", "/api/v1/file-uploads", "library")
	case "CORE-20":
		return k.delegatedSettings()
	case "CORE-21":
		return k.field("POST", "/api/v1/file-archives", "entries")
	}
	// CORE-09 (the frame bridge) and anything else this suite can't detect yet.
	return false
}

// delegatedSettings is CORE-20: the module reads practice areas as a viewer call.
func (k *kit) delegatedSettings() bool {
	k.t.Helper()
	tok := k.ctx(coretest.Staff, "")
	if tok == "" {
		return false
	}
	r := k.ModuleClient(documents.ID).Do("GET", "/api/v1/practice-areas", nil, coretest.ContextHeader, tok)
	return r.Code == http.StatusOK
}

// shipped is the package as the repository ships it.
func shipped(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := documents.Files()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// installable is the shipped package as the pinned core installs it, without the parts
// package/pending.yaml lists that the core refuses, and the parts it held back.
func installable(t *testing.T) (map[string][]byte, []modulekit.Held) {
	t.Helper()
	out, held, err := modulekit.Installable(shipped(t))
	if err != nil {
		t.Fatal(err)
	}
	return out, held
}

// --- Calling the module as a viewer, through the core's /m/ route ---

// ctx is a context token for who on the module (matter for a matter screen), or "" when
// the core issues none to who.
func (k *kit) ctx(who, matter string) string {
	k.t.Helper()
	body := map[string]any{}
	if matter != "" {
		body["matter_id"] = matter
	}
	r := k.As(who).Do("POST", "/api/v1/modules/"+documents.ID+"/context-tokens", body)
	if r.Code != http.StatusCreated {
		return ""
	}
	tok, _ := r.JSON["token"].(string)
	return tok
}

// form is a multipart body: fields and at most one file in the field "file".
type form struct {
	fields   map[string]string
	fileName string
	data     []byte
}

// at adds the context token to a module path.
func at(path, tok string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return "/m/" + documents.ID + path + sep + "context=" + url.QueryEscape(tok)
}

// do calls the module's page or endpoint at path as who, the way the core's frame does:
// the request goes to the core's /m/documents/ route with the context token on the URL.
// body is nil, JSON, url.Values or *form.
func (k *kit) do(who, method, path, matter string, body any) coretest.Resp {
	k.t.Helper()
	return k.doTok(k.As(who), k.ctx(who, matter), method, path, body)
}

func (k *kit) doTok(u *coretest.User, tok, method, path string, body any) coretest.Resp {
	k.t.Helper()
	p := at(path, tok)
	if f, ok := body.(*form); ok {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for key, v := range f.fields {
			_ = w.WriteField(key, v)
		}
		if f.fileName != "" {
			fw, _ := w.CreateFormFile("file", f.fileName)
			_, _ = fw.Write(f.data)
		}
		_ = w.Close()
		return u.Raw(method, p, buf.Bytes(), "Content-Type", w.FormDataContentType(), "X-CSRF-Token", u.CSRF)
	}
	return u.Do(method, p, body)
}

// want fails the test unless r has status code.
func (k *kit) want(r coretest.Resp, code int) coretest.Resp {
	k.t.Helper()
	if r.Code != code {
		k.t.Fatalf("status %d, want %d: %s", r.Code, code, r.Body)
	}
	return r
}

// decode reads r's JSON body into v.
func (k *kit) decode(r coretest.Resp, v any) {
	k.t.Helper()
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		k.t.Fatalf("not JSON (%d): %s", r.Code, r.Body)
	}
}

// --- Core records, made through the core API as a person would ---

// upload stages data as who and returns the upload id (matter is the file's matter, or
// "" for a new version's upload).
func (k *kit) upload(who, matter, name string, data []byte) string {
	k.t.Helper()
	sum := sha256.Sum256(data)
	body := map[string]any{"file_name": name, "size_bytes": len(data), "sha256": hex.EncodeToString(sum[:]), "media_type": mediaType(name)}
	if matter != "" {
		body["matter_id"] = matter
	}
	u := k.As(who)
	up := u.Must(http.StatusCreated, "POST", "/api/v1/file-uploads", body)
	id, _ := up.JSON["id"].(string)
	r := u.Raw("PUT", "/api/v1/file-uploads/"+id, data, "Content-Type", "application/octet-stream",
		"Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)), "X-CSRF-Token", u.CSRF)
	if r.Code != http.StatusOK {
		k.t.Fatalf("upload chunk: %d %s", r.Code, r.Body)
	}
	u.Must(http.StatusOK, "POST", "/api/v1/file-uploads/"+id+"/complete", nil)
	return id
}

// file publishes a file on matter as who and returns its id. extra goes on the publish
// body (attorney_only, say).
func (k *kit) file(who, matter, name string, data []byte, extra map[string]any) string {
	k.t.Helper()
	body := map[string]any{"upload_id": k.upload(who, matter, name, data), "matter_id": matter, "name": name}
	for key, v := range extra {
		body[key] = v
	}
	r := k.As(who).Must(http.StatusCreated, "POST", "/api/v1/files", body)
	id, _ := r.JSON["id"].(string)
	return id
}

// version adds a version to a file as who, as an editor save does.
func (k *kit) version(who, fileID, matter, name string, data []byte) {
	k.t.Helper()
	k.As(who).Must(http.StatusCreated, "POST", "/api/v1/files/"+fileID+"/versions", map[string]any{"upload_id": k.upload(who, matter, name, data)})
}

// coreJSON is a core read as who, decoded.
func (k *kit) coreJSON(who, path string, v any) coretest.Resp {
	k.t.Helper()
	r := k.As(who).Do("GET", path, nil)
	if v != nil && r.Code == http.StatusOK {
		k.decode(r, v)
	}
	return r
}

func mediaType(name string) string {
	switch {
	case strings.HasSuffix(name, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case strings.HasSuffix(name, ".pdf"):
		return "application/pdf"
	}
	return "text/plain"
}

// moduleCount runs a counting query on the module's own schema as its own login.
func (k *kit) moduleCount(q string, args ...any) int {
	k.t.Helper()
	var n int
	if err := k.ModuleDB(documents.ID).QueryRow(q, args...).Scan(&n); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
	return n
}
