package documents

import (
	"crypto/ed25519"
	"regexp"
	"strings"
	"testing"

	"github.com/CharitonMedia/togavsu/pkg/modulekit"
)

// pendingCore is the package as the pinned core can install it today: the manifest
// without the fields and scopes whose CORE items haven't landed. The shipped manifest
// keeps them, as the spec's section 3 writes it:
//
//   - CORE-08: sidebar entries' section, icon and order.
//   - CORE-18: the firm:read scope.
//   - CORE-19: the users:directory scope.
func pendingCore(files map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range files {
		out[k] = v
	}
	m := string(out[modulekit.ManifestFile])
	for _, re := range pendingFields {
		m = re.ReplaceAllString(m, "")
	}
	out[modulekit.ManifestFile] = []byte(m)
	return out
}

var pendingFields = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^    (section|icon|order): .*\n`),
	regexp.MustCompile(`(?m)^  - scope: (firm:read|users:directory)\n    reason: .*\n`),
}

// pendingItems names the CORE items pendingCore stands in for.
const pendingItems = "needs CORE-08 (sidebar section, icon, order), CORE-18 (firm:read) and CORE-19 (users:directory)"

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

func installable(t *testing.T) map[string][]byte {
	t.Helper()
	f := files(t)
	if _, _, err := modulekit.Check(f); err == nil {
		return f
	}
	return pendingCore(f)
}

// The package passes the core's own install checks (MOD-DOC-01's package half).
func TestPackageCheck(t *testing.T) {
	f := files(t)
	pkg, ms, err := modulekit.Check(f)
	if err != nil {
		if _, _, perr := modulekit.Check(pendingCore(f)); perr != nil {
			t.Fatalf("check: %v; without the pending fields: %v", err, perr)
		}
		t.Skipf("%s: %v", pendingItems, err)
	}
	if pkg.Manifest.ID != ID || pkg.Manifest.Version != Version || len(ms) != 6 {
		t.Fatalf("checked %s %s with %d migrations", pkg.Manifest.ID, pkg.Manifest.Version, len(ms))
	}
}

// Everything but the pending fields passes today, archived and signed: Check,
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
