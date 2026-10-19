// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-richdoc/richdoc"
)

// pictures are real PNG and JPEG files, made here, so that a reader that
// decodes them — LibreOffice does — has something to decode.
func pictures(t testing.TB) (pngData, jpegData []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 16), uint8(y * 28), 128, 255})
		}
	}
	var p, j bytes.Buffer
	if err := png.Encode(&p, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&j, img, nil); err != nil {
		t.Fatal(err)
	}
	return p.Bytes(), j.Bytes()
}

// deck is three slides at marp's 1280×720: a PNG with two lines of notes, a
// JPEG with none, and a PNG whose notes and name need escaping.
func deck(t testing.TB) Presentation {
	p, j := pictures(t)
	return Presentation{
		Width: 1280, Height: 720,
		Title: "The Deck", Author: "Ada & Bob", Subject: "A test", Keywords: "one, two",
		Slides: []PresentationSlide{
			{Image: p, ImageType: "png", Notes: "first note\r\nsecond line", Name: "Opening"},
			{Image: j, ImageType: "jpeg"},
			{Image: p, ImageType: "png", Notes: "a < b & \"c\"\n\nafter a blank", Name: "x <y>"},
		},
	}
}

func writeDeck(t testing.TB, p Presentation) []byte {
	t.Helper()
	b, err := WritePresentation(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type entries struct {
	order []string
	files map[string][]byte
	first *zip.File
}

func unzipDeck(t *testing.T, b []byte) entries {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := entries{files: map[string][]byte{}, first: zr.File[0]}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out.order = append(out.order, f.Name)
		out.files[f.Name] = data
	}
	return out
}

// The control is the reader that was here first: every slide comes back as a
// heading of its name, followed by its picture, byte for byte; the notes do
// not, because the reader skips them on purpose; and the properties come back.
func TestWrittenPresentationReadsBack(t *testing.T) {
	in := deck(t)
	doc, err := Parse(writeDeck(t, in))
	if err != nil {
		t.Fatalf("what was written could not be read: %v", err)
	}
	var names []string
	var pics [][]byte
	for _, b := range doc.Blocks {
		switch n := b.(type) {
		case richdoc.Heading:
			names = append(names, richdoc.PlainText(&richdoc.Document{Blocks: []richdoc.Block{n}}))
		case richdoc.Paragraph:
			img, ok := n.Inlines[0].(richdoc.Image)
			if !ok {
				t.Fatalf("a slide holds %T, not its picture", n.Inlines[0])
			}
			_, payload, _ := strings.Cut(img.URL, ",")
			data, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				t.Fatal(err)
			}
			pics = append(pics, data)
		}
	}
	if got := strings.Join(names, "|"); got != "Opening|Slide 2|x <y>" {
		t.Errorf("slides = %q", got)
	}
	if len(pics) != 3 || !bytes.Equal(pics[0], in.Slides[0].Image) || !bytes.Equal(pics[1], in.Slides[1].Image) {
		t.Errorf("the pictures did not come back as they went in")
	}
	if strings.Contains(richdoc.PlainText(doc), "first note") {
		t.Error("the notes reached the slide text")
	}
	if doc.Meta["title"] != "The Deck" || doc.Meta["author"] != "Ada & Bob" || doc.Meta["subject"] != "A test" {
		t.Errorf("meta = %v", doc.Meta)
	}
}

