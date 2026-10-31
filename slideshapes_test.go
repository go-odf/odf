// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-richdoc/richdoc"
)

// editable is a deck of editable slides: a background, every kind of shape,
// one picture on both slides, a link, text that needs escaping, an embedded
// font.
func editable(t testing.TB) Presentation {
	p, j := pictures(t)
	red := RGBA{R: 255, A: 255}
	ring := [][]PathCmd{
		{{Op: 'M', P: [3][2]float64{{0, 0}}}, {Op: 'L', P: [3][2]float64{{10, 0}}}, {Op: 'L', P: [3][2]float64{{10, 10}}}, {Op: 'Z'}},
		{{Op: 'M', P: [3][2]float64{{2, 2}}}, {Op: 'C', P: [3][2]float64{{5, 1}, {8, 5}, {8, 8}}}, {Op: 'Z'}},
	}
	slide := func(name string) PresentationSlide {
		return PresentationSlide{
			Name: name, Notes: "say it",
			Background: &RGBA{R: 16, G: 32, B: 48, A: 255},
			Shapes: []Shape{
				{X: 0, Y: 0, W: 100, H: 50, Fill: RGBA{G: 128, B: 255, A: 128}},
				{X: 10, Y: 10, W: 100, H: 50, Radius: 8, Fill: red},
				{Kind: ShapeEllipse, X: 10, Y: 10, W: 30, H: 30, Fill: red},
				{Kind: ShapePath, X: 5, Y: 5, W: 10, H: 10, Paths: ring, Fill: red},
				{Kind: ShapeText, X: 96, Y: 96, W: 300, H: 40, Text: []TextRun{
					{Text: `Héllo <b>&"` + "\x01", Font: "Lato", Size: 26.25, Color: RGBA{A: 255}, Spacing: 0.75},
					{Text: " link", Font: "Lato", Size: 26.25, Bold: true, Italic: true, Color: RGBA{B: 200, A: 100}, Link: "https://example.com/?a=1&b=2"},
					{Text: " plain", Font: "Elsewhere", Size: 12, Color: RGBA{A: 255}},
				}},
				{Kind: ShapeImage, X: 0, Y: 0, W: 96, H: 54, Image: 1, Name: "a picture"},
				{Kind: ShapeImage, X: 0, Y: 0, W: 96, H: 54, Image: 1},
				{Kind: ShapeText, X: 0, Y: 0, W: 1, H: 1},
				{X: 1, Y: 1, W: 2, H: 2},                             // no fill
				{Kind: ShapeImage, X: 0, Y: 0, W: 9, H: 9, Image: 2}, // a JPEG
			},
		}
	}
	return Presentation{
		Width: 1280, Height: 720, Title: "Editable",
		Images: []Image{{Data: p, Type: "png"}, {Data: j, Type: "jpeg"}},
		Fonts:  []Font{{Family: "Lato", Regular: []byte("font data"), BoldItal: []byte("more font data")}},
		Slides: []PresentationSlide{slide("One"), slide("Two")},
	}
}

