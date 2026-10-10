// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// An ODF package is a ZIP, and a ZIP says how big it will be once opened. What
// it says is a number in a FILE.
//
// ⛔ Measured before these limits existed: an .ods of 67 206 bytes whose
// content.xml inflates to 64 MiB parsed without complaint and held 222.9 MiB —
// a thousandfold on disk, and better than three times the inflated size in the
// heap, because the bytes are read, then decoded as UTF-8, then kept as runs.
// Nothing in the package has to be plausible for that to work; deflate simply
// likes repetition.
//
// The ceilings below are what no real document reaches and no hostile one may
// pass. For scale: the content.xml of OpenSSL's own six-sheet lifecycles.ods —
// a workbook that pads every row to 1024 columns and asks for a million rows —
// is 96 453 bytes. These leave it six hundred times its size.
const (
	// maxPartBytes is how large any single entry may be once opened.
	maxPartBytes = 64 << 20

	// maxPackageBytes is how much the whole package may come to. One entry
	// under the ceiling says nothing about a thousand of them.
	maxPackageBytes = 192 << 20

	// maxParts is how many entries a package may hold.
	//
	// ⛔ It bounds an allocation made BEFORE anything is read: the parts map
	// used to be made with len(zr.File) buckets, and the central directory of a
	// ZIP costs about forty-six bytes per entry, so a modest file could ask for
	// millions. An ODF package of more than a few thousand parts is a package
	// with a few thousand pictures in it.
	maxParts = 8192

	// maxDepth is how far elements may nest inside one another.
	//
	// ⛔ It bounds something no size ceiling can see: the bytes on the way in
	// are small, and it is the SHAPE that costs. A 65 851 byte package of two
	// hundred thousand nested tables was measured holding 115.5 MiB and parsed
	// without complaint — under every other limit here. Deeper still is a stack
	// the runtime cannot grow, and a stack overflow runs no defer and cannot be
	// recovered: there would be nothing left to return an error with.
	//
	// A list inside a table cell inside a list is three. A document that
	// genuinely reaches two hundred was written by a program, for a program.
	maxDepth = 256
)

// errTooLarge is returned when a package asks for more than the ceilings allow.
// It names the entry, what it wanted and what it was allowed, because the whole
// value of a refusal is that the person reading it can tell a hostile file from
// a legitimately enormous one.
type errTooLarge struct {
	entry string
	want  uint64 // bytes, or entries when entry is ""
	limit uint64
}

func (e errTooLarge) Error() string {
	if e.entry == "" {
		return fmt.Sprintf("the package holds %d entries, past the %d allowed", e.want, e.limit)
	}
	return fmt.Sprintf("%s says it opens to %d bytes, past the %d allowed", e.entry, e.want, e.limit)
}

// errTooDeep is returned when elements nest further than [maxDepth].
type errTooDeep struct{ limit int }

func (e errTooDeep) Error() string {
	return fmt.Sprintf("elements nest more than %d deep", e.limit)
}

// slurpWithin reads one ZIP entry, refusing to hold more than limit bytes.
//
// The DECLARED size decides, and it decides before a single byte is inflated,
// which is the point: a ZIP bomb costs nothing to refuse, because its own
// header says what it will cost.
//
// ⛔ A declared size is a CLAIM, so a second check on the bytes actually read
// looks obviously necessary — and it is not, because archive/zip makes the
// claim binding. Its reader counts what it hands out and refuses the moment an
// entry yields more than it declared, before any CRC is reached. Measured: a
// crafted package declaring one byte and holding four thousand is refused
// during the read, by archive/zip, not here.
//
// So that second check was written, was unreachable, and is gone. An
// unreachable guard is not defence in depth; it is a claim nobody has tested —
// the same lesson a format comparison in go-pdfkit/convert taught on the same
// day. What is left is a DEPENDENCY on archive/zip's behaviour, and
// TestAnEntryThatHoldsMoreThanItDeclaresIsRefused pins it: if a future Go
// stopped bounding the read, that test goes red rather than this package
// quietly losing its ceiling.
//
// The reader is bounded at limit+1 regardless. It costs nothing, and it means
// the ceiling does not rest on being right about the paragraph above.
func slurpWithin(f *zip.File, limit uint64) ([]byte, error) {
	if f.UncompressedSize64 > limit {
		return nil, errTooLarge{entry: f.Name, want: f.UncompressedSize64, limit: limit}
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, int64(limit)+1))
}

// checkDepth walks a part and refuses nesting past [maxDepth].
//
// ⛔ A separate pass, and it has to be. The first attempt counted depth in
// parseBlock, where this package's own recursion is — and it caught NOTHING,
// because a table inside a table cell never reaches parseBlock: parseCell hands
// anything that is not a paragraph to the decoder's Skip, which walks it
// without this package seeing a single element. The memory was never in our
// document tree; it was in encoding/xml's own stack of open elements.
//
// A guard placed where the code recurses, rather than where the COST is, is a
// guard that passes its own test and stops nothing.
//
// It stops at the first token past the ceiling, so the stack it builds on the
// way is bounded by the ceiling too — a check that had to read a whole file
// before refusing it would be the same denial of service wearing a refusal.
func checkDepth(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// Malformed XML is not this check's business: the real parse says
			// so, with the position and the reason.
			return nil
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			if depth > maxDepth {
				return errTooDeep{limit: maxDepth}
			}
		case xml.EndElement:
			depth--
		}
	}
}

// errTooManyPages is returned when a drawing or presentation claims more pages
// than [maxPages]. It says how many were allowed, not how many there were:
// the count is not known — the walk stops at the ceiling rather than reading
// to the end of a file that may not have one.
type errTooManyPages struct{ limit int }

func (e errTooManyPages) Error() string {
	return fmt.Sprintf("the document holds more than %d pages", e.limit)
}
