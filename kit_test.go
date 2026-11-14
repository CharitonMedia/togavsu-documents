package documents

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CharitonMedia/togavsu-documents/internal/pending"
	"github.com/CharitonMedia/togavsu/pkg/modulekit"
)

// files is the shipped package, read the way the kit reads a package directory.
func files(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := modulekit.ReadDir("package")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != len(embedded) {
		t.Fatalf("the package directory has %d files and the embedded package %d", len(f), len(embedded))
	}
	for k, v := range f {
		if string(embedded[k]) != string(v) {
			t.Fatalf("embedded %s differs from the package directory", k)
		}
	}
	return f
}

// installable is the shipped package without the pending fields: what the pinned core
// installs today.
func installable(t *testing.T) map[string][]byte {
	t.Helper()
	return pending.Strip(files(t))
}

// The package passes the core's own install checks once the pending fields are out
// (MOD-DOC-01's package half), and the shipped manifest is refused for those fields and
// nothing else: each field the checks refuse is refused alone, each scope refused only
// at approval passes them, and no field matches twice. When the core accepts a field
// this fails and names it, and the field leaves internal/pending.
func TestPackageCheck(t *testing.T) {
	f := files(t)
	pkg, ms, err := modulekit.Check(installable(t))
	if err != nil {
		t.Fatalf("check without the pending fields: %v", err)
	}
	if pkg.Manifest.ID != ID || pkg.Manifest.Version != Version || len(ms) != 6 {
		t.Fatalf("checked %s %s with %d migrations", pkg.Manifest.ID, pkg.Manifest.Version, len(ms))
	}
	if _, _, err := modulekit.Check(f); err == nil {
		t.Errorf("the core accepts the shipped manifest: every field in internal/pending has landed; empty pending.Fields")
	}
	for i := range pending.Fields {
		p := &pending.Fields[i]
		if n := len(p.Lines.FindAllString(string(f[pending.ManifestFile]), -1)); n != 1 {
			t.Errorf("%s (%s) matches %d places in the shipped manifest, want 1", p.Name, p.Item, n)
			continue
		}
		_, _, err := modulekit.Check(pending.StripBut(f, p))
		switch {
		case p.Refused == pending.AtCheck && err == nil:
			t.Errorf("the core now accepts the %s (%s): ship it and delete its row in internal/pending", p.Name, p.Item)
		case p.Refused == pending.AtCheck:
			t.Logf("%s (%s) refused: %v", p.Name, p.Item, err)
		case err != nil:
			t.Errorf("the %s (%s) is listed as refused at approval, but the install checks refuse it: %v", p.Name, p.Item, err)
		}
	}
}

// The installable package archived and signed: Check,
// CheckPackage on the installable tar, and VerifyFile on the signed file with a
// throwaway key. A tampered package or an untrusted signer is refused.
func TestBuildSignVerify(t *testing.T) {
	f := installable(t)
	if _, _, err := modulekit.Check(f); err != nil {
		t.Fatalf("check: %v", err)
	}
	raw, err := modulekit.Archive(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, ms, err := modulekit.CheckPackage(raw); err != nil || len(ms) != 6 {
		t.Fatalf("check package: %d migrations, %v", len(ms), err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := modulekit.Build(priv, f)
	if err != nil {
		t.Fatal(err)
	}
	v, ms, err := modulekit.VerifyFile(signed, []modulekit.TrustedKey{modulekit.Trust(pub)})
	if err != nil || v.ModuleID != ID || len(ms) != 6 {
		t.Fatalf("verify: %s %d %v", v.ModuleID, len(ms), err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if _, _, err := modulekit.VerifyFile(signed, []modulekit.TrustedKey{modulekit.Trust(other)}); modulekit.Code(err) != modulekit.CodeUnsigned {
		t.Fatalf("untrusted signer: %q %v", modulekit.Code(err), err)
	}
	f["migrations/0001_library.sql"] = append([]byte("-- changed\n"), f["migrations/0001_library.sql"]...)
	tampered, err := modulekit.Archive(f)
	if err != nil {
		t.Fatal(err)
	}
	sig := modulekit.Sign(priv, raw)
	if _, err := modulekit.Verify(tampered, modulekit.Digest(raw), sig, modulekit.KeyID(pub), []modulekit.TrustedKey{modulekit.Trust(pub)}); err == nil {
		t.Fatal("a changed package verified")
	}
	if !strings.Contains(modulekit.PublicKeyPEM(pub), "PUBLIC KEY") {
		t.Fatal("no PEM public key")
	}
}

// The module's Go source passes the core's settings source check (AC-030): no
// environment reads and no numeric configuration literal outside a setting.
func TestCheckSource(t *testing.T) {
	if err := modulekit.CheckSource("."); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := "package m\n\nimport \"os\"\n\nfunc f() string { return os.Getenv(\"X\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "m.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if modulekit.CheckSource(dir) == nil {
		t.Fatal("the check passed an environment read")
	}
}
