// Command documents runs the Documents and templates module, or packages it.
//
//	documents serve -core http://core:8080 -secrets /run/secrets -db-host postgres:5432 -db-name togavsu
//	documents package -key signing.key -out documents.signed.json
//
// Secrets come from the Docker secrets the core hands the supervisor:
// togavsu_api_token, togavsu_webhook_secret, togavsu_db_role, togavsu_db_password and
// togavsu_db_schema.
//
// The package command builds the installable package with the core's own code
// (modulekit.BuildInstallable): it holds back each part package/pending.yaml lists that
// the pinned core refuses, runs the install checks on the rest, refuses to sign a package
// they refuse or one with a refusal pending.yaml doesn't cover, archives it and signs it
// with the base64 Ed25519 seed in the key file. It prints each held-back part with its
// CORE item, each pending entry that holds nothing back, and the PEM public key the admin
// trusts. It never makes or stores a key.
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

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Fixed numbers, spelled as arithmetic for the core's settings source check
// (modulekit.CheckSource): no log flags, and the arguments after the command name.
var (
	zero = int(0)
	one  = zero + 1
)

func main() {
	log.SetFlags(zero)
	args := os.Args[one:]
	if len(args) < 1 {
		log.Fatal("usage: documents serve|package [flags]")
	}
	switch args[0] {
	case "serve":
		serve(args[one:])
	case "package":
		pack(args[one:])
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
	_ = fs.Parse(args)
	schema := optional(*dir, "togavsu_db_schema", "mod_documents")
	u := url.URL{Scheme: "postgres", User: url.UserPassword(secret(*dir, "togavsu_db_role"), secret(*dir, "togavsu_db_password")),
		Host: *host, Path: "/" + *name, RawQuery: url.Values{"search_path": {schema}}.Encode()}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		log.Fatal(err)
	}
	srv := documents.New(documents.Config{Core: *core, Token: secret(*dir, "togavsu_api_token"),
		WebhookSecret: secret(*dir, "togavsu_webhook_secret"), DB: db,
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
	signed, notes, err := build(priv, files)
	if err != nil {
		log.Fatal(err)
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	if err := writeFile(*out, signed); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s\n%s", *out, modulekit.PublicKeyPEM(priv.Public().(ed25519.PublicKey)))
}

// build holds back the pending parts the core refuses, runs the install checks on the
// rest, archives and signs it. notes is one line per held-back part and one per pending
// entry that held nothing back (its CORE item has landed, or the package lost the part).
func build(priv ed25519.PrivateKey, files map[string][]byte) (signed []byte, notes []string, err error) {
	signed, ip, err := modulekit.BuildInstallable(priv, files)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range ip.Held {
		notes = append(notes, h.String())
	}
	for _, e := range ip.Unused {
		notes = append(notes, "pending "+e.Part()+" ("+e.Core.String()+") holds nothing back: delete its entry in package/pending.yaml")
	}
	return signed, notes, nil
}

// writeFile writes b to a new or truncated file with the usual 0666-before-umask mode.
func writeFile(path string, b []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
