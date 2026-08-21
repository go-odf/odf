// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/go-richdoc/richdoc"
)

// Write renders a [richdoc.Document] as the bytes of a minimal, valid
// OpenDocument Text (.odt) package.
//
// The returned ZIP always begins with a stored (uncompressed) mimetype entry,
// as the OpenDocument packaging specification requires, followed by
// META-INF/manifest.xml, content.xml, styles.xml, an optional meta.xml (when the
// document carries metadata) and any embedded Pictures/. Inline formatting is
// generated as automatic text styles in content.xml.
//
// Write is the inverse of [Parse] over the supported model: parsing Write's
// output reproduces the input tree. An [richdoc.Image] whose URL is a data: URI
// is decoded and re-embedded; a malformed data: URI is the only input that makes
// Write fail.
func Write(d *richdoc.Document) ([]byte, error) {
	if d == nil {
		d = &richdoc.Document{}
	}
	w := &writer{}
	w.writeBlocks(d.Blocks)
	if w.err != nil {
		return nil, w.err
	}

	content := buildContent(w.body.String(), w.autoStyles.String())
	meta := buildMeta(d.Meta)
	return w.pack(content, meta), nil
}

// writer accumulates the rendered office:text body, the automatic styles the
// body refers to, and the image parts to embed.
type writer struct {
	body       strings.Builder
	autoStyles strings.Builder // per-document generated styles (lists)
	images     []imagePart
	err        error

	listSeq, sectSeq, tableSeq, frameSeq, imgSeq, noteSeq int
}

type imagePart struct {
	path      string
	mediaType string
	data      []byte
}

func (w *writer) setErr(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *writer) writeBlocks(blocks []richdoc.Block) {
	for _, b := range blocks {
		w.writeBlock(b)
	}
}

func (w *writer) writeBlock(b richdoc.Block) {
	switch n := b.(type) {
	case richdoc.Heading:
		lvl := n.Level
		if lvl < 1 {
			lvl = 1
		}
		if lvl > 6 {
			lvl = 6
		}
		w.body.WriteString(`<text:h text:outline-level="` + strconv.Itoa(lvl) + `">`)
		w.writeInlines(n.Inlines)
		w.body.WriteString(`</text:h>`)
	case richdoc.Paragraph:
		w.body.WriteString(`<text:p>`)
		w.writeInlines(n.Inlines)
		w.body.WriteString(`</text:p>`)
	case richdoc.CodeBlock:
		w.body.WriteString(`<text:p text:style-name="` + styleCode + `" odfgo:code-language="` + escAttr(n.Language) + `">`)
		w.writeCodeText(n.Text)
		w.body.WriteString(`</text:p>`)
	case richdoc.BlockQuote:
		w.sectSeq++
		w.body.WriteString(`<text:section text:name="Sect` + strconv.Itoa(w.sectSeq) + `" odfgo:blockquote="true">`)
		w.writeBlocks(n.Blocks)
		w.body.WriteString(`</text:section>`)
	case richdoc.List:
		w.writeList(n)
	case richdoc.Table:
		w.writeTable(n)
	case richdoc.ThematicBreak:
		w.body.WriteString(`<text:p text:style-name="` + styleHR + `" odfgo:thematic-break="true"></text:p>`)
	case richdoc.MathBlock:
		w.body.WriteString(`<text:p odfgo:math="block">` + escText(n.TeX) + `</text:p>`)
	case richdoc.RawBlock:
		if n.Format == "odf" {
			w.body.WriteString(n.Text)
		}
	}
}

// writeCodeText renders verbatim code, mapping internal newlines to
// text:line-break so the source survives the round-trip.
func (w *writer) writeCodeText(text string) {
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			w.body.WriteString(`<text:line-break/>`)
		}
		w.body.WriteString(escText(line))
	}
}

func (w *writer) writeList(l richdoc.List) {
	start := l.Start
	if start < 1 {
		start = 1
	}
	name := w.listStyle(l.Ordered, start)
	w.body.WriteString(`<text:list text:style-name="` + name + `"`)
	if l.Tight {
		w.body.WriteString(` odfgo:tight="true"`)
	}
	w.body.WriteString(`>`)
	for _, it := range l.Items {
		w.body.WriteString(`<text:list-item>`)
		w.writeBlocks(it.Blocks)
		w.body.WriteString(`</text:list-item>`)
	}
	w.body.WriteString(`</text:list>`)
}