func mustBeWellFormed(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := wellFormed(data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func TestEditableSlidesAreWhole(t *testing.T) {
	b := writeDeck(t, editable(t))
	e := unzipDeck(t, b)
	for name, data := range e.files {
		if strings.HasSuffix(name, ".xml") {
			mustBeWellFormed(t, name, data)
		}
	}
	pics, fonts := 0, 0
	for _, name := range e.order {
		switch {
		case strings.HasPrefix(name, "Pictures/"):
			pics++
		case strings.HasPrefix(name, "Fonts/"):
			fonts++
		}
	}
	if pics != 2 || fonts != 2 {
		t.Errorf("%d pictures and %d fonts, want the two shared pictures and two faces", pics, fonts)
	}
	if _, ok := e.files["Pictures/shared2.jpg"]; !ok {
		t.Error("the JPEG is not stored as one")
	}
	manifest := string(e.files["META-INF/manifest.xml"])
	for _, want := range []string{`manifest:full-path="Fonts/font1_0.ttf" manifest:media-type="application/x-font-ttf"`, `Pictures/shared1.png`} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest lacks %s", want)
		}
	}
	// A font file is deflated, a picture stored.
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "Fonts/") && f.Method != zip.Deflate {
			t.Errorf("%s is not deflated", f.Name)
		}
		if strings.HasPrefix(f.Name, "Pictures/") && f.Method != zip.Store {
			t.Errorf("%s is not stored", f.Name)
		}
	}
	c := string(e.files["content.xml"])
	for _, want := range []string{
		`<style:font-face style:name="Lato" svg:font-family="Lato" svg:font-weight="normal" svg:font-style="normal"><svg:font-face-src><svg:font-face-uri xlink:href="Fonts/font1_0.ttf"`,
		`style:name="Lato Bold Italic"`,
		`<style:font-face style:name="Elsewhere" svg:font-family="Elsewhere"/>`,
		`draw:fill-color="#102030"`,
		`draw:opacity="50.2%"`,
		`draw:corner-radius="6pt"`,
		`<draw:ellipse`, `<draw:path`, `svg:fill-rule="evenodd"`, `svg:d="M0 0 L1000 0 L1000 1000 Z M200 200 C500 100 800 500 800 800 Z "`,
		`Héllo &lt;b&gt;&amp;"</text:span>`,
		`fo:letter-spacing="0.75pt"`, `fo:font-weight="bold" fo:font-style="italic"`, `loext:opacity="39%"`,
		`<text:a xlink:type="simple" xlink:href="https://example.com/?a=1&amp;b=2">`,
		`<svg:desc>a picture</svg:desc>`, `draw:name="Picture 7"`, `draw:name="Text 8"`,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("content.xml lacks %s", want)
		}
	}
	if strings.Contains(c, "\x01") {
		t.Error("a control character reached content.xml")
	}
	// Our reader reads the words back.
	doc, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if text := richdoc.PlainText(doc); !strings.Contains(text, `Héllo <b>&"`) {
		t.Errorf("the words do not read back: %q", text)
	}
	if !bytes.Equal(b, writeDeck(t, editable(t))) {
		t.Error("two writes of one deck differ")
	}
}

