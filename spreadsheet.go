// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"encoding/xml"
	"strconv"

	"github.com/go-richdoc/richdoc"
)

// A spreadsheet is the same package as a text document — the same ZIP, the
// same content.xml, the same table:table/-row/-cell elements — inside an
// office:spreadsheet body instead of an office:text one. So the reader needed
// three things rather than a second parser:
//
//   - to walk that body at all. It did not, and the cost was SILENT: a .ods
//     parsed without error into a document of nothing, so a conversion handed
//     somebody an empty page and no reason for it.
//   - sheet names, because a workbook of three sheets is otherwise three
//     tables run together with nothing saying where one ends.
//   - the repeat attributes. A text document rarely carries them; a
//     spreadsheet is MADE of them — LibreOffice pads every row out to the
//     sheet width with a single repeated empty cell.

// How far a repeat is honoured. The attribute is a count in a file, so it is
// whatever the file says: LibreOffice writes 1016 to pad a row and 1048576 to
// say "the rest of the sheet".
//
// ⛔ Materialising what it asks for is an allocation decided by the input.
// Capped, and the caps are the formats' own limits rather than round numbers —
// a file asking past them is asking for something no spreadsheet could hold.
const (
	maxRepeatColumns = 1024
	maxRepeatRows    = 65536
)

// repeatOf reads one of the repeat attributes. Anything absent, unreadable or
// below one counts once: a cell is still a cell.
func repeatOf(se xml.StartElement, local string) int {
	for _, a := range se.Attr {
		if a.Name.Local != local {
			continue
		}
		n, err := strconv.Atoi(a.Value)
		if err != nil || n < 1 {
			return 1
		}
		return n
	}
	return 1
}

// emptyCell says whether a cell holds nothing at all. The padding at the end
// of every spreadsheet row is made of these, and keeping them would give a
// two-column sheet a thousand empty columns.
func emptyCell(c richdoc.Cell) bool {
	if len(c.Blocks) > 0 {
		return false
	}
	for _, in := range c.Inlines {
		if t, ok := in.(richdoc.Text); !ok || t.Value != "" {
			return false
		}
	}
	return true
}

// trimTrailingEmpty drops the padding a spreadsheet writes to square its rows
// off. It stops at the first cell holding something, so an empty cell BETWEEN
// two full ones stays: that one is a gap in the data and the shape of the row
// depends on it.
func trimTrailingEmpty(cells []richdoc.Cell, aligns []richdoc.Alignment) ([]richdoc.Cell, []richdoc.Alignment) {
	n := len(cells)
	for n > 0 && emptyCell(cells[n-1]) {
		n--
	}
	// The two slices are built in lockstep, so this cannot differ today. If it
	// ever does, PAD the alignments rather than truncate the cells: a guard
	// against a panic that drops data instead is a worse failure than the one
	// it prevents, and a silent one.
	for len(aligns) < n {
		aligns = append(aligns, richdoc.AlignDefault)
	}
	return cells[:n], aligns[:n]
}
