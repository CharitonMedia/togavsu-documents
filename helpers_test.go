package documents

import (
	"archive/zip"
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// Synthetic DOCX bodies for tests.

const wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

// para is one paragraph of plain runs.
func para(runs ...string) string {
	var b strings.Builder
	b.WriteString("<w:p>")
	for _, r := range runs {
		b.WriteString(`<w:r><w:t xml:space="preserve">` + xmlEscape(r) + `</w:t></w:r>`)
	}
	b.WriteString("</w:p>")
	return b.String()
}

// makeDOCX builds a DOCX whose body holds body, plus any extra parts.
func makeDOCX(t testing.TB, body string, extra map[string]string) []byte {
	t.Helper()
	d, err := readDOCX(blankDOCX(""))
	if err != nil {
		t.Fatal(err)
	}
	d.parts[documentPart] = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + wNS + `><w:body>` + body + `<w:sectPr/></w:body></w:document>`)
	for name, x := range extra {
		if _, ok := d.parts[name]; !ok {
			d.names = append(d.names, name)
		}
		d.parts[name] = []byte(x)
	}
	b, err := d.bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hdrPart(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:hdr ` + wNS + `>` + body + `</w:hdr>`
}

func ftrPart(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:ftr ` + wNS + `>` + body + `</w:ftr>`
}

var tText = regexp.MustCompile(`<w:t(?:\s[^>]*)?>([^<]*)</w:t>`)

// docText is the visible text of one part, paragraphs on their own lines, breaks as \n.
func docText(t testing.TB, b []byte, part string) string {
	t.Helper()
	d, err := readDOCX(b)
	if err != nil {
		t.Fatal(err)
	}
	x := string(d.parts[part])
	x = strings.ReplaceAll(x, "<w:br/>", "<w:t>\n</w:t>")
	x = strings.ReplaceAll(x, "</w:p>", "<w:t>\n</w:t>")
	var out strings.Builder
	for _, m := range tText.FindAllStringSubmatch(x, -1) {
		out.WriteString(m[1])
	}
	return strings.TrimSpace(unescape(out.String()))
}

var xmlUnescaper = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")

func unescape(s string) string { return xmlUnescaper.Replace(s) }

// zipEntries reads a zip's entry names and bytes.
func zipEntries(t testing.TB, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rc)
		rc.Close()
		out[f.Name] = buf.Bytes()
	}
	return out
}
