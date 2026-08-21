// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"io"
	"reflect"
	"testing"

	"github.com/go-richdoc/richdoc"
)

// pngBytes is an opaque image payload used to exercise embedded-image handling;
// the converter treats picture bytes as opaque, so it need not be a real PNG.
var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}

func dataURI(mt string, data []byte) string {
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func roundTripCases() map[string]*richdoc.Document {
	return map[string]*richdoc.Document{
		"empty": richdoc.New().Doc(),
		"headings": richdoc.New().
			H(1, richdoc.Txt("Title")).
			H(3, richdoc.Txt("Sub")).
			P(richdoc.Txt("Body text.")).
			Doc(),
		"inline-formatting": richdoc.New().
			P(
				richdoc.Bold(richdoc.Txt("bold")),
				richdoc.Txt(" "),
				richdoc.Italic(richdoc.Txt("italic")),
				richdoc.Txt(" "),
				richdoc.Strike(richdoc.Txt("struck")),
				richdoc.Txt(" "),
				richdoc.Mono("code()"),
			).Doc(),
		"nested-inline": richdoc.New().
			P(richdoc.Bold(richdoc.Italic(richdoc.Txt("bi")))).
			Doc(),
		"linebreak": richdoc.New().
			P(richdoc.Txt("a"), richdoc.Br(), richdoc.Txt("b")).
			Doc(),
		"links": richdoc.New().
			P(richdoc.Href("https://example.com/a", "", richdoc.Txt("plain"))).
			P(richdoc.Href("https://example.com/b", "tip", richdoc.Bold(richdoc.Txt("titled")))).
			Doc(),
		"lists": richdoc.New().
			UList(false, richdoc.Item(richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("one")}}),
				richdoc.Item(richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("two")}})).
			OList(3, true, richdoc.Item(richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("third")}})).
			Doc(),
		"nested-list": richdoc.New().
			UList(false, richdoc.Item(
				richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("outer")}},
				richdoc.List{Ordered: true, Start: 1, Items: []richdoc.ListItem{
					richdoc.Item(richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("inner")}}),
				}},
			)).Doc(),
		"table": richdoc.New().
			Table(
				[]richdoc.Alignment{richdoc.AlignLeft, richdoc.AlignRight},
				[]richdoc.Cell{richdoc.Td(richdoc.Txt("H1")), richdoc.Td(richdoc.Txt("H2"))},
				[][]richdoc.Cell{
					{richdoc.Td(richdoc.Txt("a")), richdoc.Td(richdoc.Bold(richdoc.Txt("b")))},
					{richdoc.Td(richdoc.Txt("c")), richdoc.Td()},
				},
			).Doc(),
		"table-plain": richdoc.New().
			Table(nil, nil, [][]richdoc.Cell{
				{richdoc.Td(richdoc.Txt("x")), richdoc.Td(richdoc.Txt("y"))},
			}).Doc(),
		"table-center": richdoc.New().
			Table([]richdoc.Alignment{richdoc.AlignCenter}, nil,
				[][]richdoc.Cell{{richdoc.Td(richdoc.Txt("m"))}}).Doc(),
		"codeblock": richdoc.New().
			CodeBlock("go", "package main\n\nfunc main() {}\n").
			CodeBlock("", "no-lang").
			Doc(),
		"blockquote": richdoc.New().
			Quote(
				richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("quoted")}},
				richdoc.BlockQuote{Blocks: []richdoc.Block{
					richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("nested quote")}},
				}},
			).Doc(),
		"thematic-break": richdoc.New().
			P(richdoc.Txt("above")).HR().P(richdoc.Txt("below")).Doc(),
		"math": richdoc.New().
			MathBlock("a^2 + b^2 = c^2").
			P(richdoc.Txt("inline "), richdoc.InlineMath("x_1")).
			Doc(),
		"image-embedded": richdoc.New().
			P(richdoc.Img(dataURI("image/png", pngBytes), "alt text", "the title")).
			Doc(),
		"image-embedded-unknown-media": richdoc.New().
			P(richdoc.Img(dataURI("image/webp", pngBytes), "", "")).
			Doc(),
		"image-external": richdoc.New().
			P(richdoc.Img("https://example.com/x.png", "ext", "")).
			Doc(),
		"image-bare-path": richdoc.New().
			P(richdoc.Img("Pictures/unresolved.png", "", "")).
			Doc(),
		"footnote": richdoc.New().
			P(
				richdoc.Txt("text"),
				richdoc.Note(
					richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("first note para")}},
					richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Bold(richdoc.Txt("second"))}},
				),
			).Doc(),
		"bookmark": richdoc.New().
			P(richdoc.Txt("at "), richdoc.Mark("anchor1"), richdoc.Txt(" here")).
			Doc(),
		"crossref-label": richdoc.New().
			P(richdoc.Txt("see "), richdoc.Ref("anchor1", richdoc.Txt("Fig. 1"))).
			Doc(),
		"crossref-cite": richdoc.New().
			P(richdoc.Txt("as in "), richdoc.Cite("knuth1997", richdoc.Txt("[1]"))).
			Doc(),
		"raw": richdoc.New().
			// A table-of-content block and a reference-mark inline have no model
			// node and still round-trip verbatim through Raw.
			Add(richdoc.RawBlock{Format: "odf", Text: `<text:table-of-content text:name="Table1"><text:index-body><text:p>TOC</text:p></text:index-body></text:table-of-content>`}).
			P(richdoc.Txt("see "), richdoc.RawInline{Format: "odf", Text: `<text:reference-mark text:name="mark"></text:reference-mark>`}, richdoc.Txt(" here")).
			Doc(),
		"raw-dropped-other-format": richdoc.New().
			// Raw nodes for a foreign format are dropped, like other converters.
			Add(richdoc.RawBlock{Format: "latex", Text: `\foo`}).
			P(richdoc.Txt("kept"), richdoc.RawInline{Format: "latex", Text: `\bar`}).
			Doc(),
		"escaping": richdoc.New().
			P(richdoc.Txt("angle < & > \"quote\" and\ttab")).
			P(richdoc.Href("https://ex.com/?a=1&b=2", "t<i&t>le", richdoc.Txt("l"))).
			Doc(),
		"meta": func() *richdoc.Document {
			d := richdoc.New().P(richdoc.Txt("with meta")).Doc()
			d.Meta = map[string]string{"title": "T", "author": "A", "project": "Zeta"}
			return d
		}(),
	}
}

