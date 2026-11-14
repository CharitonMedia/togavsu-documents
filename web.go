package documents

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
)

// The web layer: who is looking, how input is read, and how answers are written. Every
// screen action is a JSON endpoint under /api/ (served at /m/documents/api/), and the
// screens' forms post to those same endpoints with a _return page, so an agent with a
// context token reaches exactly what a person reaches.

// viewer is the person (or agent) the core's context token names.
type viewer struct {
	UserID   string
	Roles    []string
	MatterID string
	Token    string
}

func (v viewer) has(roles ...string) bool {
	for _, have := range v.Roles {
		for _, want := range roles {
			if have == want {
				return true
			}
		}
	}
	return false
}

// firm is a firm user the module serves: admin, attorney or staff. Accountant and
// client viewers get 404 on every page and endpoint.
func (v viewer) firm() bool { return v.has("admin", "attorney", "staff") }

func (v viewer) admin() bool { return v.has("admin") }

// contextToken reads the frame's context token: the ?context= the core puts on every
// frame, a form field, or the header an agent sends.
func contextToken(r *http.Request) string {
	if t := r.URL.Query().Get("context"); t != "" {
		return t
	}
	if t := r.Header.Get(modkit.ContextHeader); t != "" {
		return t
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := parseForm(r); err == nil {
			return r.FormValue("context")
		}
	}
	return ""
}

func parseForm(r *http.Request) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if r.MultipartForm != nil {
			return nil
		}
		return r.ParseMultipartForm(32 << 20)
	}
	return r.ParseForm()
}

// errNoViewer is a request with no valid context token.
var errNoViewer = errors.New("sign in through Togavsu")

func (s *Server) viewer(r *http.Request) (viewer, error) {
	tok := contextToken(r)
	if tok == "" {
		return viewer{}, errNoViewer
	}
	var out struct {
		UserID   string   `json:"viewer_user_id"`
		Roles    []string `json:"roles"`
		MatterID *string  `json:"matter_id"`
	}
	if err := s.call(r.Context(), http.MethodPost, "/api/v1/modules/"+ID+"/context-tokens/verify", tok, nil, &out); err != nil {
		return viewer{}, errNoViewer
	}
	v := viewer{UserID: out.UserID, Roles: out.Roles, Token: tok}
	if out.MatterID != nil {
		v.MatterID = *out.MatterID
	}
	return v, nil
}

// inputError is a refusal the module itself decides, with its HTTP status.
type inputError struct {
	Status int
	Code   string
	Msg    string
	Extra  map[string]any
}

func (e *inputError) Error() string { return e.Msg }

func notFound() error {
	return &inputError{Status: http.StatusNotFound, Code: "not_found", Msg: "not found"}
}

func unprocessable(msg string, extra map[string]any) error {
	return &inputError{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Msg: msg, Extra: extra}
}

func conflict(msg string) error {
	return &inputError{Status: http.StatusConflict, Code: "conflict", Msg: msg}
}

// statusOf maps an error to the status and body the module answers with. A core 404 is
// the module's 404: the viewer can't see that record.
func statusOf(err error) (int, map[string]any) {
	var ie *inputError
	if errors.As(err, &ie) {
		body := map[string]any{"code": ie.Code, "message": ie.Msg}
		for k, v := range ie.Extra {
			body[k] = v
		}
		return ie.Status, body
	}
	var ce *coreError
	if errors.As(err, &ce) {
		switch ce.Status {
		case http.StatusNotFound, http.StatusForbidden:
			return http.StatusNotFound, map[string]any{"code": "not_found", "message": "not found"}
		case http.StatusUnprocessableEntity, http.StatusConflict:
			return ce.Status, map[string]any{"code": ce.Code, "message": ce.Detail}
		}
	}
	if errors.Is(err, errNoViewer) {
		return http.StatusUnauthorized, map[string]any{"code": "unauthenticated", "message": errNoViewer.Error()}
	}
	return http.StatusBadGateway, map[string]any{"code": "core_unavailable", "message": "Documents can't reach the core right now"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// wantsPage reports whether a form post asked to go back to a page.
func wantsPage(r *http.Request) string {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") && !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return ""
	}
	if err := parseForm(r); err != nil {
		return ""
	}
	ret := r.FormValue("_return")
	if !strings.HasPrefix(ret, "/") || strings.HasPrefix(ret, "//") || strings.Contains(ret, "..") {
		return ""
	}
	return ret
}

// fail answers an error: a page for a form post, JSON otherwise. The message names what
// was refused, never a value a person typed.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, body := statusOf(err)
	if status == http.StatusBadGateway {
		s.c.Log.Printf("documents: %s %s: %v", r.Method, r.URL.Path, err)
	}
	if wantsPage(r) != "" || (r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/")) {
		s.render(w, status, "error", map[string]any{"Status": status, "Body": body, "Context": contextToken(r)})
		return
	}
	writeJSON(w, status, body)
}

