// Package documents is the Documents and templates module for Togavsu: the firm's
// template library, token-filled drafts and whole document packages on a matter, matter
// zips, and version notes, compare and copy on top of the core's file versions.
//
// The module runs in its own container. It reaches core records only through the core
// API with its scoped token and the viewer's context token, so walls, roles and
// attorney-only rules come from the core. It keeps its own rows in its own schema
// (mod_documents) through its own database login, and never keeps file bytes: every
// template body, component body and generated output is a core file.
package documents

import (
	"database/sql"
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

// ID is the module id from the manifest.
const ID = "documents"

// Version is the module version from the manifest.
const Version = "1.0.0"

// Prefix is where the core serves the module's pages (PRD 9.4).
const Prefix = "/m/" + ID

//go:embed package
var packageFS embed.FS

//go:embed pages
var pagesFS embed.FS

// Files returns the package files (manifest and migrations) by package path.
func Files() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(packageFS, "package", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := packageFS.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, "package/")] = b
		return nil
	})
	return out, err
}

// Config is what the container gets: the core's address, its secrets and its own DB.
type Config struct {
	Core          string
	Token         string
	WebhookSecret string
	DB            *sql.DB
	HTTP          *http.Client
	Now           func() time.Time
	// SkewSeconds is how far a webhook timestamp may be from the module's clock; zero
	// means the spec's five minutes.
	SkewSeconds int
	// Log receives the module's log lines. It never gets a typed token value.
	Log *log.Logger
}

// Server is the module's HTTP server.
type Server struct {
	c Config
}

// New returns the server.
func New(c Config) *Server {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = log.New(io.Discard, "", zero)
	}
	return &Server{c: c}
}

// Handler serves every page, JSON endpoint and the event webhook.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	for _, rt := range s.routes() {
		m.HandleFunc(rt.Method+" "+rt.Path, rt.handler)
	}
	m.HandleFunc("POST /events", s.events)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	m.HandleFunc("GET /static/{name}", s.static)
	return m
}
