package documents

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// DOCX handling with the standard library only. A DOCX is a zip of XML parts; the module
// fills tokens by mechanical text replacement in word/document.xml and the header and
// footer parts, and copies a component's paragraphs and tables into a slot paragraph.
// Nothing here is an editing surface: further edits happen in LawCraft.

const docxMedia = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

const documentPart = "word/document.xml"

// Refusal reasons for a file that isn't a DOCX.
var (
	errNotDOCX     = errors.New("only DOCX files can be templates or components; open a DOC, ODT or RTF in the editor, save it, and save that as a template")
	errNoDocument  = errors.New("the file is not a DOCX: it has no word/document.xml")
	errUnreadable  = errors.New("the file is not a DOCX: it is not a readable zip")
	errSlotNotLone = errors.New("a slot must be alone in its paragraph")
)

// partError names a part a component can't hold, because a slot copies only paragraphs
// and tables and would leave its relationship behind.
type partError struct{ Part string }

func (e *partError) Error() string {
	return "the component holds a " + e.Part + ", which a slot can't copy"
}

type docx struct {
	names []string
	parts map[string][]byte
}

// readDOCX opens a DOCX held in memory.
func readDOCX(b []byte) (*docx, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, errUnreadable
	}
	d := &docx{parts: map[string][]byte{}}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, errUnreadable
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, errUnreadable
		}
		if _, dup := d.parts[f.Name]; !dup {
			d.names = append(d.names, f.Name)
		}
		d.parts[f.Name] = body
	}
	if _, ok := d.parts[documentPart]; !ok {
		return nil, errNoDocument
	}
	return d, nil
}

// checkUpload refuses anything but a DOCX: the name must end .docx and the bytes must be
// a zip holding word/document.xml.
func checkUpload(name string, b []byte) (*docx, error) {
	if !strings.HasSuffix(strings.ToLower(name), ".docx") {
		return nil, errNotDOCX
	}
	return readDOCX(b)
}

