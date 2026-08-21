// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/go-richdoc/richdoc"
)

// --- ODT builders for hand-authored and malformed inputs ------------------

// zipODT builds a ZIP container from the given entries. A "mimetype" entry is
// written first, stored uncompressed.
func zipODT(entries map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if mt, ok := entries["mimetype"]; ok {
		fw, _ := zw.CreateHeader(&zip.FileHeader{Name: partMimetype, Method: zip.Store})
		io.WriteString(fw, mt)
	}
	for name, data := range entries {
		if name == partMimetype {
			continue
		}
		f, _ := zw.Create(name)
		io.WriteString(f, data)
	}
	zw.Close()
	return buf.Bytes()
}

// contentDoc wraps automatic styles and a body into a content.xml document.
func contentDoc(styles, body string) string {
	return xmlDecl +
		`<office:document-content ` + odfNamespaces + `>` +
		`<office:automatic-styles>` + styles + `</office:automatic-styles>` +
		`<office:body><office:text>` + body + `</office:text></office:body>` +
		`</office:document-content>`
}

// odtWith builds a minimal valid package around a content.xml body.
func odtWith(styles, body string) []byte {
	return zipODT(map[string]string{
		"mimetype":   mimetypeODT,
		partContent:  contentDoc(styles, body),
		partManifest: buildManifest(false, nil),
	})
}

