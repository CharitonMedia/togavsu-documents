// Package pending is the one list of manifest fields the shipped package carries for
// CORE items the pinned core lacks. The shipped manifest keeps them, as the spec's
// section 3 writes it; tests check, sign and install a copy without them. When the core
// accepts a field, a test fails and names it: TestPackageCheck for a field the install
// checks refuse, the acceptance suite's MOD-DOC-01 for a scope the core refuses when the
// admin approves it. Then delete its row here.
package pending

import (
	"regexp"
	"strings"
)

// ManifestFile is the manifest's path in the package.
const ManifestFile = "togavsu-module.yaml"

// Where the pinned core refuses a field.
const (
	AtCheck    = "check"    // the install checks (modulekit.Check, togavsu-module check)
	AtApproval = "approval" // the admin's approval ("unknown scope")
)

// Field is one manifest field waiting on a CORE item.
type Field struct {
	Item    string         // the CORE item that adds it
	Name    string         // what it is
	Refused string         // AtCheck or AtApproval
	Lines   *regexp.Regexp // its lines in the shipped manifest, matched exactly once
}

// Fields are the pending fields.
var Fields = []Field{
	{"CORE-08", "sidebar section", AtCheck, regexp.MustCompile(`(?m)^    section: .*\n`)},
	{"CORE-08", "sidebar icon", AtCheck, regexp.MustCompile(`(?m)^    icon: .*\n`)},
	{"CORE-08", "sidebar order", AtCheck, regexp.MustCompile(`(?m)^    order: .*\n`)},
	{"CORE-18", "firm:read scope", AtApproval, regexp.MustCompile(`(?m)^  - scope: firm:read\n    reason: .*\n`)},
	{"CORE-19", "users:directory scope", AtApproval, regexp.MustCompile(`(?m)^  - scope: users:directory\n    reason: .*\n`)},
}

// Items names every pending field with its CORE item.
func Items() string {
	var out []string
	for _, f := range Fields {
		out = append(out, f.Item+" ("+f.Name+")")
	}
	return "needs " + strings.Join(out, ", ")
}

// Strip returns a copy of the package files with every pending field taken out of the
// manifest.
func Strip(files map[string][]byte) map[string][]byte { return StripBut(files, nil) }

// StripBut is Strip keeping the field keep, when keep is one of Fields.
func StripBut(files map[string][]byte, keep *Field) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range files {
		out[k] = v
	}
	m := string(out[ManifestFile])
	for i := range Fields {
		if &Fields[i] != keep {
			m = Fields[i].Lines.ReplaceAllString(m, "")
		}
	}
	out[ManifestFile] = []byte(m)
	return out
}