// done answers a successful write: back to the page a form named, or JSON.
func (s *Server) done(w http.ResponseWriter, r *http.Request, status int, v any) {
	if ret := wantsPage(r); ret != "" {
		u, err := url.Parse(ret)
		if err == nil {
			q := u.Query()
			q.Set("context", contextToken(r))
			u.RawQuery = q.Encode()
			http.Redirect(w, r, Prefix+u.String(), http.StatusSeeOther)
			return
		}
	}
	writeJSON(w, status, v)
}

// guard verifies the viewer and refuses roles the module doesn't serve with 404.
func (s *Server) guard(admin bool, h func(http.ResponseWriter, *http.Request, viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.viewer(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !v.firm() || (admin && !v.admin()) {
			s.fail(w, r, notFound())
			return
		}
		h(w, r, v)
	}
}

// bind reads a JSON body, or form fields by their json names.
func bind(r *http.Request, out any) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "application/json" {
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return unprocessable("the request body is not valid JSON", nil)
		}
		return nil
	}
	if err := parseForm(r); err != nil {
		return unprocessable("the form could not be read", nil)
	}
	rv := reflect.ValueOf(out).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		vals, ok := r.Form[name]
		if !ok {
			continue
		}
		fv := rv.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(vals[0])
		case reflect.Bool:
			fv.SetBool(vals[len(vals)-1] == "true" || vals[len(vals)-1] == "on" || vals[len(vals)-1] == "1")
		case reflect.Int:
			n, err := strconv.Atoi(vals[0])
			if err != nil {
				return unprocessable(name+" must be a whole number", nil)
			}
			fv.SetInt(int64(n))
		case reflect.Slice:
			if fv.Type().Elem().Kind() == reflect.String {
				var list []string
				for _, v := range vals {
					for _, p := range strings.Split(v, ",") {
						if p = strings.TrimSpace(p); p != "" {
							list = append(list, p)
						}
					}
				}
				fv.Set(reflect.ValueOf(list))
			}
		case reflect.Map:
			if fv.Type().Key().Kind() == reflect.String && fv.Type().Elem().Kind() == reflect.String {
				m := map[string]string{}
				for k, vs := range r.Form {
					if key, ok := strings.CutPrefix(k, name+"."); ok {
						m[key] = vs[0]
					}
				}
				fv.Set(reflect.ValueOf(m))
			}
		}
	}
	// Map fields named like values.school_name, even when no plain field was sent.
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		fv := rv.Field(i)
		if fv.Kind() != reflect.Map || !fv.IsNil() || name == "" {
			continue
		}
		m := map[string]string{}
		for k, vs := range r.Form {
			if key, ok := strings.CutPrefix(k, name+"."); ok {
				m[key] = vs[0]
			}
		}
		if len(m) > 0 {
			fv.Set(reflect.ValueOf(m))
		}
	}
	return nil
}

// Pages.

var pages = template.Must(template.New("pages").Funcs(template.FuncMap{
	"link": func(ctx, p string, kv ...string) string {
		q := url.Values{"context": {ctx}}
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] != "" {
				q.Set(kv[i], kv[i+1])
			}
		}
		return Prefix + p + "?" + q.Encode()
	},
	"prefix":    func() string { return Prefix },
	"list":      func(v ...string) []string { return v },
	"runs":      func(ctx string, rs []Run) map[string]any { return map[string]any{"Context": ctx, "Runs": rs} },
	"date":      func(unix int64) string { return time.Unix(unix, 0).UTC().Format("Jan 2, 2006 15:04 UTC") },
	"draftsTab": draftsTabLink,
	"fields": func(g grants, areas []area, cats []docCategory, t any) map[string]any {
		return map[string]any{"Grants": g, "Areas": areas, "Categories": cats, "T": t}
	},
	"join": strings.Join,
	"has": func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	},
}).ParseFS(pagesFS, "pages/*.html"))

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, name, data); err != nil {
		s.c.Log.Printf("documents: page %s: %v", name, err)
		http.Error(w, "the page could not be drawn", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// static serves the one stylesheet and the one script; no external assets.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.PathValue("name"))
	b, err := pagesFS.ReadFile("pages/" + name)
	if err != nil || (name != "style.css" && name != "app.js") {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	_, _ = w.Write(b)
}

// pageData is what every page template gets.
type pageData struct {
	Title   string
	Context string
	Viewer  viewer
	Matter  *coreMatter
	Grants  grants
	Data    any
	Message string
}