// listStyle appends a fresh list style to the automatic styles and returns its
// generated name. Numbered styles carry their start value so ordered lists keep
// their first number across a round-trip.
func (w *writer) listStyle(ordered bool, start int) string {
	w.listSeq++
	name := "L" + strconv.Itoa(w.listSeq)
	if ordered {
		w.autoStyles.WriteString(`<text:list-style style:name="` + name + `"><text:list-level-style-number text:level="1" style:num-format="1" text:start-value="` + strconv.Itoa(start) + `"/></text:list-style>`)
	} else {
		w.autoStyles.WriteString(`<text:list-style style:name="` + name + `"><text:list-level-style-bullet text:level="1" text:bullet-char="&#8226;"/></text:list-style>`)
	}
	return name
}

func (w *writer) writeTable(t richdoc.Table) {
	w.tableSeq++
	cols := len(t.Header)
	for _, row := range t.Rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	w.body.WriteString(`<table:table table:name="Table` + strconv.Itoa(w.tableSeq) + `">`)
	if cols > 0 {
		w.body.WriteString(`<table:table-column table:number-columns-repeated="` + strconv.Itoa(cols) + `"/>`)
	}
	if len(t.Header) > 0 {
		w.body.WriteString(`<table:table-header-rows><table:table-row>`)
		w.writeRow(t.Header, t.Align)
		w.body.WriteString(`</table:table-row></table:table-header-rows>`)
	}
	for _, row := range t.Rows {
		w.body.WriteString(`<table:table-row>`)
		w.writeRow(row, t.Align)
		w.body.WriteString(`</table:table-row>`)
	}
	w.body.WriteString(`</table:table>`)
}

func (w *writer) writeRow(cells []richdoc.Cell, align []richdoc.Alignment) {
	for j, c := range cells {
		a := richdoc.AlignDefault
		if j < len(align) {
			a = align[j]
		}
		w.body.WriteString(`<table:table-cell office:value-type="string">`)
		if style := alignStyleName(a); style != "" {
			w.body.WriteString(`<text:p text:style-name="` + style + `">`)
		} else {
			w.body.WriteString(`<text:p>`)
		}
		w.writeInlines(c.Inlines)
		w.body.WriteString(`</text:p></table:table-cell>`)
	}
}

func alignStyleName(a richdoc.Alignment) string {
	switch a {
	case richdoc.AlignLeft:
		return styleAlignL
	case richdoc.AlignCenter:
		return styleAlignC
	case richdoc.AlignRight:
		return styleAlignR
	default:
		return ""
	}
}

func (w *writer) writeInlines(inlines []richdoc.Inline) {
	for _, in := range inlines {
		w.writeInline(in)
	}
}

func (w *writer) writeInline(in richdoc.Inline) {
	switch n := in.(type) {
	case richdoc.Text:
		w.body.WriteString(escText(n.Value))
	case richdoc.Strong:
		w.span(styleBold, n.Inlines)
	case richdoc.Emph:
		w.span(styleItalic, n.Inlines)
	case richdoc.Strikethrough:
		w.span(styleStrike, n.Inlines)
	case richdoc.Code:
		w.body.WriteString(`<text:span text:style-name="` + styleMono + `">` + escText(n.Value) + `</text:span>`)
	case richdoc.Link:
		w.body.WriteString(`<text:a xlink:type="simple" xlink:href="` + escAttr(n.URL) + `"`)
		if n.Title != "" {
			w.body.WriteString(` office:title="` + escAttr(n.Title) + `"`)
		}
		w.body.WriteString(`>`)
		w.writeInlines(n.Inlines)
		w.body.WriteString(`</text:a>`)
	case richdoc.Image:
		w.writeImage(n)
	case richdoc.Math:
		w.body.WriteString(`<text:span odfgo:math="inline">` + escText(n.TeX) + `</text:span>`)
	case richdoc.Footnote:
		w.noteSeq++
		seq := strconv.Itoa(w.noteSeq)
		w.body.WriteString(`<text:note text:id="ftn` + seq + `" text:note-class="footnote"><text:note-citation>` + seq + `</text:note-citation><text:note-body>`)
		w.writeBlocks(n.Blocks)
		w.body.WriteString(`</text:note-body></text:note>`)
	case richdoc.Anchor:
		// ODF bookmarks are point markers; any Inlines the Anchor labels are
		// written adjacent to the bookmark so no visible content is lost.
		w.body.WriteString(`<text:bookmark text:name="` + escAttr(n.ID) + `"/>`)
		w.writeInlines(n.Inlines)
	case richdoc.CrossRef:
		w.body.WriteString(`<text:bookmark-ref text:ref-name="` + escAttr(n.Target) + `"`)
		if n.Kind == richdoc.RefCite {
			// ODF has no citation element: tag the ref so Parse restores RefCite.
			w.body.WriteString(` odfgo:` + attrCite + `="true"`)
		}
		w.body.WriteString(`>`)
		w.writeInlines(n.Inlines)
		w.body.WriteString(`</text:bookmark-ref>`)
	case richdoc.LineBreak:
		w.body.WriteString(`<text:line-break/>`)
	case richdoc.RawInline:
		if n.Format == "odf" {
			w.body.WriteString(n.Text)
		}
	}
}

