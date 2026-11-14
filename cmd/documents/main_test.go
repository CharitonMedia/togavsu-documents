package main

import (
	"crypto/ed25519"
	"strings"
	"testing"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
)

// The package command's build, with a key made at test time: it always signs the shipped
// package (pending parts held back), the signature verifies, and the signed files pass
// the core's install checks with no refusal. It fails when package/pending.yaml misses a
// refusal (BuildInstallable refuses) or holds an entry that holds nothing back.
func TestBuild(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := documents.Files()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := build(priv, map[string][]byte{modulekit.ManifestFile: []byte("id: documents\n")}); err == nil {
		t.Fatal("an incomplete manifest was signed")
	}
	signed, notes, err := build(priv, files)
	if err != nil {
		t.Fatalf("build with a valid key: %v", err)
	}
	for _, n := range notes {
		if strings.HasPrefix(n, "pending ") {
			t.Errorf("%s", n)
		} else {
			t.Log(n)
		}
	}
	v, ms, err := modulekit.VerifyFile(signed, []modulekit.TrustedKey{modulekit.Trust(pub)})
	if err != nil {
		t.Fatal(err)
	}
	if v.ModuleID != documents.ID || v.Version != documents.Version || len(ms) != 6 {
		t.Fatalf("verified %s %s with %d migrations", v.ModuleID, v.Version, len(ms))
	}
	pkg, err := modulekit.ReadPackage(v.Package)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pkg.Files[modulekit.PendingFile]; ok {
		t.Fatal("the signed package ships pending.yaml")
	}
	if refs := modulekit.CheckAll(pkg.Files); len(refs) != 0 {
		t.Fatalf("the signed files are refused: %v", refs)
	}
}
