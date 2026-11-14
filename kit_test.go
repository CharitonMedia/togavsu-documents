package documents

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// installable is the shipped package as the pinned core installs it: modulekit.Installable
// holds back each part package/pending.yaml lists that the core refuses.
func installable(t *testing.T) (map[string][]byte, []modulekit.Held) {
	t.Helper()
	out, held, err := modulekit.Installable(files(t))
	if err != nil {
		t.Fatalf("package/pending.yaml doesn't cover every refusal of the pinned core: %v", err)
	}
	return out, held
}

// The package passes the core's own install checks once the pending parts the core
// refuses are held back (MOD-DOC-01's package half), and package/pending.yaml lists those
// parts and nothing else: a refusal it misses fails installable, and an entry that holds
// nothing back (its CORE item has landed) fails here and names the entry to delete.
func TestPackageCheck(t *testing.T) {
	f := files(t)
	out, held := installable(t)
	if _, ok := out[modulekit.PendingFile]; ok {
		t.Fatal("the installable package ships pending.yaml")
	}
	if refs := modulekit.CheckAll(out); len(refs) != 0 {
		t.Fatalf("the installable package is refused: %v", refs)
	}
	pkg, ms, err := modulekit.Check(out)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.ID != ID || pkg.Manifest.Version != Version || len(ms) != 6 {
		t.Fatalf("checked %s %s with %d migrations", pkg.Manifest.ID, pkg.Manifest.Version, len(ms))
	}
	entries, err := modulekit.ParsePending(f[modulekit.PendingFile])
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range modulekit.Unused(entries, held) {
		t.Errorf("pending %s (%s) holds nothing back: the core accepts it; delete its entry in package/pending.yaml", e.Part(), e.Core)
	}
	for _, h := range held {
		t.Log(h.String())
	}
}

// The installable package archived and signed: Check,
// CheckPackage on the installable tar, and VerifyFile on the signed file with a
// throwaway key. A tampered package or an untrusted signer is refused.
func TestBuildSignVerify(t *testing.T) {
	f, _ := installable(t)
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