// parseOK parses a hand-authored package built from styles + body and fails the
// test on error.
func parseOK(t *testing.T, styles, body string) *richdoc.Document {
	t.Helper()
	d, err := Parse(odtWith(styles, body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return d
}

// --- direct unit tests ----------------------------------------------------

func TestDecodeDataURI(t *testing.T) {
	if _, _, err := decodeDataURI("http://x"); err == nil {
		t.Fatal("want error for non-data URI")
	}
	if _, _, err := decodeDataURI("data:image/png;base64"); err == nil {
		t.Fatal("want error for missing comma")
	}
	if _, _, err := decodeDataURI("data:text/plain,hello"); err == nil {
		t.Fatal("want error for non-base64")
	}
	if _, _, err := decodeDataURI("data:image/png;base64,@@@@"); err == nil {
		t.Fatal("want error for bad base64")
	}
	data, mt, err := decodeDataURI("data:;base64,QQ==")
	if err != nil || mt != "application/octet-stream" || string(data) != "A" {
		t.Fatalf("empty media type: data=%q mt=%q err=%v", data, mt, err)
	}
}

func TestExtForMedia(t *testing.T) {
	cases := map[string]string{
		"image/png":     "png",
		"image/jpeg":    "jpg",
		"image/gif":     "gif",
		"image/svg+xml": "svg",
		"image/tiff":    "bin",
	}
	for mt, want := range cases {
		if got := extForMedia(mt); got != want {
			t.Errorf("extForMedia(%q) = %q, want %q", mt, got, want)
		}
	}
}

func TestMediaForExt(t *testing.T) {
	cases := map[string]string{
		"a.png":  "image/png",
		"a.jpg":  "image/jpeg",
		"a.jpeg": "image/jpeg",
		"a.gif":  "image/gif",
		"a.svg":  "image/svg+xml",
		"a.tiff": "application/octet-stream",
	}
	for name, want := range cases {
		if got := mediaForExt(name); got != want {
			t.Errorf("mediaForExt(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAlignment(t *testing.T) {
	cases := map[string]richdoc.Alignment{
		"left":    richdoc.AlignLeft,
		"start":   richdoc.AlignLeft,
		"center":  richdoc.AlignCenter,
		"right":   richdoc.AlignRight,
		"end":     richdoc.AlignRight,
		"":        richdoc.AlignDefault,
		"justify": richdoc.AlignDefault,
	}
	for align, want := range cases {
		if got := (styleProps{textAlign: align}).alignment(); got != want {
			t.Errorf("alignment(%q) = %v, want %v", align, got, want)
		}
	}
}

func TestEscAttr(t *testing.T) {
	got := escAttr("a<b>&\"c\n\r\td")
	want := "a&lt;b&gt;&amp;&quot;c&#10;&#13;&#9;d"
	if got != want {
		t.Fatalf("escAttr = %q, want %q", got, want)
	}
}

func TestWriteBadDataURI(t *testing.T) {
	doc := richdoc.New().P(richdoc.Img("data:image/png;base64,@@@", "", "")).Doc()
	if _, err := Write(doc); err == nil {
		t.Fatal("want error for malformed data URI")
	}
}

// --- container-level errors -----------------------------------------------

func TestParseNotAZip(t *testing.T) {
	if _, err := Parse([]byte("this is not a zip file")); err == nil {
		t.Fatal("want error for non-zip input")
	}
}

func TestParseMissingContent(t *testing.T) {
	src := zipODT(map[string]string{"mimetype": mimetypeODT})
	if _, err := Parse(src); err == nil {
		t.Fatal("want error for missing content.xml")
	}
}

func TestParseCorruptLocalHeader(t *testing.T) {
	// A valid ZIP whose first local header signature is damaged: the central
	// directory still parses, but opening the entry fails.
	src := zipODT(map[string]string{"mimetype": mimetypeODT, partContent: contentDoc("", "")})
	src[0] = 0x00 // corrupt the 'P' of the leading "PK\x03\x04" signature
	if _, err := Parse(src); err == nil {
		t.Fatal("want error opening a corrupt local header")
	}
}

func TestParseChecksumMismatch(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, _ := zw.CreateHeader(&zip.FileHeader{Name: partMimetype, Method: zip.Store})
	io.WriteString(fw, mimetypeODT)
	// A stored entry whose declared CRC does not match its data: reading it to
	// completion fails the checksum.
	data := []byte(contentDoc("", ""))
	raw, _ := zw.CreateRaw(&zip.FileHeader{
		Name:               partContent,
		Method:             zip.Store,
		CRC32:              0xdeadbeef,
		CompressedSize64:   uint64(len(data)),
		UncompressedSize64: uint64(len(data)),
	})
	raw.Write(data)
	zw.Close()
	if _, err := Parse(buf.Bytes()); err == nil {
		t.Fatal("want checksum error")
	}
}

func TestParseMalformedParts(t *testing.T) {
	base := func(extra map[string]string) map[string]string {
		m := map[string]string{"mimetype": mimetypeODT, partContent: contentDoc("", "")}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]map[string]string{
		"manifest": {partManifest: "<broken"},
		"styles":   {partStyles: "<broken"},
		"meta":     {partMeta: "<broken"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(zipODT(base(extra))); err == nil {
				t.Fatalf("want error for malformed %s", name)
			}
		})
	}
}

// TestParseMalformedContent truncates content.xml at many depths so every
// decoder error path in the walk is exercised.
func TestParseMalformedContent(t *testing.T) {
	head := xmlDecl + `<office:document-content ` + odfNamespaces + `>`
	as := `<office:automatic-styles>`
	body := `<office:body><office:text>`
	bodies := []string{
		// parseContent top-level syntax error, before body/styles.
		head + `<<<`,
		// collectStylesContainer: unterminated container.
		head + as,
		// collectStylesContainer default Skip on a malformed unknown child.
		head + as + `<style:zz>`,
		// collectStyle: unterminated style element.
		head + as + `<style:style style:name="x">`,
		// collectStyle: malformed child of a style element.
		head + as + `<style:style style:name="x"><style:text-properties>`,
		// collectListStyle: unterminated list style.
		head + as + `<text:list-style style:name="L">`,
		// collectListStyle: malformed child.
		head + as + `<text:list-style style:name="L"><text:list-level-style-number>`,
		// parseBlocks: unterminated office:text.
		head + body,
		// parseBlock default (unknown) captureRaw Skip error.
		head + body + `<w:weird>`,
		// parseHeading -> parseInlines error (also exercises one()).
		head + body + `<text:h>`,
		// parseParagraph thematic-break Skip error.
		head + body + `<text:p odfgo:thematic-break="true">`,
		// parseParagraph math -> readFlatText error.
		head + body + `<text:p odfgo:math="block">`,
		// parseParagraph math -> readFlatText Skip error on a child element.
		head + body + `<text:p odfgo:math="block"><x>`,
		// parseParagraph code -> readFlatText error.
		head + body + `<text:p odfgo:code-language="go">`,
		// parseParagraph plain -> parseInlines error.
		head + body + `<text:p>`,
		// parseSection -> parseBlocks error.
		head + body + `<text:section text:name="s">`,
		// parseList: unterminated list.
		head + body + `<text:list>`,
		// parseList: unknown child Skip error.
		head + body + `<text:list><zz>`,
		// parseList -> list-item parseBlocks error.
		head + body + `<text:list><text:list-item>`,
		// parseTable: unterminated table.
		head + body + `<table:table>`,
		// parseTable: default Skip error on malformed child.
		head + body + `<table:table><zz>`,
		// parseTable -> parseRows (header) error.
		head + body + `<table:table><table:table-header-rows>`,
		// parseRows: default Skip error.
		head + body + `<table:table><table:table-header-rows><zz>`,
		// parseRows -> parseRow error.
		head + body + `<table:table><table:table-header-rows><table:table-row>`,
		// parseTable -> parseRow (body) error.
		head + body + `<table:table><table:table-row>`,
		// parseRow: default Skip error.
		head + body + `<table:table><table:table-row><zz>`,
		// parseRow -> parseCell error.
		head + body + `<table:table><table:table-row><table:table-cell>`,
		// parseCell: default Skip error.
		head + body + `<table:table><table:table-row><table:table-cell><zz>`,
		// parseCell -> paragraph parseInlines error.
		head + body + `<table:table><table:table-row><table:table-cell><text:p>`,
		// parseInlines -> parseInlineElement (span) error.
		head + body + `<text:p><text:span>`,
		// parseInlineElement line-break Skip error.
		head + body + `<text:p><text:line-break>`,
		// parseInlineElement tab Skip error.
		head + body + `<text:p><text:tab>`,
		// parseInlineElement s Skip error.
		head + body + `<text:p><text:s>`,
		// parseInlineElement default captureRaw Skip error.
		head + body + `<text:p><z:z>`,
		// parseSpan math -> readFlatText error.
		head + body + `<text:p><text:span odfgo:math="inline">`,
		// parseSpan mono -> readFlatText error.
		head + body + `<text:p><text:span text:style-name="` + styleMono + `">`,
		// parseLink -> parseInlines error.
		head + body + `<text:p><text:a xlink:href="u">`,
		// parseImage: unterminated frame.
		head + body + `<text:p><draw:frame>`,
		// parseImage: image child Skip error.
		head + body + `<text:p><draw:frame><draw:image>`,
		// parseImage: svg:title readFlatText error.
		head + body + `<text:p><draw:frame><svg:title>`,
		// parseImage: svg:desc readFlatText error.
		head + body + `<text:p><draw:frame><svg:desc>`,
		// parseImage: default Skip error on a malformed child.
		head + body + `<text:p><draw:frame><zz>`,
		// parseInlineElement bookmark Skip error (unterminated bookmark).
		head + body + `<text:p><text:bookmark>`,
		// parseInlineElement bookmark-end Skip error.
		head + body + `<text:p><text:bookmark-end>`,
		// parseNote: unterminated note (Token error).
		head + body + `<text:p><text:note>`,
		// parseNote: note-body parseBlocks error.
		head + body + `<text:p><text:note><text:note-body>`,
		// parseNote: note-citation Skip error.
		head + body + `<text:p><text:note><text:note-citation>`,
		// parseRef: reference-ref parseInlines error.
		head + body + `<text:p><text:reference-ref>`,
	}
	for i, b := range bodies {
		src := zipODT(map[string]string{"mimetype": mimetypeODT, partContent: b})
		if _, err := Parse(src); err == nil {
			t.Errorf("case %d: expected error for %q", i, b)
		}
	}
}

// TestParseMalformedStylesFile drives the scanStyles error branch and its style
// collectors from styles.xml (rather than content.xml automatic styles).
func TestParseMalformedStylesFile(t *testing.T) {
	head := `<office:document-styles ` + odfNamespaces + `>`
	cases := []string{
		// scanStyles top-level syntax error.
		`<office:document-styles><<<`,
		// scanStyles -> collectStyle error (unterminated style).
		head + `<style:style style:name="x">`,
		// scanStyles -> collectListStyle error (unterminated list style).
		head + `<text:list-style style:name="L">`,
	}
	for i, styles := range cases {
		src := zipODT(map[string]string{
			"mimetype":  mimetypeODT,
			partContent: contentDoc("", ""),
			partStyles:  styles,
		})
		if _, err := Parse(src); err == nil {
			t.Errorf("case %d: want error for malformed styles.xml", i)
		}
	}
}

// TestParseMalformedMonoSpan truncates a span whose resolved style is
// monospace, driving the Code-span read error path.
func TestParseMalformedMonoSpan(t *testing.T) {
	content := xmlDecl + `<office:document-content ` + odfNamespaces + `>` +
		`<office:automatic-styles>` + constAutoStyles + `</office:automatic-styles>` +
		`<office:body><office:text><text:p><text:span text:style-name="` + styleMono + `">`
	src := zipODT(map[string]string{"mimetype": mimetypeODT, partContent: content})
	if _, err := Parse(src); err == nil {
		t.Fatal("want error for truncated monospace span")
	}
}

// TestParseMalformedMeta truncates metadata elements, driving the readElemText
// error paths for both Dublin Core and user-defined fields.
func TestParseMalformedMeta(t *testing.T) {
	head := xmlDecl + `<office:document-meta ` + odfNamespaces + `><office:meta>`
	cases := []string{
		head + `<dc:title>`,
		head + `<meta:user-defined meta:name="x">`,
	}
	for i, meta := range cases {
		src := zipODT(map[string]string{
			"mimetype":  mimetypeODT,
			partContent: contentDoc("", ""),
			partMeta:    meta,
		})
		if _, err := Parse(src); err == nil {
			t.Errorf("case %d: want error for malformed meta.xml", i)
		}
	}
}

func TestWriteHeadingLevelClamp(t *testing.T) {
	doc := richdoc.New().H(0, richdoc.Txt("lo")).H(9, richdoc.Txt("hi")).Doc()
	got, err := Parse(mustWrite(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if h := got.Blocks[0].(richdoc.Heading); h.Level != 1 {
		t.Fatalf("level 0 should clamp to 1, got %d", h.Level)
	}
	if h := got.Blocks[1].(richdoc.Heading); h.Level != 6 {
		t.Fatalf("level 9 should clamp to 6, got %d", h.Level)
	}
}

func TestWriteListStartClamp(t *testing.T) {
	doc := &richdoc.Document{Blocks: []richdoc.Block{
		richdoc.List{Ordered: true, Start: 0, Items: []richdoc.ListItem{
			richdoc.Item(richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("x")}}),
		}},
	}}
	got, err := Parse(mustWrite(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if l := got.Blocks[0].(richdoc.List); l.Start != 1 {
		t.Fatalf("start 0 should clamp to 1, got %d", l.Start)
	}
}

func mustWrite(t *testing.T, d *richdoc.Document) []byte {
	t.Helper()
	out, err := Write(d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- hand-authored valid parses -------------------------------------------

func TestParseNamedStylesFromStylesXML(t *testing.T) {
	// A named style with a parent chain, defined in styles.xml, resolved by a
	// span in content.xml. Base is italic; child adds bold -> Strong{Emph}.
	styles := `<office:document-styles ` + odfNamespaces + `>` +
		`<office:styles>` +
		`<style:style style:name="Base" style:family="text"><style:text-properties fo:font-style="italic"/></style:style>` +
		`<style:style style:name="Child" style:family="text" style:parent-style-name="Base"><style:text-properties fo:font-weight="bold"/></style:style>` +
		`<text:list-style style:name="LNum"><text:list-level-style-number text:level="1" text:start-value="5"/></text:list-style>` +
		`</office:styles></office:document-styles>`
	body := `<text:p><text:span text:style-name="Child">x</text:span></text:p>` +
		`<text:list text:style-name="LNum"><text:list-item><text:p>i</text:p></text:list-item></text:list>`
	src := zipODT(map[string]string{
		"mimetype":  mimetypeODT,
		partContent: contentDoc("", body),
		partStyles:  styles,
	})
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	para := d.Blocks[0].(richdoc.Paragraph)
	if _, ok := para.Inlines[0].(richdoc.Strong); !ok {
		t.Fatalf("want Strong from parent chain, got %T", para.Inlines[0])
	}
	list := d.Blocks[1].(richdoc.List)
	if !list.Ordered || list.Start != 5 {
		t.Fatalf("want ordered list start=5, got ordered=%v start=%d", list.Ordered, list.Start)
	}
}

func TestParseStyleCycleAndUnknown(t *testing.T) {
	// A parent-style cycle must terminate; an undefined style resolves to no
	// formatting. Both spans reduce to their text.
	styles := `<style:style style:name="A" style:family="text" style:parent-style-name="B"/>` +
		`<style:style style:name="B" style:family="text" style:parent-style-name="A"/>`
	body := `<text:p><text:span text:style-name="A">cyc</text:span><text:span text:style-name="Undef">unk</text:span></text:p>`
	d := parseOK(t, styles, body)
	para := d.Blocks[0].(richdoc.Paragraph)
	if len(para.Inlines) != 2 {
		t.Fatalf("want 2 inlines, got %d", len(para.Inlines))
	}
	if txt, ok := para.Inlines[0].(richdoc.Text); !ok || txt.Value != "cyc" {
		t.Fatalf("want flattened text, got %#v", para.Inlines[0])
	}
}

func TestParseLineThroughNone(t *testing.T) {
	styles := `<style:style style:name="NoStrike" style:family="text"><style:text-properties style:text-line-through-style="none"/></style:style>`
	body := `<text:p><text:span text:style-name="NoStrike">x</text:span></text:p>`
	d := parseOK(t, styles, body)
	para := d.Blocks[0].(richdoc.Paragraph)
	if _, ok := para.Inlines[0].(richdoc.Text); !ok {
		t.Fatalf("line-through none must not be Strikethrough, got %T", para.Inlines[0])
	}
}

func TestParseWhitespaceAndTabInline(t *testing.T) {
	body := `<text:p>a<text:tab/>b<text:s text:c="3"/>c<text:s/>d</text:p>`
	d := parseOK(t, "", body)
	got := richdoc.PlainText(d)
	if got != "a\tb   c d" {
		t.Fatalf("whitespace mapping = %q", got)
	}
}

func TestParseListHeaderAndStray(t *testing.T) {
	body := `<text:list text:style-name="L">` +
		`<text:list-header><text:p>lead</text:p></text:list-header>` +
		`<text:list-item><text:p>item</text:p></text:list-item>` +
		`<text:tracked-changes/>` +
		`</text:list>`
	d := parseOK(t, "", body)
	list := d.Blocks[0].(richdoc.List)
	if len(list.Items) != 2 {
		t.Fatalf("want 2 items (header + item), got %d", len(list.Items))
	}
}

func TestParseListBadStartValue(t *testing.T) {
	styles := `<text:list-style style:name="LB"><text:list-level-style-number text:start-value="notanumber"/></text:list-style>`
	body := `<text:list text:style-name="LB"><text:list-item><text:p>x</text:p></text:list-item></text:list>`
	d := parseOK(t, styles, body)
	list := d.Blocks[0].(richdoc.List)
	if !list.Ordered || list.Start != 1 {
		t.Fatalf("bad start-value should default to 1, got ordered=%v start=%d", list.Ordered, list.Start)
	}
}

func TestParseUnmarkedSectionFlattens(t *testing.T) {
	body := `<text:section text:name="Sect1"><text:p>inner</text:p></text:section>`
	d := parseOK(t, "", body)
	if len(d.Blocks) != 1 {
		t.Fatalf("want 1 flattened block, got %d", len(d.Blocks))
	}
	if _, ok := d.Blocks[0].(richdoc.Paragraph); !ok {
		t.Fatalf("want Paragraph, got %T", d.Blocks[0])
	}
}

func TestParseTableStrayAndMultiHeader(t *testing.T) {
	body := `<table:table table:name="T">` +
		`<table:table-column table:number-columns-repeated="1"/>` +
		`<table:table-header-rows>` +
		`<table:table-row><table:table-cell><text:p>H</text:p></table:table-cell></table:table-row>` +
		`<table:table-row><table:table-cell><text:p>H2</text:p></table:table-cell></table:table-row>` +
		`<x:stray/>` +
		`</table:table-header-rows>` +
		`<table:table-row>` +
		`<x:stray/>` +
		`<table:table-cell><x:stray/><text:p>a</text:p><text:p>b</text:p></table:table-cell>` +
		`<table:covered-table-cell/>` +
		`</table:table-row>` +
		`<x:stray/>` +
		`</table:table>`
	d := parseOK(t, "", body)
	tbl := d.Blocks[0].(richdoc.Table)
	if len(tbl.Header) != 1 {
		t.Fatalf("want 1 header cell, got %d", len(tbl.Header))
	}
	// Two body rows: the extra header row and the real body row.
	if len(tbl.Rows) != 2 {
		t.Fatalf("want 2 body rows, got %d", len(tbl.Rows))
	}
	// The multi-paragraph cell is joined with a LineBreak.
	cell := tbl.Rows[1][0]
	if len(cell.Inlines) != 3 {
		t.Fatalf("want 3 inlines (a, break, b), got %d", len(cell.Inlines))
	}
}

func TestParsePlainSpanAndUnknownInline(t *testing.T) {
	body := `<text:p><text:span>plain</text:span><text:note-ref>ref</text:note-ref></text:p>`
	d := parseOK(t, "", body)
	para := d.Blocks[0].(richdoc.Paragraph)
	if txt, ok := para.Inlines[0].(richdoc.Text); !ok || txt.Value != "plain" {
		t.Fatalf("want plain text from style-less span, got %#v", para.Inlines[0])
	}
	if _, ok := para.Inlines[1].(richdoc.RawInline); !ok {
		t.Fatalf("want RawInline for unknown element, got %T", para.Inlines[1])
	}
}

// TestParseBookmarkVariants covers the range form of bookmarks: a
// text:bookmark-start yields a point Anchor and a stray text:bookmark-end is
// consumed without producing a node.
func TestParseBookmarkVariants(t *testing.T) {
	body := `<text:p>a<text:bookmark-start text:name="r"/>b<text:bookmark-end text:name="r"/>c</text:p>`
	d := parseOK(t, "", body)
	para := d.Blocks[0].(richdoc.Paragraph)
	// a, Anchor{r}, b, c -> the bookmark-end adds nothing.
	var anchors int
	var text string
	for _, in := range para.Inlines {
		switch n := in.(type) {
		case richdoc.Anchor:
			anchors++
			if n.ID != "r" {
				t.Fatalf("anchor id = %q, want r", n.ID)
			}
		case richdoc.Text:
			text += n.Value
		}
	}
	if anchors != 1 {
		t.Fatalf("want 1 anchor, got %d", anchors)
	}
	if text != "abc" {
		t.Fatalf("visible text = %q, want abc", text)
	}
}

// TestParseReferenceRefLabelAndCite covers the reference-ref parse element (the
// alternate of bookmark-ref) for both a plain label and an odfgo:cite citation.
func TestParseReferenceRefLabelAndCite(t *testing.T) {
	body := `<text:p>` +
		`<text:reference-ref text:ref-name="lbl">L</text:reference-ref>` +
		`<text:reference-ref text:ref-name="key" odfgo:cite="true">C</text:reference-ref>` +
		`</text:p>`
	d := parseOK(t, "", body)
	para := d.Blocks[0].(richdoc.Paragraph)
	r0 := para.Inlines[0].(richdoc.CrossRef)
	if r0.Target != "lbl" || r0.Kind != richdoc.RefLabel {
		t.Fatalf("ref 0 = %+v, want label lbl", r0)
	}
	r1 := para.Inlines[1].(richdoc.CrossRef)
	if r1.Target != "key" || r1.Kind != richdoc.RefCite {
		t.Fatalf("ref 1 = %+v, want cite key", r1)
	}
}

// TestParseEndnoteNormalizesToFootnote covers the endnote note-class mapping to
// a Footnote (the note-class attribute is not preserved).
func TestParseEndnoteNormalizesToFootnote(t *testing.T) {
	body := `<text:p><text:note text:id="en1" text:note-class="endnote">` +
		`<text:note-citation>i</text:note-citation>` +
		`<text:note-body><text:p>end</text:p></text:note-body>` +
		`</text:note></text:p>`
	d := parseOK(t, "", body)
	fn := d.Blocks[0].(richdoc.Paragraph).Inlines[0].(richdoc.Footnote)
	if got := richdoc.PlainText(&richdoc.Document{Blocks: fn.Blocks}); got != "end" {
		t.Fatalf("footnote body = %q, want end", got)
	}
}

// TestWriteAnchorWithInlines covers writing an Anchor that labels visible
// content: the bookmark is a point marker and the inlines are written next to
// it, so no text is lost even though the round-trip detaches them.
func TestWriteAnchorWithInlines(t *testing.T) {
	doc := richdoc.New().P(richdoc.Mark("m", richdoc.Txt("visible"))).Doc()
	out := mustWrite(t, doc)
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, f := range zr.File {
		if f.Name == partContent {
			data, _ := slurp(f)
			content = string(data)
		}
	}
	if !strings.Contains(content, `<text:bookmark text:name="m"/>visible`) {
		t.Fatalf("anchor inlines not written adjacent: %s", content)
	}
	// Re-parse: the visible text survives as a sibling Text node.
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if pt := richdoc.PlainText(got); pt != "visible" {
		t.Fatalf("plain text = %q, want visible", pt)
	}
}

func TestParseHeadingLevelClamp(t *testing.T) {
	body := `<text:h text:outline-level="9">Deep</text:h><text:h text:outline-level="0">Zero</text:h><text:h>None</text:h>`
	d := parseOK(t, "", body)
	if h := d.Blocks[0].(richdoc.Heading); h.Level != 6 {
		t.Fatalf("outline-level 9 should clamp to 6, got %d", h.Level)
	}
	if h := d.Blocks[1].(richdoc.Heading); h.Level != 1 {
		t.Fatalf("outline-level 0 should default to 1, got %d", h.Level)
	}
	if h := d.Blocks[2].(richdoc.Heading); h.Level != 1 {
		t.Fatalf("missing outline-level should default to 1, got %d", h.Level)
	}
}

func TestParseEmbeddedImageWithoutManifestMedia(t *testing.T) {
	// No manifest media type: the media type is inferred from the extension.
	pic := "Pictures/photo.gif"
	src := zipODT(map[string]string{
		"mimetype":  mimetypeODT,
		partContent: contentDoc("", `<text:p><draw:frame><draw:image xlink:href="`+pic+`"/></draw:frame></text:p>`),
		pic:         "GIFDATA",
	})
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	img := d.Blocks[0].(richdoc.Paragraph).Inlines[0].(richdoc.Image)
	if !strings.HasPrefix(img.URL, "data:image/gif;base64,") {
		t.Fatalf("want gif data URI inferred from extension, got %q", img.URL)
	}
}

func TestParseImageEmptyHref(t *testing.T) {
	body := `<text:p><draw:frame><draw:image xlink:href=""/></draw:frame></text:p>`
	d := parseOK(t, "", body)
	img := d.Blocks[0].(richdoc.Paragraph).Inlines[0].(richdoc.Image)
	if img.URL != "" {
		t.Fatalf("empty href should yield empty URL, got %q", img.URL)
	}
}

func TestParseMetaUserDefinedEmptyNameAndStray(t *testing.T) {
	meta := xmlDecl + `<office:document-meta ` + odfNamespaces + `><office:meta>` +
		`<dc:title>T</dc:title>` +
		`<meta:user-defined>ignored</meta:user-defined>` + // no name -> skipped
		`<meta:generator>go-odf</meta:generator>` + // non-dc, non-user-defined -> ignored
		`</office:meta></office:document-meta>`
	src := zipODT(map[string]string{
		"mimetype":  mimetypeODT,
		partContent: contentDoc("", ""),
		partMeta:    meta,
	})
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Meta["title"] != "T" || len(d.Meta) != 1 {
		t.Fatalf("meta = %#v, want only title", d.Meta)
	}
}

func TestParseEmptyMetaYieldsNil(t *testing.T) {
	meta := xmlDecl + `<office:document-meta ` + odfNamespaces + `><office:meta></office:meta></office:document-meta>`
	src := zipODT(map[string]string{
		"mimetype":  mimetypeODT,
		partContent: contentDoc("", ""),
		partMeta:    meta,
	})
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Meta != nil {
		t.Fatalf("empty meta should be nil, got %#v", d.Meta)
	}
}