func TestRoundTrip(t *testing.T) {
	for name, doc := range roundTripCases() {
		t.Run(name, func(t *testing.T) {
			out, err := Write(doc)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			// Expected result: raw nodes for formats other than "odf" are dropped.
			want := dropForeignRaw(doc)
			if !reflect.DeepEqual(norm(want), norm(got)) {
				t.Fatalf("round-trip mismatch\n want: %s\n  got: %s", dump(norm(want)), dump(norm(got)))
			}
		})
	}
}

// TestRoundTripStable verifies Parse(Write(doc)) == doc a second time, proving
// the mapping is a fixed point.
func TestRoundTripStable(t *testing.T) {
	for name, doc := range roundTripCases() {
		t.Run(name, func(t *testing.T) {
			out, err := Write(dropForeignRaw(doc))
			if err != nil {
				t.Fatal(err)
			}
			d1, err := Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			out2, err := Write(d1)
			if err != nil {
				t.Fatal(err)
			}
			d2, err := Parse(out2)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(norm(d1), norm(d2)) {
				t.Fatalf("not stable\n d1: %s\n d2: %s", dump(norm(d1)), dump(norm(d2)))
			}
		})
	}
}

func TestWriteNilDocument(t *testing.T) {
	out, err := Write(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 0 {
		t.Fatalf("expected empty doc, got %d blocks", len(got.Blocks))
	}
}

// TestPackageValidity asserts the ODF packaging invariants and structural
// shape. A real office-suite open is not automatable here, so validity is
// established by these structural + well-formedness + re-parse checks.
func TestPackageValidity(t *testing.T) {
	doc := roundTripCases()["image-embedded"]
	out, err := Write(doc)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}

	// mimetype MUST be the first entry, stored uncompressed, with the exact
	// media type.
	first := zr.File[0]
	if first.Name != "mimetype" {
		t.Fatalf("first entry = %q, want mimetype", first.Name)
	}
	if first.Method != zip.Store {
		t.Fatalf("mimetype method = %d, want Store(0)", first.Method)
	}
	rc, _ := first.Open()
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != mimetypeODT {
		t.Fatalf("mimetype content = %q, want %q", body, mimetypeODT)
	}
	if len(first.Extra) != 0 {
		t.Fatalf("mimetype entry must have no extra field, got %d bytes", len(first.Extra))
	}

	// Required parts present and well-formed XML.
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, req := range []string{"content.xml", "styles.xml", "META-INF/manifest.xml", "Pictures/img1.png"} {
		if !names[req] {
			t.Fatalf("missing required part %q", req)
		}
	}
	for _, f := range zr.File {
		if f.Name == "mimetype" || f.Name == "Pictures/img1.png" {
			continue
		}
		data, _ := slurp(f)
		if err := wellFormed(data); err != nil {
			t.Fatalf("%s is not well-formed XML: %v", f.Name, err)
		}
	}
}

