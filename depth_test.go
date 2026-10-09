// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"strings"
	"testing"
)

// nest wraps a body in n levels of table/row/cell, which is the deepest thing
// ODF lets you repeat without inventing elements.
func nest(n int, body string) string {
	open := strings.Repeat(`<table:table><table:table-row><table:table-cell>`, n)
	shut := strings.Repeat(`</table:table-cell></table:table-row></table:table>`, n)
	return open + body + shut
}

func TestBlocksThatNestTooDeepAreRefused(t *testing.T) {
	// ⛔ No size ceiling can see this. A 65 851 byte package of two hundred
	// thousand nested tables was measured building 115.5 MiB of document tree
	// and parsing without complaint: the bytes on the way in are small, and it
	// is the SHAPE that costs.
	src := zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", sheetOf(nest(maxDepth+10, `<text:p>x</text:p>`))},
	)
	if len(src) > 1<<20 {
		t.Fatalf("the fixture is %d bytes on disk, so this would be about size", len(src))
	}
	_, err := Parse(src)
	if err == nil {
		t.Fatal("blocks nesting past the ceiling were accepted")
	}
	if !strings.Contains(err.Error(), "nest") {
		t.Errorf("the refusal is %q and does not say what was too deep", err)
	}
}

func TestOrdinaryNestingStillParses(t *testing.T) {
	// ⛔ The side a ceiling gets wrong. Tested only from above, a limit is
	// satisfied by refusing everything — and a table inside a cell is an
	// ordinary document, not an attack.
	d, err := Parse(zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", sheetOf(
			`<table:table table:name="S"><table:table-row><table:table-cell>` +
				`<table:table><table:table-row><table:table-cell>` +
				`<text:p>inner</text:p>` +
				`</table:table-cell></table:table-row></table:table>` +
				`</table:table-cell></table:table-row></table:table>`)},
	))
	if err != nil {
		t.Fatalf("a table inside a table cell was refused: %v", err)
	}
	if len(d.Blocks) == 0 {
		t.Error("it was accepted and came back empty")
	}
}

func TestDepthIsCountedPerDocumentAndNotCumulatively(t *testing.T) {
	// ⛔ The bug a depth counter invites: increment on the way in and forget to
	// decrement, and a document of five hundred sibling paragraphs trips a
	// ceiling meant for nesting. Siblings are not depth.
	var b strings.Builder
	for i := 0; i < maxDepth*4; i++ {
		b.WriteString(`<table:table table:name="S"><table:table-row>` +
			`<table:table-cell office:value-type="string"><text:p>x</text:p></table:table-cell>` +
			`</table:table-row></table:table>`)
	}
	d, err := Parse(zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", sheetOf(b.String())},
	))
	if err != nil {
		t.Fatalf("%d sibling tables were refused as too deep: %v", maxDepth*4, err)
	}
	var tables int
	for _, blk := range d.Blocks {
		if _, ok := blk.(interface{ IsBlock() }); ok || blk != nil {
			tables++
		}
	}
	if tables == 0 {
		t.Error("they were accepted and came back empty")
	}
}