func (w *writer) span(style string, inlines []richdoc.Inline) {
	w.body.WriteString(`<text:span text:style-name="` + style + `">`)
	w.writeInlines(inlines)
	w.body.WriteString(`</text:span>`)
}

func (w *writer) writeImage(img richdoc.Image) {
	w.frameSeq++
	href := img.URL
	if strings.HasPrefix(img.URL, "data:") {
		data, mt, err := decodeDataURI(img.URL)
		if err != nil {
			w.setErr(err)
			return
		}
		w.imgSeq++
		path := dirPictures + "img" + strconv.Itoa(w.imgSeq) + "." + extForMedia(mt)
		w.images = append(w.images, imagePart{path: path, mediaType: mt, data: data})
		href = path
	}
	w.body.WriteString(`<draw:frame draw:name="Image` + strconv.Itoa(w.frameSeq) + `" text:anchor-type="as-char">`)
	w.body.WriteString(`<draw:image xlink:href="` + escAttr(href) + `" xlink:type="simple" xlink:show="embed" xlink:actuate="onLoad"/>`)
	if img.Title != "" {
		w.body.WriteString(`<svg:title>` + escText(img.Title) + `</svg:title>`)
	}
	if img.Alt != "" {
		w.body.WriteString(`<svg:desc>` + escText(img.Alt) + `</svg:desc>`)
	}
	w.body.WriteString(`</draw:frame>`)
}

// pack assembles the ZIP container. The mimetype entry is written first and
// stored uncompressed, per the OpenDocument packaging requirement. Writes go to
// an in-memory buffer with unique entry names, so the archive/zip calls cannot
// fail and their errors are deliberately not threaded out.
func (w *writer) pack(content, meta string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	fw, _ := zw.CreateHeader(&zip.FileHeader{Name: partMimetype, Method: zip.Store})
	io.WriteString(fw, mimetypeODT)

	store := func(name, data string) {
		f, _ := zw.Create(name)
		io.WriteString(f, data)
	}
	store(partManifest, buildManifest(meta != "", w.images))
	store(partContent, content)
	store(partStyles, buildStyles())
	if meta != "" {
		store(partMeta, meta)
	}
	for _, img := range w.images {
		f, _ := zw.Create(img.path)
		f.Write(img.data)
	}
	zw.Close()
	return buf.Bytes()
}

// buildContent wraps the rendered body and generated list styles in an
// office:document-content shell, declaring every namespace the body (and any
// re-injected raw XML) may use.
func buildContent(body, listStyles string) string {
	var b strings.Builder
	b.WriteString(xmlDecl)
	b.WriteString(`<office:document-content ` + odfNamespaces + ` office:version="1.3">`)
	b.WriteString(`<office:automatic-styles>`)
	b.WriteString(constAutoStyles)
	b.WriteString(listStyles)
	b.WriteString(`</office:automatic-styles>`)
	b.WriteString(`<office:body><office:text>`)
	b.WriteString(body)
	b.WriteString(`</office:text></office:body>`)
	b.WriteString(`</office:document-content>`)
	return b.String()
}

func buildStyles() string {
	return xmlDecl +
		`<office:document-styles ` + odfNamespaces + ` office:version="1.3">` +
		`<office:styles/><office:automatic-styles/><office:master-styles/>` +
		`</office:document-styles>`
}

func buildManifest(hasMeta bool, images []imagePart) string {
	var b strings.Builder
	b.WriteString(xmlDecl)
	b.WriteString(`<manifest:manifest xmlns:manifest="` + nsManifest + `" manifest:version="1.3">`)
	b.WriteString(`<manifest:file-entry manifest:full-path="/" manifest:version="1.3" manifest:media-type="` + mimetypeODT + `"/>`)
	b.WriteString(`<manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>`)
	b.WriteString(`<manifest:file-entry manifest:full-path="styles.xml" manifest:media-type="text/xml"/>`)
	if hasMeta {
		b.WriteString(`<manifest:file-entry manifest:full-path="meta.xml" manifest:media-type="text/xml"/>`)
	}
	for _, img := range images {
		b.WriteString(`<manifest:file-entry manifest:full-path="` + escAttr(img.path) + `" manifest:media-type="` + escAttr(img.mediaType) + `"/>`)
	}
	b.WriteString(`</manifest:manifest>`)
	return b.String()
}

