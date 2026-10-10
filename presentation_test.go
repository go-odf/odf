// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-richdoc/richdoc"
)

// ⛔ These two files were WRITTEN BY LIBREOFFICE, not by this test. A fixture I
// compose myself tests my reading of the specification; these test the thing
// the reader will actually be handed. Four of the rules in presentation.go
// exist only because a real file disagreed with the specification — most
// sharply over presentation:class, which Impress strips from every frame it
// does not treat as a master-page placeholder, so the obvious
// "the title is the frame classed title" finds nothing at all here.
const (
	deckPath = "testdata/deck.odp"
	drawPath = "testdata/draw.odg"
)

// text flattens a document to the words in it, in order, with a marker for
// each heading so a test can assert WHICH page a sentence was on.
func text(doc *richdoc.Document) string {
	var b strings.Builder
	writeBlocks(&b, doc.Blocks)
	return b.String()
}

func writeInlines(b *strings.Builder, is []richdoc.Inline) {
	for _, i := range is {
		switch v := i.(type) {
		case richdoc.Text:
			b.WriteString(v.Value)
		case richdoc.Image:
			b.WriteString("[img " + v.URL + "]")
		case richdoc.Emph:
			writeInlines(b, v.Inlines)
		case richdoc.Strong:
			writeInlines(b, v.Inlines)
		}
	}
}

func writeBlocks(b *strings.Builder, bs []richdoc.Block) {
	for _, bl := range bs {
		switch v := bl.(type) {
		case richdoc.Heading:
			b.WriteString("\n# ")
			writeInlines(b, v.Inlines)
			b.WriteString("\n")
		case richdoc.Paragraph:
			writeInlines(b, v.Inlines)
			b.WriteString("\n")
		case richdoc.List:
			for _, it := range v.Items {
				b.WriteString("- ")
				writeBlocks(b, it.Blocks)
			}
		case richdoc.Table:
			for _, r := range v.Rows {
				for _, c := range r {
					// ⛔ Content(), never .Blocks: a Cell holds EITHER Inlines
					// or Blocks, and the wrong field compiles, type-checks and
					// reads back empty. This helper did exactly that and
					// reported an empty table that was in fact full — an
					// instrument's defect read as the reader's.
					b.WriteString("|" + strings.TrimSpace(render(c.Content())))
				}
				b.WriteString("|\n")
			}
		case richdoc.RawBlock:
			b.WriteString("[raw " + v.Format + "]\n")
		}
	}
}

// render is writeBlocks into a builder of its own, for the places that need a
// block's text as a VALUE — a table cell, whose newlines must not reach the
// row.
func render(bs []richdoc.Block) string {
	var b strings.Builder
	writeBlocks(&b, bs)
	return b.String()
}

func parseFile(t *testing.T, path string) *richdoc.Document {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return doc
}

func TestPresentationFromLibreOffice(t *testing.T) {
	got := text(parseFile(t, deckPath))

	// Every slide is separated by its own name, so a reader can tell which
	// slide a sentence was on. LibreOffice kept the names through the
	// conversion; presentation:class did not survive it.
	for _, want := range []string{
		"# Opening",
		"What a reader needs from a deck",
		"- the words, in order",
		"- which slide they were on",
		"# Numbers",
		"A table on a slide",
		"|format|tools|",
		"|odp|1|",
		"text in a shape", // a draw:custom-shape, which holds text:p with NO text-box
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the deck is missing %q\n--- got ---\n%s", want, got)
		}
	}

	// ⛔ Speaker notes are NOT on the slide. This line is in the file, inside
	// presentation:notes, and it reads exactly like body text — which is why
	// its absence has to be asserted rather than assumed.
	if strings.Contains(got, "Speaker note") {
		t.Errorf("a speaker note reached the document:\n%s", got)
	}

	// ⛔ Impress writes a draw:image PREVIEW of the table into the same frame
	// as the table. Emitting it would put a broken reference, in a format
	// nothing reads, beside the content it is only a picture of.
	if strings.Contains(got, "TablePreview") || strings.Contains(got, ".svm") {
		t.Errorf("a table's preview image reached the document:\n%s", got)
	}

	// Nothing fell through to a raw block: a raw block here would be content
	// this reader could not read, reported as though it had been read.
	if strings.Contains(got, "[raw") {
		t.Errorf("a shape came out raw:\n%s", got)
	}
}

