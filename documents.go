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
	"errors"
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

// Packager packages and signs the module for the marketplace. The core verifies
// packages with its own internal code (internal/modules and internal/marketplace), which
// a standalone module can't import, and a copy would drift from what the core checks. So
// packaging and signing go through this adapter, and the default is a stub that refuses
// until the core publishes a public packaging and signing package or CLI.
type Packager interface {
	// Package turns the package files (manifest and migrations by path) into the
	// marketplace's signed package file, signed with the author key at keyFile.
	Package(files map[string][]byte, keyFile string) ([]byte, error)
}

// ErrPackagingUnavailable is the stub's answer.
var ErrPackagingUnavailable = errors.New("documents: packaging and signing wait on a public packaging and signing package or CLI from the core")

type stubPackager struct{}

func (stubPackager) Package(map[string][]byte, string) ([]byte, error) {
	return nil, ErrPackagingUnavailable
}

// DefaultPackager is the packager the package command uses.
var DefaultPackager Packager = stubPackager{}

// Build packages and signs the module through p (DefaultPackager when nil). edit, when
// set, changes the files first.
func Build(p Packager, keyFile string, edit func(map[string][]byte)) ([]byte, error) {
	files, err := Files()
	if err != nil {
		return nil, err
	}
	if edit != nil {
		edit(files)
	}
	if p == nil {
		p = DefaultPackager
	}
	return p.Package(files, keyFile)
}

// Config is what the container gets: the core's address, its secrets and its own DB.
type Config struct {
	Core          string
	Token         string
	WebhookSecret string
	DB            *sql.DB
	HTTP          *http.Client
	Now           func() time.Time
	// SkewSeconds is how far a webhook timestamp may be from the module's clock.
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
		c.Log = log.New(io.Discard, "", 0)
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
