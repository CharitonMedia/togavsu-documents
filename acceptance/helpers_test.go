package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strings"

	documents "github.com/CharitonMedia/togavsu-documents"
	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// Synthetic DOCX bodies.

const wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

var xmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// para is one paragraph of plain runs.
func para(runs ...string) string {
	var b strings.Builder
	b.WriteString("<w:p>")
	for _, r := range runs {
		b.WriteString(`<w:r><w:t xml:space="preserve">` + xmlEsc.Replace(r) + `</w:t></w:r>`)
	}
	b.WriteString("</w:p>")
	return b.String()
}

// docx is a DOCX whose body holds body, plus extra parts by name.
func docx(body string, extra ...map[string]string) []byte {
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + wNS + `><w:body>` + body + `<w:sectPr/></w:body></w:document>`},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			panic(err)
		}
		_, _ = io.WriteString(w, body)
	}
	for _, p := range parts {
		write(p.name, p.body)
	}
	for _, e := range extra {
		for name, body := range e {
			write(name, body)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// zipParts reads a zip's entries.
func zipParts(b []byte) map[string][]byte {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = body
	}
	return out
}

var tText = regexp.MustCompile(`<w:t(?:\s[^>]*)?>([^<]*)</w:t>`)

// docText is the visible text of a DOCX part, paragraphs on their own lines.
func docText(b []byte, part string) string {
	x := string(zipParts(b)[part])
	x = strings.ReplaceAll(x, "<w:br/>", "<w:t>\n</w:t>")
	x = strings.ReplaceAll(x, "</w:p>", "<w:t>\n</w:t>")
	var out strings.Builder
	for _, m := range tText.FindAllStringSubmatch(x, -1) {
		out.WriteString(m[1])
	}
	return strings.TrimSpace(strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(out.String()))
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	return strings.Trim(slugStrip.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// --- The module's records, made through its own API as a viewer ---

// template uploads a DOCX template as who.
func (k *kit) template(who, name string, body []byte, fields map[string]string) documents.Template {
	k.t.Helper()
	f := map[string]string{"mode": "upload", "name": name}
	for key, v := range fields {
		f[key] = v
	}
	var tm documents.Template
	k.decode(k.want(k.do(who, "POST", "/api/templates", "", &form{fields: f, fileName: slug(name) + ".docx", data: body}), http.StatusCreated), &tm)
	return tm
}

// component uploads a DOCX component of kind as who.
func (k *kit) component(who, name, kind string, body []byte) documents.Component {
	k.t.Helper()
	var c documents.Component
	k.decode(k.want(k.do(who, "POST", "/api/components", "", &form{fields: map[string]string{"mode": "upload", "name": name, "category": kind},
		fileName: slug(name) + ".docx", data: body}), http.StatusCreated), &c)
	return c
}

// generate runs a template or package on matter as who.
func (k *kit) generate(who, matter string, in map[string]any) coretest.Resp {
	k.t.Helper()
	return k.do(who, "POST", "/api/matters/"+matter+"/generate", matter, in)
}

// current is a core file's current version bytes, read as the admin.
func (k *kit) current(fileID string) []byte {
	k.t.Helper()
	var vs struct {
		Data []struct {
			ID     string `json:"id"`
			Number int    `json:"number"`
		} `json:"data"`
	}
	k.coreJSON("admin", "/api/v1/files/"+fileID+"/versions", &vs)
	best := -1
	for i, v := range vs.Data {
		if best < 0 || v.Number > vs.Data[best].Number {
			best = i
		}
	}
	if best < 0 {
		k.t.Fatalf("file %s has no versions", fileID)
	}
	r := k.Admin.Do("GET", "/api/v1/files/"+fileID+"/versions/"+vs.Data[best].ID+"/download", nil)
	if r.Code != http.StatusOK {
		k.t.Fatalf("download: %d %s", r.Code, r.Body)
	}
	return []byte(r.Body)
}