// ⛔ The structural judge, for what our reader is too forgiving to notice:
// mimetype first, STORED, with no extra field (ODF 1.3 part 2 §3.3); every
// file but mimetype and META-INF/ listed once in the manifest and nothing
// listed that is not there (§3.2); every xlink:href resolves; every XML part
// well-formed; page names unique.
func TestWrittenPresentationIsWhole(t *testing.T) {
	b := writeDeck(t, deck(t))
	e := unzipDeck(t, b)
	if e.first.Name != "mimetype" || e.first.Method != zip.Store || len(e.first.Extra) != 0 {
		t.Fatalf("first entry %q method %d extra %d: want a STORED mimetype with no extra field", e.first.Name, e.first.Method, len(e.first.Extra))
	}
	if got := string(e.files["mimetype"]); got != mimetypeODP {
		t.Errorf("mimetype = %q", got)
	}
	if string(b[30:38]) != "mimetype" || string(b[38:38+len(mimetypeODP)]) != mimetypeODP {
		t.Error("the media type is not at byte 38, where a magic-number reader looks")
	}
	checkMimetypeHeader(t, b, mimetypeODP)

	var man struct {
		Entry []struct {
			Path  string `xml:"full-path,attr"`
			Media string `xml:"media-type,attr"`
		} `xml:"file-entry"`
	}
	if err := xml.Unmarshal(e.files[partManifest], &man); err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, m := range man.Entry {
		if _, twice := listed[m.Path]; twice {
			t.Errorf("%s is in the manifest twice", m.Path)
		}
		listed[m.Path] = m.Media
		if _, there := e.files[m.Path]; !there && m.Path != "/" {
			t.Errorf("the manifest lists %s, which is not in the package", m.Path)
		}
	}
	if listed["/"] != mimetypeODP {
		t.Errorf("the root entry says %q", listed["/"])
	}
	for _, name := range e.order {
		if name == "mimetype" || strings.HasPrefix(name, "META-INF/") {
			continue
		}
		if listed[name] == "" {
			t.Errorf("%s is not in the manifest", name)
		}
	}
	if listed["Pictures/slide1.png"] != "image/png" || listed["Pictures/slide2.jpg"] != "image/jpeg" {
		t.Errorf("picture media types: %v", listed)
	}

	for name, data := range e.files {
		if !strings.HasSuffix(name, ".xml") {
			continue
		}
		if err := wellFormed(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	content := string(e.files[partContent])
	for _, m := range regexp.MustCompile(`xlink:href="([^"]*)"`).FindAllStringSubmatch(content, -1) {
		if _, there := e.files[m[1]]; !there {
			t.Errorf("xlink:href %q is not in the package", m[1])
		}
	}
	styles := string(e.files[partStyles])
	for _, want := range []string{`fo:page-width="960pt"`, `fo:page-height="540pt"`, `style:master-page style:name="Default"`} {
		if !strings.Contains(styles, want) {
			t.Errorf("%q is not in styles.xml", want)
		}
	}
	if n := strings.Count(content, `draw:master-page-name="Default"`); n != 3 {
		t.Errorf("%d pages name the master, want 3", n)
	}
	if !strings.Contains(content, `svg:width="960pt" svg:height="540pt"`) {
		t.Error("the picture does not cover the slide")
	}
}

// checkMimetypeHeader asserts the mimetype entry's LOCAL header carries its
// own sizes and no data descriptor follows it: flag bit 3 clear, and the
// compressed and uncompressed sizes equal to the media type's length. This is
// what LibreOffice insists on, and what archive/zip's reader cannot see.
func checkMimetypeHeader(t *testing.T, b []byte, mimetype string) {
	t.Helper()
	flags := binary.LittleEndian.Uint16(b[6:8])
	csize := binary.LittleEndian.Uint32(b[18:22])
	usize := binary.LittleEndian.Uint32(b[22:26])
	if flags&0x8 != 0 || int(csize) != len(mimetype) || int(usize) != len(mimetype) {
		t.Errorf("mimetype local header: flags %#x, sizes %d/%d: want no data descriptor and %d", flags, csize, usize, len(mimetype))
	}
}

// ⛔ The text writer had the same defect, and every .odt it ever wrote was
// refused by LibreOffice. This is the CI-side half of that check.
func TestWriteMimetypeHasNoDataDescriptor(t *testing.T) {
	b, err := Write(&richdoc.Document{})
	if err != nil {
		t.Fatal(err)
	}
	checkMimetypeHeader(t, b, mimetypeODT)
}

// And the other half, where LibreOffice is installed: it opens what Write
// wrote, and reads its words.
func TestLibreOfficeOpensTheTextWeWrote(t *testing.T) {
	soffice := findSoffice()
	if soffice == "" {
		t.Skip("no LibreOffice here")
	}
	dir := t.TempDir()
	b, err := Write(&richdoc.Document{Blocks: []richdoc.Block{
		richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Txt("hello from go-odf")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	odt := filepath.Join(dir, "doc.odt")
	if err := os.WriteFile(odt, b, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(soffice, "--headless", "--convert-to", "txt", "--outdir", dir, odt)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("LibreOffice refused the document: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "doc.txt"))
	if err != nil || !strings.Contains(string(got), "hello from go-odf") {
		t.Errorf("LibreOffice did not read the document: %v %q", err, got)
	}
}

// The notes are a presentation:notes on the page, a paragraph per line,
// escaped, beside a thumbnail of their own page; a slide without notes has
// none.
func TestNotesAreWritten(t *testing.T) {
	content := string(unzipDeck(t, writeDeck(t, deck(t))).files[partContent])
	pages := strings.Split(content, "<draw:page ")[1:]
	if len(pages) != 3 {
		t.Fatalf("%d pages", len(pages))
	}
	if !strings.Contains(pages[0], "<text:p>first note</text:p><text:p>second line</text:p>") || !strings.Contains(pages[0], `draw:page-number="1"`) {
		t.Errorf("slide 1's notes:\n%s", pages[0])
	}
	if strings.Contains(pages[1], "presentation:notes") {
		t.Error("a slide with no notes has notes")
	}
	if !strings.Contains(pages[2], "a &lt; b &amp; \"c\"") || !strings.Contains(pages[2], "<text:p/>") {
		t.Errorf("slide 3's notes are not escaped, or the blank line was lost:\n%s", pages[2])
	}
	if !strings.Contains(pages[2], `draw:name="x &lt;y&gt;"`) || !strings.Contains(pages[0], "<svg:desc>Opening</svg:desc>") {
		t.Error("a slide's name is not its page name and its picture's description")
	}
}

func TestPresentationMetaIsWritten(t *testing.T) {
	p := deck(t)
	meta := string(unzipDeck(t, writeDeck(t, p)).files[partMeta])
	for _, want := range []string{
		"<dc:title>The Deck</dc:title>", "<dc:creator>Ada &amp; Bob</dc:creator>",
		"<dc:subject>A test</dc:subject>", "<meta:keyword>one</meta:keyword><meta:keyword>two</meta:keyword>",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("%q is not in meta.xml:\n%s", want, meta)
		}
	}
	if strings.Contains(meta, "date") {
		t.Error("a date was written though none was given")
	}

	p.Created = time.Date(2026, 10, 19, 9, 4, 5, 0, time.FixedZone("CEST", 2*3600))
	meta = string(unzipDeck(t, writeDeck(t, p)).files[partMeta])
	for _, want := range []string{
		"<meta:creation-date>2026-10-19T07:04:05Z</meta:creation-date>", "<dc:date>2026-10-19T07:04:05Z</dc:date>",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("%q is not in meta.xml:\n%s", want, meta)
		}
	}

	p = deck(t)
	p.Author, p.Keywords = "", " , "
	meta = string(unzipDeck(t, writeDeck(t, p)).files[partMeta])
	if strings.Contains(meta, "creator") || strings.Contains(meta, "keyword") {
		t.Errorf("a property not given was written:\n%s", meta)
	}

	p.Title, p.Subject = "", ""
	e := unzipDeck(t, writeDeck(t, p))
	if _, there := e.files[partMeta]; there {
		t.Error("an empty meta.xml was written")
	}
}

// A deck taller than it is wide is printed portrait, and its notes thumbnail
// keeps the slide's proportions.
func TestAPortraitDeckIsPortrait(t *testing.T) {
	p := deck(t)
	p.Width, p.Height = 720, 1280
	e := unzipDeck(t, writeDeck(t, p))
	if !strings.Contains(string(e.files[partStyles]), `fo:page-width="540pt" fo:page-height="960pt" style:print-orientation="portrait"`) {
		t.Error("a portrait deck is not laid out portrait")
	}
	if !strings.Contains(string(e.files[partContent]), `<draw:page-thumbnail draw:layer="layout" svg:width="151pt" svg:height="270pt"`) {
		t.Error("the notes thumbnail is not at the slide's proportions, inside its box")
	}
	if !strings.Contains(string(unzipDeck(t, writeDeck(t, deck(t))).files[partContent]), `<draw:page-thumbnail draw:layer="layout" svg:width="432pt" svg:height="243pt"`) {
		t.Error("a 16:9 thumbnail is not 432 × 243")
	}
}

func TestWritePresentationIsDeterministic(t *testing.T) {
	if !bytes.Equal(writeDeck(t, deck(t)), writeDeck(t, deck(t))) {
		t.Error("two writes of one deck differ")
	}
}

// A size in CSS pixels is written in points, exactly: three quarters of a
// pixel is a point, so a whole number of pixels is at most two decimals.
func TestPixelsAreWrittenAsPoints(t *testing.T) {
	for px, want := range map[int]string{1280: "960pt", 721: "540.75pt", 722: "541.5pt", 723: "542.25pt"} {
		if got := pt(px); got != want {
			t.Errorf("pt(%d) = %q, want %q", px, got, want)
		}
	}
}

func TestWritePresentationRefuses(t *testing.T) {
	defer func(s, i int, all int64, txt int) {
		maxWriteSlides, maxImageBytes, maxImagesBytes, maxTextBytes = s, i, all, txt
	}(maxWriteSlides, maxImageBytes, maxImagesBytes, maxTextBytes)

	cases := []struct {
		name  string
		edit  func(*Presentation)
		lower func()
		want  string
	}{
		{"narrow", func(p *Presentation) { p.Width = 95 }, nil, "width"},
		{"tall", func(p *Presentation) { p.Height = 5377 }, nil, "height"},
		{"empty", func(p *Presentation) { p.Slides = nil }, nil, "no slides"},
		{"many", func(p *Presentation) {}, func() { maxWriteSlides = 2 }, "3 slides"},
		{"type", func(p *Presentation) { p.Slides[0].ImageType = "gif" }, nil, `"gif"`},
		{"lying", func(p *Presentation) { p.Slides[1].ImageType = "png" }, nil, "not a png"},
		{"big", func(p *Presentation) {}, func() { maxImageBytes = 10 }, "slide 1: a picture"},
		{"all", func(p *Presentation) {}, func() { maxImagesBytes = int64(len(deck(t).Slides[0].Image)) + 1 }, "pictures come to"},
		{"notes", func(p *Presentation) { p.Slides[2].Notes = strings.Repeat("x", 26) }, func() { maxTextBytes = 25 }, "slide 3: its notes"},
		{"title", func(p *Presentation) { p.Title = strings.Repeat("x", 26) }, func() { maxTextBytes = 25 }, "document property"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			maxWriteSlides, maxImageBytes, maxImagesBytes, maxTextBytes = 1000, 64<<20, 160<<20, 1<<20
			if c.lower != nil {
				c.lower()
			}
			p := deck(t)
			c.edit(&p)
			_, err := WritePresentation(p)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one saying %q", err, c.want)
			}
		})
	}
}

// ⛔ THE ONLY JUDGE HERE THAT IS NOT OUR OWN CODE. Where LibreOffice is
// installed it converts the deck to PDF — one page per slide — and to .pptx,
// where the notes are on notes slides only if it took ours for notes.
// It skips where LibreOffice is not installed, which includes CI.
func TestLibreOfficeOpensTheDeckWeWrote(t *testing.T) {
	soffice := findSoffice()
	if soffice == "" {
		t.Skip("no LibreOffice here: the structural checks are the only judge")
	}
	dir := t.TempDir()
	odp := filepath.Join(dir, "deck.odp")
	if err := os.WriteFile(odp, writeDeck(t, deck(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"pdf", "pptx"} {
		cmd := exec.Command(soffice, "--headless", "--convert-to", to, "--outdir", dir, odp)
		cmd.Env = append(os.Environ(), "HOME="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("LibreOffice refused the deck: %v\n%s", err, out)
		}
	}
	pdf, err := os.ReadFile(filepath.Join(dir, "deck.pdf"))
	if err != nil {
		t.Fatalf("LibreOffice wrote no PDF: %v", err)
	}
	if n := len(regexp.MustCompile(`/Type\s*/Page[^s]`).FindAll(pdf, -1)); n != 3 {
		t.Errorf("the PDF has %d pages, want 3", n)
	}
	pptx, err := os.ReadFile(filepath.Join(dir, "deck.pptx"))
	if err != nil {
		t.Fatalf("LibreOffice wrote no .pptx: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(pptx), int64(len(pptx)))
	if err != nil {
		t.Fatal(err)
	}
	var notes strings.Builder
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/notesSlides/notesSlide") && strings.HasSuffix(f.Name, ".xml") {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			notes.Write(data)
		}
	}
	for _, want := range []string{"first note", "second line", "a &lt; b &amp;"} {
		if !strings.Contains(notes.String(), want) {
			t.Errorf("LibreOffice did not carry %q into the notes", want)
		}
	}
}

// findSoffice is where LibreOffice is, or "" if it is not here. The macOS
// bundle is not on PATH, which is why the name alone is not enough.
func findSoffice() string {
	if p, err := exec.LookPath("soffice"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		const bundled = "/Applications/LibreOffice.app/Contents/MacOS/soffice"
		if _, err := os.Stat(bundled); err == nil {
			return bundled
		}
	}
	return ""
}