// buildMeta renders meta.xml from the document metadata, or "" when there is
// none. Well-known keys map onto Dublin Core / meta elements; every other key is
// preserved as a meta:user-defined field so arbitrary metadata round-trips.
func buildMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	std := map[string]string{
		"title":       "dc:title",
		"author":      "dc:creator",
		"date":        "dc:date",
		"subject":     "dc:subject",
		"description": "dc:description",
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(xmlDecl)
	b.WriteString(`<office:document-meta xmlns:office="` + nsOffice + `" xmlns:dc="` + nsDC + `" xmlns:meta="` + nsMeta + `" office:version="1.3"><office:meta>`)
	for _, k := range keys {
		if el, ok := std[k]; ok {
			b.WriteString(`<` + el + `>` + escText(meta[k]) + `</` + el + `>`)
		} else {
			b.WriteString(`<meta:user-defined meta:name="` + escAttr(k) + `">` + escText(meta[k]) + `</meta:user-defined>`)
		}
	}
	b.WriteString(`</office:meta></office:document-meta>`)
	return b.String()
}

// decodeDataURI decodes a base64 "data:<media-type>;base64,<payload>" URI into
// its bytes and media type.
func decodeDataURI(uri string) ([]byte, string, error) {
	rest, ok := strings.CutPrefix(uri, "data:")
	if !ok {
		return nil, "", errBadDataURI
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, "", errBadDataURI
	}
	mediaType, isB64 := strings.CutSuffix(meta, ";base64")
	if !isB64 {
		return nil, "", errBadDataURI
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", errBadDataURI
	}
	return data, mediaType, nil
}

func extForMedia(mt string) string {
	switch mt {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/svg+xml":
		return "svg"
	default:
		return "bin"
	}
}

// escText escapes a run of character data for XML text content.
func escText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escAttr escapes an attribute value. Whitespace that XML attribute-value
// normalization would otherwise fold is written as numeric references so it
// survives the round-trip.
func escAttr(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\n':
			b.WriteString("&#10;")
		case '\r':
			b.WriteString("&#13;")
		case '\t':
			b.WriteString("&#9;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

const xmlDecl = `<?xml version="1.0" encoding="UTF-8"?>`

// odfNamespaces is the set of namespace declarations placed on the root of
// content.xml and styles.xml. It is deliberately generous so raw XML re-injected
// from RawBlock/RawInline can reference any common ODF prefix.
const odfNamespaces = `xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"` +
	` xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"` +
	` xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"` +
	` xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0"` +
	` xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"` +
	` xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"` +
	` xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0"` +
	` xmlns:xlink="http://www.w3.org/1999/xlink"` +
	` xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0"` +
	` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
	` xmlns:number="urn:oasis:names:tc:opendocument:xmlns:datastyle:1.0"` +
	` xmlns:loext="urn:org:documentfoundation:names:experimental:office:xmlns:loext:1.0"` +
	` xmlns:odfgo="https://github.com/go-odf/odf"`

// constAutoStyles are the fixed automatic styles every content.xml carries; the
// body refers to them by name and Parse resolves them back to formatting.
const constAutoStyles = `<style:style style:name="T_b" style:family="text"><style:text-properties fo:font-weight="bold"/></style:style>` +
	`<style:style style:name="T_i" style:family="text"><style:text-properties fo:font-style="italic"/></style:style>` +
	`<style:style style:name="T_s" style:family="text"><style:text-properties style:text-line-through-style="solid"/></style:style>` +
	`<style:style style:name="T_c" style:family="text"><style:text-properties style:font-name="` + monoFont + `"/></style:style>` +
	`<style:style style:name="P_l" style:family="paragraph"><style:paragraph-properties fo:text-align="left"/></style:style>` +
	`<style:style style:name="P_c" style:family="paragraph"><style:paragraph-properties fo:text-align="center"/></style:style>` +
	`<style:style style:name="P_r" style:family="paragraph"><style:paragraph-properties fo:text-align="right"/></style:style>` +
	`<style:style style:name="P_hr" style:family="paragraph"><style:paragraph-properties fo:border-bottom="0.5pt solid #000000"/></style:style>` +
	`<style:style style:name="P_code" style:family="paragraph"><style:text-properties style:font-name="` + monoFont + `"/></style:style>`
