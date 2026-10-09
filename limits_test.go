// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// zipOf builds a package out of the entries given, in order. The first is
// stored, as the ODF packaging rules require of mimetype.
func zipOf(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, e := range entries {
		var w interface{ Write([]byte) (int, error) }
		var err error
		if i == 0 {
			w, err = zw.CreateHeader(&zip.FileHeader{Name: e[0], Method: zip.Store})
		} else {
			w, err = zw.Create(e[0])
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const odsMime = "application/vnd.oasis.opendocument.spreadsheet"

// sheetOf wraps a body in the smallest spreadsheet content.xml there is.
func sheetOf(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
 xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
 office:version="1.3"><office:body><office:spreadsheet>` + body +
		`</office:spreadsheet></office:body></office:document-content>`
}

func TestAnEntryThatInflatesPastTheCeilingIsRefused(t *testing.T) {
	// ⛔ Measured before this existed: 67 206 bytes on disk, 64 MiB of
	// content.xml, 222.9 MiB held, and Parse returned a document without
	// complaint. Deflate likes repetition, so nothing in the file has to be
	// plausible for a thousandfold to work.
	//
	// Built just past the ceiling rather than wildly past it, so the test is
	// about the LIMIT and not about how patient the machine is.
	fat := sheetOf(`<table:table table:name="S"><table:table-row>` +
		`<table:table-cell office:value-type="string"><text:p>` +
		strings.Repeat("A", maxPartBytes+1) +
		`</text:p></table:table-cell></table:table-row></table:table>`)
	src := zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", fat},
	)
	// The point is the ratio: this has to stay small on disk or the test is
	// measuring something else.
	if len(src) > 1<<20 {
		t.Fatalf("the bomb is %d bytes on disk, which is not a bomb", len(src))
	}
	_, err := Parse(src)
	if err == nil {
		t.Fatal("an entry inflating past the ceiling was accepted")
	}
	// ⛔ WHICH guard spoke. Disabling the ceiling and letting the read truncate
	// also produces an error — from the XML parser, about an unexpected EOF —
	// and a test that only asked whether something failed was satisfied by it.
	// The mutation removing the ceiling survived exactly that test.
	var tooLarge errTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("it was refused by %T (%v), not by the ceiling: a truncated read fails too, "+
			"and fails for a reason that says nothing about the file being hostile", err, err)
	}
	if !strings.Contains(err.Error(), "content.xml") {
		t.Errorf("the refusal is %q and does not name the entry", err)
	}
}

func TestAnEntryExactlyAtTheCeilingIsAllowed(t *testing.T) {
	// ⛔ The other side, and the one a limit gets wrong. A ceiling tested only
	// from above is satisfied by refusing everything, and a reader that refuses
	// every large document is a reader nobody can use. The run here is sized so
	// the whole content.xml lands exactly on the limit.
	const wrap = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
 xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
 office:version="1.3"><office:body><office:spreadsheet>` +
		`<table:table table:name="S"><table:table-row>` +
		`<table:table-cell office:value-type="string"><text:p>`
	const tail = `</text:p></table:table-cell></table:table-row></table:table>` +
		`</office:spreadsheet></office:body></office:document-content>`

	body := wrap + strings.Repeat("A", maxPartBytes-len(wrap)-len(tail)) + tail
	if len(body) != maxPartBytes {
		t.Fatalf("the fixture is %d bytes and the ceiling is %d", len(body), maxPartBytes)
	}
	d, err := Parse(zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", body},
	))
	if err != nil {
		t.Fatalf("a document exactly at the ceiling was refused: %v", err)
	}
	if len(d.Blocks) == 0 {
		t.Error("it was accepted and came back empty")
	}
}

func TestManyEntriesUnderTheCeilingStillAddUp(t *testing.T) {
	// ⛔ One entry under the per-entry ceiling says nothing about a thousand of
	// them. Without the running total, three thousand 64 MiB parts would each
	// pass and the package would come to 192 GiB.
	//
	// Here: entries of a quarter of the package budget each, so the fourth is
	// what the total refuses — and the per-entry ceiling never fires, which is
	// what makes this test about the total rather than about the entry.
	const each = maxPackageBytes / 4
	entries := [][2]string{{"mimetype", odsMime}}
	for i := 0; i < 5; i++ {
		entries = append(entries, [2]string{
			"Pictures/p" + string(rune('a'+i)) + ".bin",
			strings.Repeat("B", each),
		})
	}
	entries = append(entries, [2]string{"content.xml", sheetOf("")})
	_, err := Parse(zipOf(t, entries...))
	if err == nil {
		t.Fatal("five entries of a quarter of the budget each were accepted")
	}
	if each > maxPartBytes {
		t.Fatalf("each entry is %d, past the per-entry ceiling of %d — this test would "+
			"pass for the wrong reason", each, maxPartBytes)
	}
}

func TestAPackageOfTooManyEntriesIsRefusedBeforeAnythingIsRead(t *testing.T) {
	// ⛔ The entry count decides an allocation before a byte is inflated: the
	// parts map used to be made with len(zr.File) buckets, and a ZIP's central
	// directory costs about forty-six bytes an entry.
	entries := [][2]string{{"mimetype", odsMime}}
	for i := 0; i <= maxParts; i++ {
		entries = append(entries, [2]string{"x/" + itoa(i), ""})
	}
	_, err := Parse(zipOf(t, entries...))
	if err == nil {
		t.Fatal("a package of more entries than allowed was accepted")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("the refusal is %q and does not say what was too many", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestARefusalSaysWhichLimitAndByHowMuch(t *testing.T) {
	// The whole value of a ceiling is that somebody reading the refusal can
	// tell a hostile file from a legitimately enormous one.
	for _, c := range []struct {
		err  errTooLarge
		want []string
	}{
		{errTooLarge{want: 99999, limit: maxParts}, []string{"99999", "entries"}},
		{errTooLarge{entry: "content.xml", want: 1 << 40, limit: maxPartBytes}, []string{"content.xml", "1099511627776", "67108864"}},
	} {
		got := c.err.Error()
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%q does not carry %q", got, w)
			}
		}
	}
}

// understate rewrites the uncompressed size an entry DECLARES, in the central
// directory, which is where archive/zip reads it from. The compressed data is
// untouched, so the file still opens and still yields every byte it holds.
//
// Central directory file header: 4 signature, 2 version made by, 2 version
// needed, 2 flags, 2 method, 2 mod time, 2 mod date, 4 CRC-32, 4 compressed
// size, 4 uncompressed size, 2 name length, then the name.
func understate(t *testing.T, src []byte, name string, to uint32) []byte {
	t.Helper()
	out := append([]byte(nil), src...)
	sig := []byte{'P', 'K', 0x01, 0x02}
	found := false
	for i := 0; i+46 <= len(out); i++ {
		if !bytes.Equal(out[i:i+4], sig) {
			continue
		}
		n := int(out[i+28]) | int(out[i+29])<<8
		if i+46+n > len(out) || string(out[i+46:i+46+n]) != name {
			continue
		}
		out[i+24] = byte(to)
		out[i+25] = byte(to >> 8)
		out[i+26] = byte(to >> 16)
		out[i+27] = byte(to >> 24)
		found = true
	}
	if !found {
		t.Fatalf("no central directory entry named %q: the fixture is not what this test thinks", name)
	}
	return out
}

func TestADeclaredSizeDecidesAndSaysSo(t *testing.T) {
	// The honest cases, both sides of the boundary, and the refusal has to be
	// OURS — asserting merely that an error happened cannot say which guard
	// spoke, and in this file that distinction has already been wrong once.
	const payload = 4096
	honest := zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", strings.Repeat("C", payload)},
	)
	zr, err := zip.NewReader(bytes.NewReader(honest), int64(len(honest)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != "content.xml" {
			continue
		}
		if b, err := slurpWithin(f, payload); err != nil || len(b) != payload {
			t.Errorf("an entry exactly at the ceiling gave %d bytes, %v", len(b), err)
		}
		_, err := slurpWithin(f, payload-1)
		if err == nil {
			t.Fatal("an entry one byte past the ceiling was accepted")
		}
		if _, ok := err.(errTooLarge); !ok {
			t.Errorf("it was refused by %T (%v), not by this package", err, err)
		}
	}
}

func TestAnEntryThatHoldsMoreThanItDeclaresIsRefused(t *testing.T) {
	// ⛔ A declared size is a CLAIM, so a second check on the bytes actually
	// read looks obviously necessary. It was written, and it was UNREACHABLE:
	// archive/zip counts what it hands out and refuses the moment an entry
	// yields more than it declared, before any CRC is reached.
	//
	// That makes this package DEPEND on a behaviour of the standard library,
	// which is fine so long as somebody notices if it changes. This is the
	// noticing. A crafted package that declares one byte and holds four
	// thousand must not come back with four thousand bytes, whoever refuses it.
	//
	// ⛔ The first attempt at this test set UncompressedSize64 on the parsed
	// struct instead of in the file, and archive/zip said "not a valid zip
	// file" because the struct no longer matched what it had read. The test
	// passed and proved nothing. The lie has to be in the BYTES.
	const payload = 4096
	honest := zipOf(t,
		[2]string{"mimetype", odsMime},
		[2]string{"content.xml", strings.Repeat("C", payload)},
	)
	liar := understate(t, honest, "content.xml", 1)

	zr, err := zip.NewReader(bytes.NewReader(liar), int64(len(liar)))
	if err != nil {
		t.Fatalf("the crafted package does not even open, so this test measures nothing: %v", err)
	}
	seen := false
	for _, f := range zr.File {
		if f.Name != "content.xml" {
			continue
		}
		seen = true
		if f.UncompressedSize64 != 1 {
			t.Fatalf("the lie did not land: the entry declares %d", f.UncompressedSize64)
		}
		b, err := slurpWithin(f, payload)
		if err == nil && len(b) > 1 {
			t.Errorf("an entry declaring 1 byte handed back %d, and nothing objected: "+
				"the ceiling in this package rests on the declared size, and the declared "+
				"size is no longer binding", len(b))
		}
	}
	if !seen {
		t.Fatal("the fixture has no content.xml")
	}
}
