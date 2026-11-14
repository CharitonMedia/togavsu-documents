package documents

// Fixed numbers, spelled as arithmetic: the convention of the core's example module
// matter-clock, so the core's settings source check (modulekit.CheckSource, AC-030) can
// tell a fixed rule from a tunable. Each is a rule of the spec or of a format the module
// reads or writes. A value a firm might change is a module setting in
// package/togavsu-module.yaml, never one of these.
var (
	zero  = int(0)
	one   = zero + 1
	two   = one + one
	three = two + one
	five  = two + three
	ten   = two * five
)

// Positions in a regexp match: the first group of FindStringSubmatch, and the end of
// the match and the bounds of the first group in FindStringSubmatchIndex.
var (
	firstGroup = one
	matchEnd   = one
	groupStart = two
	groupEnd   = three
)

var (
	// widgetRows: the widget shows the top eight drafts (spec, Drafts).
	widgetRows = two * two * two
	// webhookSkewSeconds: an event's timestamp must be within five minutes of the
	// module's clock (spec, Events).
	webhookSkewSeconds = five * ten * (three + three)
	// slugMax: a template or component slug keeps at most sixty characters.
	slugMax = (three + three) * ten
	// zipPathMax: CC-5 keeps an archive entry path within 255 bytes.
	zipPathMax = one<<(two*two*two) - one
	// float64Bits: the bit size strconv formats a JSON number with.
	float64Bits = two << five
)

// How many runs each run list shows, newest first: the runs API, the matter page and
// the recent runs page.
var (
	runsAPIRows    = five * ten
	runsMatterRows = ten
	runsRecentRows = five
)