func TestEditableSlidesRefuse(t *testing.T) {
	long := strings.Repeat("x", 9000)
	for name, mutate := range map[string]func(*Presentation){
		"an image index past the list": func(p *Presentation) { p.Slides[0].Shapes[5].Image = 3 },
		"no image index":               func(p *Presentation) { p.Slides[0].Shapes[5].Image = 0 },
		"a negative size":              func(p *Presentation) { p.Slides[0].Shapes[0].W = -1 },
		"a far box":                    func(p *Presentation) { p.Slides[0].Shapes[0].X = 1 << 40 },
		"an unknown kind":              func(p *Presentation) { p.Slides[0].Shapes[0].Kind = 99 },
		"an unknown path command":      func(p *Presentation) { p.Slides[0].Shapes[3].Paths[0][1].Op = 'Q' },
		"a zero path command":          func(p *Presentation) { p.Slides[0].Shapes[3].Paths[0][1].Op = 0 },
		"a far point":                  func(p *Presentation) { p.Slides[0].Shapes[3].Paths[0][1].P[0][0] = 1 << 40 },
		"a zero font size":             func(p *Presentation) { p.Slides[0].Shapes[4].Text[0].Size = 0 },
		"a wild spacing":               func(p *Presentation) { p.Slides[0].Shapes[4].Text[0].Spacing = 1e9 },
		"a long font name":             func(p *Presentation) { p.Slides[0].Shapes[4].Text[0].Font = long },
		"a javascript: link":           func(p *Presentation) { p.Slides[0].Shapes[4].Text[1].Link = "javascript:alert(1)" },
		"a mailto with no address":     func(p *Presentation) { p.Slides[0].Shapes[4].Text[1].Link = "mailto:" },
		"a long link":                  func(p *Presentation) { p.Slides[0].Shapes[4].Text[1].Link = "https://e.com/" + long },
		"a link that does not parse":   func(p *Presentation) { p.Slides[0].Shapes[4].Text[1].Link = "http://[::1" },
		"a font with no name":          func(p *Presentation) { p.Fonts[0].Family = "" },
		"a picture that is not a PNG":  func(p *Presentation) { p.Images[0].Data = []byte("GIF89a") },
		"a slide with nothing":         func(p *Presentation) { p.Slides[0].Shapes, p.Slides[0].Background = nil, nil },
	} {
		p := editable(t)
		mutate(&p)
		if _, err := WritePresentation(p); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	p := editable(t)
	p.Slides[0].Shapes[4].Text[1].Link = "mailto:ada@example.com"
	if _, err := WritePresentation(p); err != nil {
		t.Errorf("mailto: %v", err)
	}
}

func TestEditableBounds(t *testing.T) {
	for name, c := range map[string]struct {
		bound  *int
		to     int
		mutate func(*Presentation)
	}{
		"too many fonts":    {&maxFonts, 0, nil},
		"a font too large":  {&maxFontBytes, 3, nil},
		"too many shapes":   {&maxShapesPerSlide, 2, nil},
		"a long shape name": {&maxTextBytes, 8, func(p *Presentation) { p.Title, p.Slides[0].Notes, p.Slides[0].Shapes[0].Name = "", "", "a long name" }},
		"a long run": {&maxTextBytes, 8, func(p *Presentation) {
			p.Title, p.Slides[0].Notes, p.Slides[0].Shapes[4].Text[0].Text = "", "", "a long run"
		}},
	} {
		p := editable(t)
		old := *c.bound
		*c.bound = c.to
		if c.mutate != nil {
			c.mutate(&p)
		}
		_, err := WritePresentation(p)
		*c.bound = old
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	old := maxImagesBytes
	maxImagesBytes = 10
	defer func() { maxImagesBytes = old }()
	if _, err := WritePresentation(editable(t)); err == nil {
		t.Error("pictures past the total: no error")
	}
}

func TestXMLChars(t *testing.T) {
	if got := xmlChars("a\x00b\x0bc\tde￾f"); got != "abc\tdef" {
		t.Errorf("xmlChars = %q", got)
	}
}

// FuzzEditableText: whatever a run, a name or a font holds, content.xml stays
// well-formed.
func FuzzEditableText(f *testing.F) {
	for _, s := range []string{"plain", "<draw:frame>", `"&'<>`, "\x00\x01\x1f", "￾", "]]>", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := editable(t)
		p.Slides[0].Shapes[4].Text[0].Text = s
		p.Slides[0].Shapes[4].Text[2].Font = s
		p.Slides[0].Shapes[5].Name = s
		p.Fonts[0].Family = "F" + s
		b, err := WritePresentation(p)
		if err != nil {
			return
		}
		e := unzipDeck(t, b)
		mustBeWellFormed(t, "content.xml", e.files["content.xml"])
	})
}

// group is a drawing of three shapes, one of them a group in its turn, and
// words in a font only the group uses: what an SVG becomes on a slide.
func group() Shape {
	sq := [][]PathCmd{
		{{Op: 'M', P: [3][2]float64{{0, 0}}}, {Op: 'L', P: [3][2]float64{{10, 0}}}, {Op: 'L', P: [3][2]float64{{10, 10}}}, {Op: 'Z'}},
		{{Op: 'M', P: [3][2]float64{{2, 0}}}, {Op: 'L', P: [3][2]float64{{12, 0}}}, {Op: 'L', P: [3][2]float64{{12, 10}}}, {Op: 'Z'}},
	}
	return Shape{Name: "an SVG", Group: []Shape{
		{Kind: ShapePath, X: 20, Y: 20, W: 12, H: 10, Paths: sq, NonZero: true, Fill: RGBA{R: 255, A: 255}},
		{Group: []Shape{{Kind: ShapeEllipse, X: 40, Y: 20, W: 10, H: 10, Fill: RGBA{B: 255, A: 255}}}},
		{Kind: ShapeText, X: 20, Y: 40, W: 200, H: 20, Text: []TextRun{{Text: "grouped words", Font: "Grouped Face", Size: 12, Color: RGBA{A: 255}}}},
	}}
}

// TestEditableGroups: a group is a draw:g, named before its members, with
// nested groups inside it; a nonzero path says so; the font only a group's
// text uses is declared; the words read back.
func TestEditableGroups(t *testing.T) {
	p := editable(t)
	p.Slides[0].Shapes = append(p.Slides[0].Shapes, group())
	b := writeDeck(t, p)
	c := string(unzipDeck(t, b).files["content.xml"])
	mustBeWellFormed(t, "content.xml", []byte(c))
	for _, want := range []string{
		`<draw:g draw:name="an SVG"><draw:path draw:name="Shape 12"`,
		`svg:fill-rule="nonzero"`,
		`<draw:g draw:name="Group 13"><draw:ellipse draw:name="Shape 14"`,
		`</draw:g><draw:frame draw:name="Text 15"`,
		`grouped words</text:span></text:p></draw:text-box></draw:frame></draw:g>`,
		`<style:font-face style:name="Grouped Face" svg:font-family="Grouped Face"/>`,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("content.xml lacks %s", want)
		}
	}
	doc, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if text := richdoc.PlainText(doc); !strings.Contains(text, "grouped words") {
		t.Errorf("the words in a group do not read back: %q", text)
	}
}