// bytes writes the DOCX back, parts in their original order.
func (d *docx) bytes() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range d.names {
		w, err := zw.Create(n)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(d.parts[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var headerFooter = regexp.MustCompile(`^word/(header|footer)[0-9]*\.xml$`)

// textParts are the parts tokens resolve in: the main body (with its tables), headers
// and footers.
func (d *docx) textParts() []string {
	out := []string{documentPart}
	var hf []string
	for _, n := range d.names {
		if headerFooter.MatchString(n) {
			hf = append(hf, n)
		}
	}
	sort.Strings(hf)
	return append(out, hf...)
}

// blankDOCX is a minimal valid DOCX: content types, relationships, default styles and
// one paragraph holding text.
func blankDOCX(text string) []byte {
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{documentPart, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:p><w:r><w:t xml:space="preserve">` + xmlEscape(text) + `</w:t></w:r></w:p><w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`},
		{"word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"word/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:cs="Times New Roman"/><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr></w:rPrDefault><w:pPrDefault/></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style></w:styles>`},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			panic(err)
		}
		if _, err := io.WriteString(w, p.body); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

var xmlReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func xmlEscape(s string) string { return xmlReplacer.Replace(s) }

// textEl is one <w:t> element in a part: its whole span, its content span and the
// paragraph that holds it.
type textEl struct {
	start, end   int
	cstart, cend int
	para         int
}

var textScan = regexp.MustCompile(`<w:p(?:\s[^>]*)?/>|<w:p(?:\s[^>]*)?>|</w:p>|<w:t(?:\s[^>]*)?/>|<w:t(?:\s[^>]*)?>([^<]*)</w:t>`)

// scanText finds every text element in a part with its innermost paragraph.
func scanText(x string) []textEl {
	var out []textEl
	var stack []int
	var next int
	for _, m := range textScan.FindAllStringSubmatchIndex(x, -1) {
		tag := x[m[0]:m[matchEnd]]
		switch {
		case strings.HasPrefix(tag, "</w:p"):
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case strings.HasPrefix(tag, "<w:p"):
			if !strings.HasSuffix(tag, "/>") {
				next++
				stack = append(stack, next)
			}
		case m[groupStart] != -1:
			var p int
			if len(stack) > 0 {
				p = stack[len(stack)-1]
			}
			out = append(out, textEl{start: m[0], end: m[matchEnd], cstart: m[groupStart], cend: m[groupEnd], para: p})
		}
	}
	return out
}

var tokenRe = regexp.MustCompile(`\{\{\s*([a-z0-9_]+(?::[a-z0-9_]+)?)\s*\}\}`)

// paragraphText joins each paragraph's run text, in order, by paragraph.
func paragraphs(x string, els []textEl) (order []int, groups map[int][]int) {
	groups = map[int][]int{}
	for i, e := range els {
		if _, ok := groups[e.para]; !ok {
			order = append(order, e.para)
		}
		groups[e.para] = append(groups[e.para], i)
	}
	return order, groups
}

// partTokens lists the token names in a part, in order, slots included as "slot:name".
func partTokens(x string) []string {
	els := scanText(x)
	order, groups := paragraphs(x, els)
	var out []string
	for _, p := range order {
		var sb strings.Builder
		for _, i := range groups[p] {
			sb.WriteString(x[els[i].cstart:els[i].cend])
		}
		for _, m := range tokenRe.FindAllStringSubmatch(sb.String(), -1) {
			out = append(out, m[firstGroup])
		}
	}
	return out
}

// breakRun ends a text element, writes a line break and starts the next element, so a
// line break in a value becomes a DOCX line break in the same run.
const breakRun = `</w:t><w:br/><w:t xml:space="preserve">`

// fillValue is how a value is written into a text element: XML-escaped, line breaks as
// DOCX breaks.
func fillValue(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	lines := strings.Split(v, "\n")
	for i := range lines {
		lines[i] = xmlEscape(lines[i])
	}
	return strings.Join(lines, breakRun)
}

// fillPart replaces every token in a part that value answers. A token split across runs
// in one paragraph still resolves: the result goes into the first run that held the
// token, keeping that run's formatting, and the token's text is removed from the runs
// after it. Slots are left for fillSlots.
func fillPart(x string, value func(name string) (string, bool)) string {
	els := scanText(x)
	if len(els) == 0 {
		return x
	}
	content := make([]string, len(els))
	changed := make([]bool, len(els))
	for i, e := range els {
		content[i] = x[e.cstart:e.cend]
	}
	order, groups := paragraphs(x, els)
	for _, p := range order {
		idx := groups[p]
		offs := make([]int, len(idx))
		var sb strings.Builder
		for k, i := range idx {
			offs[k] = sb.Len()
			sb.WriteString(content[i])
		}
		joined := sb.String()
		ms := tokenRe.FindAllStringSubmatchIndex(joined, -1)
		for j := len(ms) - 1; j >= 0; j-- {
			m := ms[j]
			name := joined[m[groupStart]:m[groupEnd]]
			v, ok := value(name)
			if strings.HasPrefix(name, "slot:") || !ok {
				continue
			}
			repl := fillValue(v)
			first := true
			for k, i := range idx {
				lo, hi := offs[k], offs[k]+len(x[els[i].cstart:els[i].cend])
				if m[matchEnd] <= lo || m[0] >= hi {
					continue
				}
				a, b := max(m[0], lo)-lo, min(m[matchEnd], hi)-lo
				c := content[i]
				if first {
					content[i] = c[:a] + repl + c[b:]
					first = false
				} else {
					content[i] = c[:a] + c[b:]
				}
				changed[i] = true
			}
		}
	}
	var out strings.Builder
	var last int
	for i, e := range els {
		if !changed[i] {
			continue
		}
		out.WriteString(x[last:e.start])
		out.WriteString(`<w:t xml:space="preserve">` + content[i] + `</w:t>`)
		last = e.end
	}
	out.WriteString(x[last:])
	return out.String()
}

// fill resolves tokens in every text part.
func (d *docx) fill(value func(string) (string, bool)) {
	for _, n := range d.textParts() {
		d.parts[n] = []byte(fillPart(string(d.parts[n]), value))
	}
}

// element is one child element of w:body.
type element struct {
	start, end int
	name       string
}

// bodyChildren finds w:body and its direct child elements.
func bodyChildren(x string) (bodyOpen, bodyClose int, kids []element, err error) {
	bo := strings.Index(x, "<w:body")
	if bo == -1 {
		return 0, 0, nil, errNoDocument
	}
	gt := strings.Index(x[bo:], ">")
	if gt == -1 {
		return 0, 0, nil, errNoDocument
	}
	i := bo + gt + 1
	bodyOpen = i
	var depth int
	cur := element{}
	for i < len(x) {
		lt := strings.IndexByte(x[i:], '<')
		if lt == -1 {
			break
		}
		lt += i
		end := strings.IndexByte(x[lt:], '>')
		if end == -1 {
			break
		}
		end += lt + 1
		tag := x[lt:end]
		switch {
		case strings.HasPrefix(tag, "<?"), strings.HasPrefix(tag, "<!"):
		case strings.HasPrefix(tag, "</"):
			if depth == zero {
				return bodyOpen, lt, kids, nil // </w:body>
			}
			depth--
			if depth == zero {
				cur.end = end
				kids = append(kids, cur)
			}
		default:
			name := strings.TrimPrefix(tag, "<")
			if k := strings.IndexAny(name, " \t\r\n/>"); k != -1 {
				name = name[:k]
			}
			self := strings.HasSuffix(tag, "/>")
			if depth == zero {
				cur = element{start: lt, name: name}
				if self {
					cur.end = end
					kids = append(kids, cur)
				}
			}
			if !self {
				depth++
			}
		}
		i = end
	}
	return 0, 0, nil, errNoDocument
}

var slotRe = regexp.MustCompile(`^\s*\{\{\s*slot:([a-z0-9_]+)\s*\}\}\s*$`)

func joinedText(x string) string {
	var sb strings.Builder
	for _, e := range scanText(x) {
		sb.WriteString(x[e.cstart:e.cend])
	}
	return sb.String()
}

// slots lists the slot names in the main body, in order. A slot token that isn't alone
// in a top-level paragraph is an error.
func (d *docx) slots() ([]string, error) {
	x := string(d.parts[documentPart])
	_, _, kids, err := bodyChildren(x)
	if err != nil {
		return nil, err
	}
	var out []string
	var lone int
	for _, k := range kids {
		if k.name != "w:p" {
			continue
		}
		if m := slotRe.FindStringSubmatch(joinedText(x[k.start:k.end])); m != nil {
			out = append(out, m[firstGroup])
			lone++
		}
	}
	var all int
	for _, n := range d.textParts() {
		for _, t := range partTokens(string(d.parts[n])) {
			if strings.HasPrefix(t, "slot:") {
				all++
			}
		}
	}
	if all != lone {
		return out, errSlotNotLone
	}
	return out, nil
}

// tokens lists the distinct non-slot token names across every text part, in order.
func (d *docx) tokens() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range d.textParts() {
		for _, t := range partTokens(string(d.parts[n])) {
			if strings.HasPrefix(t, "slot:") || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// componentChecks are the parts a slot can't copy, in the order they are reported.
var componentChecks = []struct {
	part  string
	marks []string
}{
	{"picture", []string{"<w:drawing", "<w:pict", "<v:imagedata", "<w:object", "<pic:"}},
	{"hyperlink", []string{"<w:hyperlink"}},
	{"footnote", []string{"<w:footnoteReference"}},
	{"endnote", []string{"<w:endnoteReference"}},
	{"comment", []string{"<w:commentReference", "<w:commentRangeStart"}},
	{"numbering", []string{"<w:numPr"}},
}

var relAttr = regexp.MustCompile(`\sr:[A-Za-z]+="`)

// componentBody returns the paragraphs and tables a component contributes to a slot,
// refusing any part the copy would break.
func (d *docx) componentBody() (string, error) {
	x := string(d.parts[documentPart])
	_, _, kids, err := bodyChildren(x)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, k := range kids {
		switch k.name {
		case "w:p", "w:tbl":
			sb.WriteString(x[k.start:k.end])
		case "w:sectPr", "w:bookmarkStart", "w:bookmarkEnd":
		default:
			return "", &partError{Part: "content control (" + k.name + ")"}
		}
	}
	body := sb.String()
	for _, c := range componentChecks {
		for _, m := range c.marks {
			if strings.Contains(body, m) {
				return "", &partError{Part: c.part}
			}
		}
	}
	if relAttr.MatchString(body) {
		return "", &partError{Part: "relationship part"}
	}
	if len(partTokens(body)) > 0 {
		for _, t := range partTokens(body) {
			if strings.HasPrefix(t, "slot:") {
				return "", &partError{Part: "slot"}
			}
		}
	}
	return body, nil
}

// fillSlots replaces each slot paragraph in the main body with its component's
// paragraphs and tables, in order.
func (d *docx) fillSlots(bodies map[string]string) error {
	x := string(d.parts[documentPart])
	_, _, kids, err := bodyChildren(x)
	if err != nil {
		return err
	}
	var out strings.Builder
	var last int
	for _, k := range kids {
		if k.name != "w:p" {
			continue
		}
		m := slotRe.FindStringSubmatch(joinedText(x[k.start:k.end]))
		if m == nil {
			continue
		}
		body, ok := bodies[m[firstGroup]]
		if !ok {
			return fmt.Errorf("slot %s is not mapped", m[firstGroup])
		}
		out.WriteString(x[last:k.start])
		out.WriteString(body)
		last = k.end
	}
	out.WriteString(x[last:])
	d.parts[documentPart] = []byte(out.String())
	return nil
}
