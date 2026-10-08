// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/go-richdoc/richdoc"
)

// sheetPackage builds the smallest .ods there is around the given body.
func sheetPackage(t *testing.T, body string) []byte {
	t.Helper()
	const mime = "application/vnd.oasis.opendocument.spreadsheet"
	content := `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
 xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
 office:version="1.3"><office:body><office:spreadsheet>` + body +
		`</office:spreadsheet></office:body></office:document-content>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(mime))
	if fw, err = zw.Create("content.xml"); err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	if fw, err = zw.Create("META-INF/manifest.xml"); err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(`<?xml version="1.0"?><manifest:manifest ` +
		`xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3">` +
		`<manifest:file-entry manifest:full-path="/" manifest:media-type="` + mime + `"/>` +
		`<manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>` +
		`</manifest:manifest>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func cell(s string) string {
	return `<table:table-cell office:value-type="string"><text:p>` + s + `</text:p></table:table-cell>`
}

func tables(t *testing.T, d *richdoc.Document) []richdoc.Table {
	t.Helper()
	var out []richdoc.Table
	for _, b := range d.Blocks {
		if tb, ok := b.(richdoc.Table); ok {
			out = append(out, tb)
		}
	}
	return out
}

func TestASpreadsheetIsReadAtAll(t *testing.T) {
	// ⛔ The regression this file exists for. The reader walked office:text
	// and nothing else, so a .ods parsed WITHOUT AN ERROR into a document of
	// no blocks — a conversion handed somebody an empty page and no reason
	// for it. An empty answer and a refusal are different things, and this
	// was neither: it was an empty answer wearing a success.
	d, err := Parse(sheetPackage(t, `<table:table table:name="S">
		<table:table-row>`+cell("a")+cell("b")+`</table:table-row>
		<table:table-row>`+cell("c")+cell("d")+`</table:table-row>
	</table:table>`))
	if err != nil {
		t.Fatalf("a spreadsheet was refused: %v", err)
	}
	tb := tables(t, d)
	if len(tb) != 1 {
		t.Fatalf("%d tables came out of a one-sheet workbook", len(tb))
	}
	if len(tb[0].Rows) != 2 {
		t.Errorf("the sheet has two rows and %d came back", len(tb[0].Rows))
	}
}

func TestEachSheetIsNamedAboveItsTable(t *testing.T) {
	// Two sheets run together are one table with a seam nobody can see.
	d, err := Parse(sheetPackage(t,
		`<table:table table:name="Budget"><table:table-row>`+cell("x")+`</table:table-row></table:table>`+
			`<table:table table:name="Notes"><table:table-row>`+cell("y")+`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	var headings []string
	for _, b := range d.Blocks {
		// ⛔ A VALUE. *Heading satisfies Block as well, matches no case any
		// consumer wrote, and travels the whole chain to come out as nothing.
		h, ok := b.(richdoc.Heading)
		if !ok {
			if _, isPtr := b.(*richdoc.Heading); isPtr {
				t.Fatal("a heading came back as *Heading: it satisfies Block and no consumer matches it")
			}
			continue
		}
		var b strings.Builder
		for _, in := range h.Inlines {
			if t, ok := in.(richdoc.Text); ok {
				b.WriteString(t.Value)
			}
		}
		headings = append(headings, b.String())
	}
	if strings.Join(headings, ",") != "Budget,Notes" {
		t.Errorf("the sheets are named %v", headings)
	}
}

