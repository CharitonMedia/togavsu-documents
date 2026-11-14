// Package pending is the module package as the pinned core can install it today: the
// manifest without the fields and scopes whose CORE items haven't landed. The shipped
// manifest keeps them, as the spec's section 3 writes it. Only tests use it.
package pending

import "regexp"

// Items names the CORE items Strip stands in for.
const Items = "needs CORE-08 (sidebar section, icon, order), CORE-18 (firm:read) and CORE-19 (users:directory)"

const manifestFile = "togavsu-module.yaml"

var fields = []*regexp.Regexp{
	// CORE-08: sidebar entries' section, icon and order.
	regexp.MustCompile(`(?m)^    (section|icon|order): .*\n`),
	// CORE-18 and CORE-19: the firm:read and users:directory scopes.
	regexp.MustCompile(`(?m)^  - scope: (firm:read|users:directory)\n    reason: .*\n`),
}

// Strip returns a copy of the package files with those fields taken out of the manifest.
func Strip(files map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range files {
		out[k] = v
	}
	m := string(out[manifestFile])
	for _, re := range fields {
		m = re.ReplaceAllString(m, "")
	}
	out[manifestFile] = []byte(m)
	return out
}
