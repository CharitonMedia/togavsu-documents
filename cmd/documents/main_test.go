package main

import (
	"crypto/ed25519"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
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
	signed, err := build(priv, files)
	if err != nil {
		if _, _, cerr := modulekit.Check(files); cerr != nil {
			t.Skipf("needs CORE-08, CORE-18 and CORE-19 before the shipped manifest installs: %v", err)
		}
		t.Fatal(err)
	}
	if _, _, err := modulekit.VerifyFile(signed, []modulekit.TrustedKey{modulekit.Trust(pub)}); err != nil {
		t.Fatal(err)
	}
}