func TestATableInRunningTextGetsNoHeading(t *testing.T) {
	// The heading belongs to a SHEET. A table in a text document has no name
	// and must not grow one.
	d, err := Parse(odtWith("", `<table:table table:name="Table1">
		<table:table-row>`+cell("a")+`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range d.Blocks {
		if _, ok := b.(richdoc.Heading); ok {
			t.Error("a table in running text was given a heading")
		}
	}
}

func TestARepeatedCellBecomesThatManyColumns(t *testing.T) {
	d, err := Parse(sheetPackage(t, `<table:table table:name="S"><table:table-row>`+
		`<table:table-cell table:number-columns-repeated="3" office:value-type="string"><text:p>x</text:p></table:table-cell>`+
		cell("end")+`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 || len(tb[0].Rows) != 1 {
		t.Fatalf("the sheet came back as %d tables", len(tb))
	}
	if n := len(tb[0].Rows[0]); n != 4 {
		t.Errorf("a cell repeated three times beside one more gave %d columns", n)
	}
}

func TestThePaddingAtTheEndOfARowIsDropped(t *testing.T) {
	// ⛔ Every spreadsheet row is squared off with one repeated empty cell.
	// Keeping it gives a two-column sheet a thousand empty columns, and the
	// table that comes out is unreadable in every format downstream.
	d, err := Parse(sheetPackage(t, `<table:table table:name="S"><table:table-row>`+
		cell("a")+cell("b")+
		`<table:table-cell table:number-columns-repeated="1016"/>`+
		`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 || len(tb[0].Rows) != 1 {
		t.Fatalf("%d tables", len(tb))
	}
	if n := len(tb[0].Rows[0]); n != 2 {
		t.Errorf("a two-column row padded to 1018 came back with %d columns", n)
	}
}

func TestAGapBetweenTwoFullCellsSurvives(t *testing.T) {
	// The other direction, and the one trimming gets wrong if it is written
	// as "drop every empty cell": an empty cell BETWEEN two full ones is a
	// gap in the data, and the shape of the row depends on it.
	d, err := Parse(sheetPackage(t, `<table:table table:name="S"><table:table-row>`+
		cell("a")+`<table:table-cell/>`+cell("c")+
		`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 || len(tb[0].Rows) != 1 {
		t.Fatalf("%d tables", len(tb))
	}
	if n := len(tb[0].Rows[0]); n != 3 {
		t.Errorf("a row of full, empty, full came back with %d columns", n)
	}
}

func TestARepeatIsBoundedByWhatASheetCouldHold(t *testing.T) {
	// ⛔ The repeat is a number in a FILE, and materialising what it asks for
	// is an allocation the input decides. LibreOffice writes 1048576 to mean
	// "the rest of the sheet"; a file may write anything.
	d, err := Parse(sheetPackage(t, `<table:table table:name="S"><table:table-row>`+
		`<table:table-cell table:number-columns-repeated="99999999" office:value-type="string"><text:p>x</text:p></table:table-cell>`+
		`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 || len(tb[0].Rows) != 1 {
		t.Fatalf("%d tables", len(tb))
	}
	if n := len(tb[0].Rows[0]); n > 1024 {
		t.Errorf("a cell asking to be repeated a hundred million times gave %d columns", n)
	}
}

func TestAnEmptyRepeatedRowAddsNothing(t *testing.T) {
	// "number-rows-repeated" on a row of nothing is the sheet saying "empty
	// until here". Keeping thousands of them is keeping the emptiness.
	d, err := Parse(sheetPackage(t, `<table:table table:name="S">`+
		`<table:table-row>`+cell("a")+`</table:table-row>`+
		`<table:table-row table:number-rows-repeated="1048000"><table:table-cell/></table:table-row>`+
		`</table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 {
		t.Fatalf("%d tables", len(tb))
	}
	if n := len(tb[0].Rows); n != 1 {
		t.Errorf("one row of data and a million empty ones came back as %d rows", n)
	}
}

func TestARepeatedRowOfDataIsKeptThatManyTimes(t *testing.T) {
	d, err := Parse(sheetPackage(t, `<table:table table:name="S">`+
		`<table:table-row table:number-rows-repeated="3">`+cell("a")+`</table:table-row>`+
		`</table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 {
		t.Fatalf("%d tables", len(tb))
	}
	if n := len(tb[0].Rows); n != 3 {
		t.Errorf("a row repeated three times came back %d times", n)
	}
}

func TestARepeatOfDataRowsIsBoundedToo(t *testing.T) {
	// ⛔ The row cap had no witness: the only test repeating rows a million
	// times repeated an EMPTY one, which the reader drops to nothing before it
	// ever reaches the cap. Raising maxRepeatRows to a hundred million changed
	// no outcome, so the guard against an allocation the FILE decides was
	// resting on a test that could not see it. This one carries data.
	d, err := Parse(sheetPackage(t, `<table:table table:name="S">`+
		`<table:table-row table:number-rows-repeated="99999999">`+cell("a")+`</table:table-row>`+
		`</table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	tb := tables(t, d)
	if len(tb) != 1 {
		t.Fatalf("%d tables", len(tb))
	}
	// ⛔ A LITERAL. The first draft asserted `n > maxRepeatRows`, so raising the
	// constant raised the assertion with it and the mutation passed: the judge
	// moved with the subject. The number here is what a spreadsheet could hold,
	// written out, and it is the test's own.
	if n := len(tb[0].Rows); n > 65536 {
		t.Errorf("a row asking to be repeated a hundred million times gave %d rows", n)
	}
}

func TestASheetNameIsATopLevelHeading(t *testing.T) {
	// A sheet is a top-level division of a workbook, so its name is a level-1
	// heading: everything the sheet contains sits under it. Nothing observed
	// the level, and a sheet name demoted to level 2 would nest every sheet
	// under whatever heading happened to come before it.
	d, err := Parse(sheetPackage(t,
		`<table:table table:name="Budget"><table:table-row>`+cell("x")+`</table:table-row></table:table>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range d.Blocks {
		if h, ok := b.(richdoc.Heading); ok {
			if h.Level != 1 {
				t.Errorf("the sheet name came back at level %d", h.Level)
			}
			return
		}
	}
	t.Fatal("no heading at all")
}
