package documents

import (
	"errors"
	"strings"
	"testing"
)

func fillAll(t *testing.T, b []byte, vals map[string]string) []byte {
	t.Helper()
	d, err := readDOCX(b)
	if err != nil {
		t.Fatal(err)
	}
	d.fill(func(n string) (string, bool) { v, ok := vals[n]; return v, ok })
	out, err := d.bytes()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// MOD-DOC-12: a token split across three runs resolves in the first run's formatting;
// values are escaped; a newline writes a w:br.
func TestFillSplitRunsEscapingBreaks(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:b/></w:rPr><w:t>Dear {{client_</w:t></w:r><w:r><w:t>full_</w:t></w:r><w:r><w:rPr><w:i/></w:rPr><w:t>name}},</w:t></w:r></w:p>` +
		para("Company: {{firm_name}}") + para("{{firm_address}}")
	out := fillAll(t, makeDOCX(t, body, nil), map[string]string{"client_full_name": "Avery Sample", "firm_name": "R&D <Co>", "firm_address": "Line 1\nLine 2"})
	d, _ := readDOCX(out)
	x := string(d.parts[documentPart])
	if !strings.Contains(x, `<w:rPr><w:b/></w:rPr><w:t xml:space="preserve">Dear Avery Sample</w:t>`) {
		t.Fatalf("the name is not in the first run's formatting:\n%s", x)
	}
	if strings.Contains(x, "full_") || strings.Contains(x, "{{") {
		t.Fatalf("token text left behind:\n%s", x)
	}
	if !strings.Contains(x, "R&amp;D &lt;Co&gt;") {
		t.Fatalf("value not escaped:\n%s", x)
	}
	if !strings.Contains(x, "Line 1</w:t><w:br/><w:t xml:space=\"preserve\">Line 2") {
		t.Fatalf("newline did not write a w:br:\n%s", x)
	}
	if got := docText(t, out, documentPart); !strings.HasPrefix(got, "Dear Avery Sample,") {
		t.Fatalf("text = %q", got)
	}
}

// MOD-DOC-13: tokens in headers and footers resolve.
func TestFillHeadersFooters(t *testing.T) {
	b := makeDOCX(t, para("Body {{matter_number}}"), map[string]string{
		"word/header1.xml": hdrPart(para("Case {{matter_case_number}}")),
		"word/footer1.xml": ftrPart(para("{{firm_name}} page")),
	})
	d, _ := readDOCX(b)
	got := strings.Join(d.tokens(), ",")
	for _, want := range []string{"matter_number", "matter_case_number", "firm_name"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tokens %s lack %s", got, want)
		}
	}
	out := fillAll(t, b, map[string]string{"matter_number": "2026-0001", "matter_case_number": "CV-26-001", "firm_name": "Sample Law"})
	if got := docText(t, out, "word/header1.xml"); got != "Case CV-26-001" {
		t.Fatalf("header = %q", got)
	}
	if got := docText(t, out, "word/footer1.xml"); got != "Sample Law page" {
		t.Fatalf("footer = %q", got)
	}
}

// MOD-DOC-14: a slot paragraph is replaced by the component's paragraphs and tables in
// order, and the component's tokens then resolve with the template's.
func TestSlotCopy(t *testing.T) {
	tmpl := makeDOCX(t, para("Before")+para("{{slot:caption}}")+para("After {{matter_number}}"), nil)
	comp := makeDOCX(t, para("IN THE {{matter_court}}")+`<w:tbl><w:tr><w:tc>`+para("{{client_full_name}}")+`</w:tc></w:tr></w:tbl>`+para("Cause {{matter_case_number}}"), nil)
	cd, err := readDOCX(comp)
	if err != nil {
		t.Fatal(err)
	}
	body, err := cd.componentBody()
	if err != nil {
		t.Fatal(err)
	}
	d, _ := readDOCX(tmpl)
	slots, err := d.slots()
	if err != nil || len(slots) != 1 || slots[0] != "caption" {
		t.Fatalf("slots = %v, %v", slots, err)
	}
	if err := d.fillSlots(map[string]string{"caption": body}); err != nil {
		t.Fatal(err)
	}
	d.fill(func(n string) (string, bool) {
		return map[string]string{"matter_court": "District Court", "client_full_name": "Avery Sample", "matter_case_number": "CV-1", "matter_number": "2026-0001"}[n], true
	})
	out, _ := d.bytes()
	got := docText(t, out, documentPart)
	want := "Before\nIN THE District Court\nAvery Sample\nCause CV-1\nAfter 2026-0001"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(string(d.parts[documentPart]), "<w:tbl>") {
		t.Fatal("the table was not copied")
	}
}

func TestSlotMustBeAlone(t *testing.T) {
	d, _ := readDOCX(makeDOCX(t, para("See {{slot:caption}} here"), nil))
	if _, err := d.slots(); !errors.Is(err, errSlotNotLone) {
		t.Fatalf("err = %v", err)
	}
}

// MOD-DOC-10 and 23: each relationship part a slot can't copy is refused by name.
func TestComponentParts(t *testing.T) {
	cases := map[string]string{
		"picture":   `<w:p><w:r><w:drawing><wp:inline/></w:drawing></w:r></w:p>`,
		"hyperlink": `<w:p><w:hyperlink r:id="rId9"><w:r><w:t>x</w:t></w:r></w:hyperlink></w:p>`,
		"footnote":  `<w:p><w:r><w:footnoteReference w:id="1"/></w:r></w:p>`,
		"endnote":   `<w:p><w:r><w:endnoteReference w:id="1"/></w:r></w:p>`,
		"comment":   `<w:p><w:commentRangeStart w:id="0"/><w:r><w:t>x</w:t></w:r></w:p>`,
		"numbering": `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`,
	}
	for part, body := range cases {
		d, err := readDOCX(makeDOCX(t, body, nil))
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.componentBody()
		var pe *partError
		if !errors.As(err, &pe) || pe.Part != part {
			t.Errorf("%s: err = %v", part, err)
		}
		if err := checkComponentBytes(makeDOCX(t, body, nil)); err == nil || !strings.Contains(err.Error(), part) {
			t.Errorf("%s: checkComponentBytes = %v", part, err)
		}
	}
	d, _ := readDOCX(makeDOCX(t, para("Signed {{attorney_name}}"), nil))
	if _, err := d.componentBody(); err != nil {
		t.Fatalf("a plain component is refused: %v", err)
	}
}

// MOD-DOC-05: the blank DOCX holds {{document_title}} in word/document.xml.
func TestBlankDOCX(t *testing.T) {
	b := blankDOCX("{{document_title}}")
	d, err := checkUpload("blank.docx", b)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml", "word/_rels/document.xml.rels", "word/styles.xml"} {
		if _, ok := d.parts[p]; !ok {
			t.Errorf("blank DOCX lacks %s", p)
		}
	}
	if got := d.tokens(); len(got) != 1 || got[0] != "document_title" {
		t.Fatalf("tokens = %v", got)
	}
}

// MOD-DOC-04: a PDF, or a zip without word/document.xml, is refused with the reason.
func TestNotDOCX(t *testing.T) {
	if _, err := checkUpload("motion.pdf", []byte("%PDF-1.7")); !errors.Is(err, errNotDOCX) {
		t.Fatalf("pdf: %v", err)
	}
	d, _ := readDOCX(blankDOCX("x"))
	delete(d.parts, documentPart)
	var names []string
	for _, n := range d.names {
		if n != documentPart {
			names = append(names, n)
		}
	}
	d.names = names
	b, _ := d.bytes()
	if _, err := checkUpload("empty.docx", b); !errors.Is(err, errNoDocument) {
		t.Fatalf("zip without document: %v", err)
	}
	if _, err := checkUpload("junk.docx", []byte("not a zip")); !errors.Is(err, errUnreadable) {
		t.Fatalf("junk: %v", err)
	}
}