// TestEditableGroupsRefuse: a group's members are checked like any shape,
// count towards the slide's limit, and nest MaxGroupDepth deep at most.
func TestEditableGroupsRefuse(t *testing.T) {
	p := editable(t)
	g := group()
	g.Group[1].Group[0].W = -1
	p.Slides[0].Shapes = append(p.Slides[0].Shapes, g)
	if _, err := WritePresentation(p); err == nil || !strings.Contains(err.Error(), "member 2: member 1:") {
		t.Errorf("a bad member: %v", err)
	}

	deep := Shape{W: 1, H: 1}
	for i := 0; i < MaxGroupDepth; i++ {
		deep = Shape{Group: []Shape{deep}}
	}
	p = editable(t)
	p.Slides[0].Shapes = append(p.Slides[0].Shapes, deep)
	if _, err := WritePresentation(p); err == nil || !strings.Contains(err.Error(), "nest") {
		t.Errorf("groups %d deep: %v", MaxGroupDepth+1, err)
	}
	p = editable(t)
	p.Slides[0].Shapes = append(p.Slides[0].Shapes, deep.Group[0].Group[0])
	if _, err := WritePresentation(p); err != nil {
		t.Errorf("groups %d deep: %v", MaxGroupDepth-1, err)
	}

	old := maxShapesPerSlide
	maxShapesPerSlide = len(editable(t).Slides[0].Shapes) + 2
	defer func() { maxShapesPerSlide = old }()
	p = editable(t)
	p.Slides[0].Shapes = append(p.Slides[0].Shapes, group())
	if _, err := WritePresentation(p); err == nil || !strings.Contains(err.Error(), "shapes is past") {
		t.Errorf("members past the limit: %v", err)
	}
	wide := Shape{}
	for i := 0; i < 2*maxShapesPerSlide; i++ {
		wide.Group = append(wide.Group, Shape{Group: []Shape{{W: 1, H: 1}}})
	}
	if n := countShapes([]Shape{wide}, 0); n > 2*maxShapesPerSlide+2 {
		t.Errorf("countShapes went on to %d past the limit", n)
	}
}

// TestLibreOfficeOpensTheEditableDeck: LibreOffice renders the text boxes as
// text, those inside a group too.
func TestLibreOfficeOpensTheEditableDeck(t *testing.T) {
	soffice := findSoffice()
	if soffice == "" {
		t.Skip("LibreOffice is not installed")
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	dir := t.TempDir()
	p := editable(t)
	p.Fonts = nil
	p.Slides[0].Shapes = append(p.Slides[0].Shapes, group())
	in := filepath.Join(dir, "deck.odp")
	if err := os.WriteFile(in, writeDeck(t, p), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(soffice, "--headless", "--convert-to", "pdf", "--outdir", dir, in)
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("soffice: %v\n%s", err, out)
	}
	txt, err := exec.Command("pdftotext", filepath.Join(dir, "deck.pdf"), "-").Output()
	if err != nil {
		t.Fatalf("LibreOffice wrote no PDF: %v", err)
	}
	if !strings.Contains(string(txt), "plain") {
		t.Errorf("the PDF LibreOffice made does not hold the slide's text: %q", txt)
	}
	if !strings.Contains(string(txt), "grouped words") {
		t.Errorf("LibreOffice did not draw the text of a group: %q", txt)
	}
}
