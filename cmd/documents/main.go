// Command documents runs the Documents and templates module, or packages it.
//
//	documents serve -core http://core:8080 -secrets /run/secrets -db-host postgres:5432 -db-name togavsu
//	documents package -key signing.key -out documents.signed.json
//
// Secrets come from the Docker secrets the core hands the supervisor:
// togavsu_api_token, togavsu_webhook_secret, togavsu_db_role, togavsu_db_password and
// togavsu_db_schema.
//
// The package command runs the core's own install checks on the package (pkg/modulekit),
// refuses to sign a package they refuse, archives it, signs it with the base64 Ed25519
// seed in the key file and prints the PEM public key the admin trusts. It never makes or
// stores a key.
package main

import (
	"crypto/ed25519"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	if len(args) < 1 {
		log.Fatal("usage: documents serve|package [flags]")
	}
	switch args[0] {
	case "serve":
		serve(args[1:])
	case "package":
		pack(args[1:])
	default:
		log.Fatalf("unknown command %q", args[0])
	}
}

func secret(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		log.Fatalf("secret %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

func optional(dir, name, fallback string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return fallback
	}
	return strings.TrimSpace(string(b))
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "address to listen on")
	core := fs.String("core", "http://core:8080", "core base URL inside the stack")
	dir := fs.String("secrets", "/run/secrets", "directory of Docker secrets")
	host := fs.String("db-host", "postgres:5432", "Postgres host:port")
	name := fs.String("db-name", "togavsu", "database name")
	skew := fs.String("webhook-skew", "5m", "allowed webhook clock skew")
	_ = fs.Parse(args)
	schema := optional(*dir, "togavsu_db_schema", "mod_documents")
	u := url.URL{Scheme: "postgres", User: url.UserPassword(secret(*dir, "togavsu_db_role"), secret(*dir, "togavsu_db_password")),
		Host: *host, Path: "/" + *name, RawQuery: url.Values{"search_path": {schema}}.Encode()}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		log.Fatal(err)
	}
	skewDur, err := time.ParseDuration(*skew)
	if err != nil {
		log.Fatal(err)
	}
	srv := documents.New(documents.Config{Core: *core, Token: secret(*dir, "togavsu_api_token"),
		WebhookSecret: secret(*dir, "togavsu_webhook_secret"), DB: db, SkewSeconds: int(skewDur.Seconds()),
		Log: log.New(os.Stderr, "", log.LstdFlags)})
	log.Printf("documents listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, srv.Handler()))
}

func pack(args []string) {
	fs := flag.NewFlagSet("package", flag.ExitOnError)
	keyFile := fs.String("key", "", "file holding the author's base64 Ed25519 signing seed")
	out := fs.String("out", "documents.signed.json", "signed package file to write")
	_ = fs.Parse(args)
	if *keyFile == "" {
		log.Fatal("package: -key is required")
	}
	text, err := os.ReadFile(*keyFile)
	if err != nil {
		log.Fatal(err)
	}
	priv, err := modulekit.ParseSeed(string(text))
	if err != nil {
		log.Fatal(err)
	}
	files, err := documents.Files()
	if err != nil {
		log.Fatal(err)
	}
	signed, err := build(priv, files)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, signed, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s\n%s", *out, modulekit.PublicKeyPEM(priv.Public().(ed25519.PublicKey)))
}

// build runs the install checks on files, then archives and signs them.
func build(priv ed25519.PrivateKey, files map[string][]byte) ([]byte, error) {
	if _, _, err := modulekit.Check(files); err != nil {
		return nil, err
	}
	return modulekit.Build(priv, files)
}
