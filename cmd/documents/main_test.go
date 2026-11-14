package main

import (
	"crypto/ed25519"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu-documents/internal/pending"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
)

// build refuses what the core's install checks refuse, and its output verifies.
func TestBuild(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := documents.Files()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := build(priv, map[string][]byte{"togavsu-module.yaml": []byte("id: documents\n")}); err == nil {
		t.Fatal("an incomplete manifest was signed")
	}
	if _, err := build(priv, files); err == nil {
		t.Fatalf("the shipped manifest signed, so the core now accepts every pending field; empty pending.Fields")
	}
	signed, err := build(priv, pending.Strip(files))
	if err != nil {
		t.Fatalf("%s; without them: %v", pending.Items(), err)
	}
	if _, _, err := modulekit.VerifyFile(signed, []modulekit.TrustedKey{modulekit.Trust(pub)}); err != nil {
		t.Fatal(err)
	}
}