func wellFormed(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// --- semantic equality helpers -------------------------------------------

func dump(d *richdoc.Document) string { return richdoc.PlainText(d) + reflectString(d) }

func reflectString(d *richdoc.Document) string {
	var b bytes.Buffer
	writeVal(&b, reflect.ValueOf(d))
	return b.String()
}

func writeVal(b *bytes.Buffer, v reflect.Value) {
	if !v.IsValid() {
		b.WriteString("<nil>")
		return
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			b.WriteString("<nil>")
			return
		}
		writeVal(b, v.Elem())
	case reflect.Slice:
		b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			writeVal(b, v.Index(i))
		}
		b.WriteByte(']')
	case reflect.Struct:
		b.WriteString(v.Type().Name())
		b.WriteByte('{')
		for i := 0; i < v.NumField(); i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			writeVal(b, v.Field(i))
		}
		b.WriteByte('}')
	default:
		b.WriteString(v.String())
	}
}

// norm returns a deep copy of d with every empty slice normalized to nil and
// empty metadata dropped, so reflect.DeepEqual ignores the nil/empty-slice
// distinction.
func norm(d *richdoc.Document) *richdoc.Document {
	if d == nil {
		return nil
	}
	out := &richdoc.Document{Blocks: normBlocks(d.Blocks)}
	if len(d.Meta) > 0 {
		out.Meta = d.Meta
	}
	return out
}

func normBlocks(bs []richdoc.Block) []richdoc.Block {
	if len(bs) == 0 {
		return nil
	}
	out := make([]richdoc.Block, len(bs))
	for i, b := range bs {
		out[i] = normBlock(b)
	}
	return out
}

func normBlock(b richdoc.Block) richdoc.Block {
	switch n := b.(type) {
	case richdoc.Heading:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.Paragraph:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.List:
		items := make([]richdoc.ListItem, len(n.Items))
		for i, it := range n.Items {
			items[i] = richdoc.ListItem{Blocks: normBlocks(it.Blocks)}
		}
		if len(items) == 0 {
			items = nil
		}
		n.Items = items
		return n
	case richdoc.BlockQuote:
		n.Blocks = normBlocks(n.Blocks)
		return n
	case richdoc.Table:
		return normTable(n)
	default:
		return b
	}
}

func normTable(t richdoc.Table) richdoc.Table {
	out := richdoc.Table{Header: normCells(t.Header)}
	if len(t.Align) > 0 {
		out.Align = t.Align
	}
	if len(t.Rows) > 0 {
		out.Rows = make([][]richdoc.Cell, len(t.Rows))
		for i, row := range t.Rows {
			out.Rows[i] = normCells(row)
		}
	}
	return out
}

func normCells(cells []richdoc.Cell) []richdoc.Cell {
	if len(cells) == 0 {
		return nil
	}
	out := make([]richdoc.Cell, len(cells))
	for i, c := range cells {
		out[i] = richdoc.Cell{Inlines: normInlines(c.Inlines)}
	}
	return out
}

func normInlines(ins []richdoc.Inline) []richdoc.Inline {
	if len(ins) == 0 {
		return nil
	}
	out := make([]richdoc.Inline, len(ins))
	for i, in := range ins {
		out[i] = normInline(in)
	}
	return out
}

func normInline(in richdoc.Inline) richdoc.Inline {
	switch n := in.(type) {
	case richdoc.Emph:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.Strong:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.Strikethrough:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.Link:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.Footnote:
		n.Blocks = normBlocks(n.Blocks)
		return n
	case richdoc.Anchor:
		n.Inlines = normInlines(n.Inlines)
		return n
	case richdoc.CrossRef:
		n.Inlines = normInlines(n.Inlines)
		return n
	default:
		return in
	}
}

// dropForeignRaw returns a copy of d without raw nodes whose format is not
// "odf"; Write drops those, so the expected round-trip result omits them.
func dropForeignRaw(d *richdoc.Document) *richdoc.Document {
	out := &richdoc.Document{Meta: d.Meta}
	out.Blocks = dropBlocks(d.Blocks)
	return out
}

func dropBlocks(bs []richdoc.Block) []richdoc.Block {
	var out []richdoc.Block
	for _, b := range bs {
		switch n := b.(type) {
		case richdoc.RawBlock:
			if n.Format == "odf" {
				out = append(out, n)
			}
		case richdoc.Paragraph:
			n.Inlines = dropInlines(n.Inlines)
			out = append(out, n)
		case richdoc.Heading:
			n.Inlines = dropInlines(n.Inlines)
			out = append(out, n)
		case richdoc.BlockQuote:
			n.Blocks = dropBlocks(n.Blocks)
			out = append(out, n)
		case richdoc.List:
			items := make([]richdoc.ListItem, len(n.Items))
			for i, it := range n.Items {
				items[i] = richdoc.ListItem{Blocks: dropBlocks(it.Blocks)}
			}
			n.Items = items
			out = append(out, n)
		default:
			out = append(out, b)
		}
	}
	return out
}

func dropInlines(ins []richdoc.Inline) []richdoc.Inline {
	var out []richdoc.Inline
	for _, in := range ins {
		if r, ok := in.(richdoc.RawInline); ok && r.Format != "odf" {
			continue
		}
		out = append(out, in)
	}
	return out
}