func TestDrawingFromLibreOffice(t *testing.T) {
	got := text(parseFile(t, drawPath))
	// office:drawing is the same document as office:presentation: draw:page
	// holding shapes. A reader that handles one and not the other would be
	// refusing a file it can already read.
	for _, want := range []string{"# Opening", "What a reader needs from a deck", "# Numbers", "|format|tools|"} {
		if !strings.Contains(got, want) {
			t.Errorf("the drawing is missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "[raw") {
		t.Errorf("a shape came out raw:\n%s", got)
	}
}

// slideOrder asserts the words come out in the order they are on the page, and
// under the right slide. A set of assertions on CONTENT alone passes on a
// document that shuffled the two slides together.
func TestSlideOrder(t *testing.T) {
	got := text(parseFile(t, deckPath))
	opening := strings.Index(got, "# Opening")
	words := strings.Index(got, "the words, in order")
	numbers := strings.Index(got, "# Numbers")
	shape := strings.Index(got, "text in a shape")
	if !(opening < words && words < numbers && numbers < shape) {
		t.Errorf("the slides came out in the wrong order (%d, %d, %d, %d):\n%s",
			opening, words, numbers, shape, got)
	}
}

// deckPackage wraps a presentation body in a package, the way sheetPackage
// does for a spreadsheet.
func deckPackage(t *testing.T, body string) []byte {
	t.Helper()
	content := `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
 xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
 xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:xlink="http://www.w3.org/1999/xlink"
 xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
 office:version="1.3"><office:body><office:presentation>` + body +
		`</office:presentation></office:body></office:document-content>`
	return pack(t, "application/vnd.oasis.opendocument.presentation", content)
}

// ⛔ The WITNESS for the rule in parsePage, not a restatement of it. The
// comment says Impress drops presentation:class; this reads the real file and
// asserts it. Without this, "the title is the frame classed title" could be
// reintroduced by somebody reading the specification, and every test above
// would still pass — they asserted the words came out, and under draw:name
// they do.
func TestNoFrameOnARealDeckIsClassedTitle(t *testing.T) {
	data, err := os.ReadFile(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var content []byte
	for _, f := range zr.File {
		if f.Name == "content.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(content) == 0 {
		t.Fatal("content.xml is missing: this test proves nothing")
	}
	if bytes.Contains(content, []byte(`presentation:class="title"`)) {
		t.Error(`a real deck DOES carry presentation:class="title" — ` +
			`the rule in parsePage was argued from a file that no longer says this, so re-measure it`)
	}
	// The positive half: the names the heading rule relies on ARE there. A
	// test that only asserts an absence passes on an empty file.
	for _, want := range []string{`draw:name="Opening"`, `draw:name="Numbers"`} {
		if !bytes.Contains(content, []byte(want)) {
			t.Errorf("the real deck has no %s, so the heading rule has nothing to read", want)
		}
	}
}

// ⛔ A frame holding ONLY a picture must emit it. The held-back image exists so
// a table's preview is dropped; if it were dropped unconditionally, every
// picture on every slide would vanish and nothing above would notice.
func TestAPictureFrameEmitsItsPicture(t *testing.T) {
	doc, err := Parse(deckPackage(t, `<draw:page draw:name="P">
	 <draw:frame><draw:image xlink:href="Pictures/real.png"/></draw:frame>
	</draw:page>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := text(doc); !strings.Contains(got, "[img Pictures/real.png]") {
		t.Errorf("a picture frame lost its picture:\n%s", got)
	}
}

// And the other side of the same rule, on a hand-made file so the structure is
// unambiguous: a frame that holds a table AND a preview of it yields only the
// table.
func TestATablesPreviewIsDropped(t *testing.T) {
	doc, err := Parse(deckPackage(t, `<draw:page draw:name="P">
	 <draw:frame>
	  <table:table><table:table-row><table:table-cell><text:p>kept</text:p></table:table-cell></table:table-row></table:table>
	  <draw:image xlink:href="Pictures/TablePreview1.svm"/>
	 </draw:frame>
	</draw:page>`))
	if err != nil {
		t.Fatal(err)
	}
	got := text(doc)
	if !strings.Contains(got, "|kept|") {
		t.Errorf("the table was lost:\n%s", got)
	}
	if strings.Contains(got, "TablePreview") {
		t.Errorf("the preview was emitted beside the table it is a picture of:\n%s", got)
	}
}

// Text in an arbitrary shape type. ⛔ parseShape deliberately does NOT
// enumerate the shapes; this asserts that, with a shape nobody would think to
// list.
func TestTextInAnUnlistedShape(t *testing.T) {
	doc, err := Parse(deckPackage(t, `<draw:page draw:name="P">
	 <draw:g><draw:caption><text:p>inside a caption inside a group</text:p></draw:caption></draw:g>
	</draw:page>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := text(doc); !strings.Contains(got, "inside a caption inside a group") {
		t.Errorf("text in an unlisted shape was dropped:\n%s", got)
	}
}

func TestTooManyPagesIsRefused(t *testing.T) {
	body := strings.Repeat(`<draw:page draw:name="p"/>`, maxPages+1)
	_, err := Parse(deckPackage(t, body))
	var tooMany errTooManyPages
	if !errors.As(err, &tooMany) {
		t.Fatalf("a document of %d pages was not refused: %v", maxPages+1, err)
	}
	// ⛔ The message must say the LIMIT. It cannot say the count: the walk
	// stops at the ceiling rather than reading to the end of a file that may
	// not have one.
	if msg := err.Error(); !strings.Contains(msg, strconv.Itoa(maxPages)) {
		t.Errorf("the refusal does not say what was allowed: %q", msg)
	}
}

// Truncation inside a page, inside a shape, and between pages: each is a
// different xml.Decoder call, and a reader that returned nil there would
// report a short document as a complete one.
func TestTruncationIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"between pages":  `<draw:page draw:name="a"/><draw:pa`,
		"inside a page":  `<draw:page draw:name="a"><draw:fra`,
		"in a shape":     `<draw:page draw:name="a"><draw:frame><text:p>x</text:p>`,
		"before a page":  `<presentation:settings`,
		"in settings":    `<presentation:settings><sub`,
		"in a note":      `<draw:page draw:name="a"><presentation:notes><sub`,
		"in a paragraph": `<draw:page draw:name="a"><draw:frame><text:p><text:span`,
		"in an image":    `<draw:page draw:name="a"><draw:frame><draw:image><sub`,
		"in a svg title": `<draw:page draw:name="a"><draw:frame><svg:title><sub`,
		"in a svg desc":  `<draw:page draw:name="a"><draw:frame><svg:desc><sub`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(deckPackage(t, body)); err == nil {
				t.Error("a truncated document parsed without an error")
			}
		})
	}
}

// ⛔ svg:title and svg:desc are the frame's own children, BESIDE draw:image
// rather than inside it. A reader that looked for them within the image
// element would find none, and a picture on a slide would reach the document
// with no alternative text — which no page count, byte count or block count
// can see.
func TestAPictureKeepsItsAlternativeText(t *testing.T) {
	doc, err := Parse(deckPackage(t, `<draw:page draw:name="P">
	 <draw:frame>
	  <svg:title>The lamp</svg:title>
	  <svg:desc>A worn scanner lamp, seen from above</svg:desc>
	  <draw:image xlink:href="Pictures/lamp.png"/>
	 </draw:frame>
	</draw:page>`))
	if err != nil {
		t.Fatal(err)
	}
	var img richdoc.Image
	for _, b := range doc.Blocks {
		if para, ok := b.(richdoc.Paragraph); ok {
			for _, in := range para.Inlines {
				if m, ok := in.(richdoc.Image); ok {
					img = m
				}
			}
		}
	}
	if img.URL == "" {
		t.Fatalf("the picture never reached the document: %s", text(doc))
	}
	if img.Title != "The lamp" {
		t.Errorf("title = %q, want %q", img.Title, "The lamp")
	}
	if img.Alt != "A worn scanner lamp, seen from above" {
		t.Errorf("alt = %q, want the svg:desc", img.Alt)
	}
}
