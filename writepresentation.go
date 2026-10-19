// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"strings"
	"time"
)

// mimetypeODP is the media type of an OpenDocument Presentation package.
const mimetypeODP = "application/vnd.oasis.opendocument.presentation"

// The slide size is bounded as PowerPoint bounds it — one inch to 56 inches a
// side, in CSS pixels at 96 to the inch — so that a deck written here can be
// converted to .pptx without being refused for its size.
const (
	minSlidePx = 96
	maxSlidePx = 5376
)

// The bounds on what WritePresentation accepts. They are var rather than const
// so that a test can lower them: a test that had to build 64 MiB to see a
// refusal would be a test nobody runs.
var (
	// maxWriteSlides is far more than a talk and far less than [maxPages].
	maxWriteSlides = 1000

	// maxImageBytes is one picture and maxImagesBytes all of them. They are
	// [Parse]'s own ceilings — 64 MiB an entry, 192 MiB a package — less room
	// for the XML, so that what this writes, Parse reads.
	maxImageBytes  = maxPartBytes
	maxImagesBytes = int64(160 << 20)

	// maxTextBytes bounds each piece of text: the notes of one slide, its
	// name, and each document property.
	maxTextBytes = 1 << 20
)

// A Presentation is a deck to write as an .odp: one full-slide picture per
// slide, with its speaker notes.
//
// Width and Height are the slide size in CSS pixels, 96 to the inch — the unit
// the slides were rendered in. They are written in points, three quarters of a
// pixel each, which is exact: a whole number of pixels is at most two decimals
// of a point, where centimetres would round.
type Presentation struct {
	Width, Height int
	Slides        []PresentationSlide

	// The document properties, written to meta.xml when set: dc:title,
	// dc:creator, dc:subject, and one meta:keyword for each comma-separated
	// item of Keywords.
	Title, Author, Subject, Keywords string

	// Created is written as meta:creation-date and dc:date. The zero time
	// writes neither, which is what makes the output the same bytes for the
	// same input.
	Created time.Time
}

// A PresentationSlide is one slide: a picture that covers it, and its notes.
type PresentationSlide struct {
	// Image is the picture, stretched over the whole slide; render it at the
	// slide's aspect ratio. ImageType is "png" or "jpeg", and is checked
	// against the image's own signature.
	Image     []byte
	ImageType string

	// Notes are the speaker notes, one paragraph per line. Empty writes no
	// presentation:notes.
	Notes string

	// Name is the page's draw:name — what Impress shows in its slide pane and
	// what [Parse] reads back as the slide's heading — and the picture's
	// svg:desc, which is what a screen reader says for a slide that is a
	// picture. Empty gives "Slide N".
	Name string
}

// WritePresentation writes a deck as an OpenDocument Presentation package
// (.odp): mimetype first and STORED, the manifest, content.xml, styles.xml,
// meta.xml when there are properties, and the pictures under Pictures/.
//
// ⛔ A slide is a PICTURE, as in marp-cli's --pptx: the rendering is the
// deck, and a slide rebuilt from text boxes would be a different slide in
// every viewer's fonts. So [Parse] reads back each slide's name and picture,
// and none of its words — they are in the picture.
//
// ⛔ The output is deterministic: the same Presentation gives the same bytes.
// No clock is read (see [Presentation.Created]) and every name is a position.
func WritePresentation(p Presentation) ([]byte, error) {
	if err := checkPresentation(p); err != nil {
		return nil, err
	}
	w, h := pt(p.Width), pt(p.Height)
	tw, th := notesThumb(p)
	images := make([]imagePart, 0, len(p.Slides))
	var body strings.Builder
	for i, s := range p.Slides {
		num := strconv.Itoa(i + 1)
		ext, media := "png", "image/png"
		if s.ImageType == "jpeg" {
			ext, media = "jpg", "image/jpeg"
		}
		path := dirPictures + "slide" + num + "." + ext
		images = append(images, imagePart{path: path, mediaType: media, data: s.Image})
		name := s.Name
		if name == "" {
			name = "Slide " + num
		}
		body.WriteString(`<draw:page draw:name="` + escAttr(name) + `" draw:style-name="dp1" draw:master-page-name="Default">`)
		body.WriteString(`<draw:frame draw:name="Picture ` + num + `" draw:style-name="gr1" draw:layer="layout" svg:width="` + w + `" svg:height="` + h + `" svg:x="0pt" svg:y="0pt">`)
		body.WriteString(`<draw:image xlink:href="` + path + `" xlink:type="simple" xlink:show="embed" xlink:actuate="onLoad" draw:mime-type="` + media + `"/>`)
		body.WriteString(`<svg:desc>` + escText(name) + `</svg:desc></draw:frame>`)
		if s.Notes != "" {
			body.WriteString(`<presentation:notes draw:style-name="dp2">`)
			body.WriteString(`<draw:page-thumbnail draw:layer="layout" svg:width="` + tw + `" svg:height="` + th + `" svg:x="54pt" svg:y="90pt" draw:page-number="` + num + `" presentation:class="page"/>`)
			body.WriteString(`<draw:frame draw:layer="layout" svg:width="` + notesTextW + `" svg:height="` + notesTextH + `" svg:x="54pt" svg:y="380pt" presentation:class="notes"><draw:text-box>`)
			for _, line := range strings.Split(strings.ReplaceAll(s.Notes, "\r\n", "\n"), "\n") {
				if line == "" {
					body.WriteString(`<text:p/>`)
					continue
				}
				body.WriteString(`<text:p>` + escText(line) + `</text:p>`)
			}
			body.WriteString(`</draw:text-box></draw:frame></presentation:notes>`)
		}
		body.WriteString(`</draw:page>`)
	}
	return packODP(presContent(body.String()), presStyles(p), presMeta(p), images), nil
}

// checkPresentation refuses a deck outside the bounds above.
func checkPresentation(p Presentation) error {
	for _, d := range []struct {
		what string
		v    int
	}{{"width", p.Width}, {"height", p.Height}} {
		if d.v < minSlidePx || d.v > maxSlidePx {
			return fmt.Errorf("odf: a slide %s of %d px is outside the %d to %d allowed", d.what, d.v, minSlidePx, maxSlidePx)
		}
	}
	if len(p.Slides) == 0 {
		return fmt.Errorf("odf: no slides to write")
	}
	if len(p.Slides) > maxWriteSlides {
		return fmt.Errorf("odf: %d slides is past the %d this writes", len(p.Slides), maxWriteSlides)
	}
	for _, t := range []string{p.Title, p.Author, p.Subject, p.Keywords} {
		if len(t) > maxTextBytes {
			return fmt.Errorf("odf: a document property of %d bytes is past the %d allowed", len(t), maxTextBytes)
		}
	}
	var total int64
	for i, s := range p.Slides {
		if err := checkSlideImage(s); err != nil {
			return fmt.Errorf("odf: slide %d: %w", i+1, err)
		}
		if len(s.Notes) > maxTextBytes || len(s.Name) > maxTextBytes {
			return fmt.Errorf("odf: slide %d: its notes or name are past the %d bytes allowed", i+1, maxTextBytes)
		}
		total += int64(len(s.Image))
		if total > maxImagesBytes {
			return fmt.Errorf("odf: the pictures come to more than the %d bytes allowed", maxImagesBytes)
		}
	}
	return nil
}

// checkSlideImage refuses a picture that is not what its type says.
//
// ⛔ The type becomes the file's extension and its manifest media type, so a
// JPEG called "png" makes a package that says one thing and holds another.
func checkSlideImage(s PresentationSlide) error {
	var magic string
	switch s.ImageType {
	case "png":
		magic = "\x89PNG\r\n\x1a\n"
	case "jpeg":
		magic = "\xff\xd8\xff"
	default:
		return fmt.Errorf("image type %q is not png or jpeg", s.ImageType)
	}
	if len(s.Image) > maxImageBytes {
		return fmt.Errorf("a picture of %d bytes is past the %d allowed", len(s.Image), maxImageBytes)
	}
	if !bytes.HasPrefix(s.Image, []byte(magic)) {
		return fmt.Errorf("the picture is not a %s", s.ImageType)
	}
	return nil
}

// pt writes a length in CSS pixels as points: px × 3/4, exactly.
func pt(px int) string {
	s := strconv.Itoa(px * 3 / 4)
	switch px * 3 % 4 {
	case 1:
		s += ".25"
	case 2:
		s += ".5"
	case 3:
		s += ".75"
	}
	return s + "pt"
}

// The notes page is a portrait sheet, 7.5 × 10 inches as Impress and
// PowerPoint both make it, with the slide's thumbnail above the notes.
const (
	notesPageW = "540pt"
	notesPageH = "720pt"
	notesTextW = "432pt"
	notesTextH = "290pt"
)

// notesThumb is the thumbnail's size: the largest at the slide's aspect ratio
// that fits 432 × 270 points above the notes, so that the slide is neither
// squashed nor — for a portrait deck — drawn over the notes.
func notesThumb(p Presentation) (w, h string) {
	if 432*p.Height <= 270*p.Width {
		return "432pt", strconv.Itoa(432*p.Height/p.Width) + "pt"
	}
	return strconv.Itoa(270*p.Width/p.Height) + "pt", "270pt"
}

// presContent wraps the pages in content.xml. The two automatic styles are the
// pages' — no header, footer, date or number drawn over the picture — and the
// picture frame's, with no line and no fill around it.
func presContent(pages string) string {
	return xmlDecl +
		`<office:document-content ` + odfNamespaces + presNamespace + ` office:version="1.3">` +
		`<office:automatic-styles>` +
		`<style:style style:name="dp1" style:family="drawing-page"><style:drawing-page-properties presentation:display-footer="false" presentation:display-page-number="false" presentation:display-date-time="false"/></style:style>` +
		`<style:style style:name="dp2" style:family="drawing-page"><style:drawing-page-properties presentation:display-header="false" presentation:display-footer="false" presentation:display-page-number="false" presentation:display-date-time="false"/></style:style>` +
		`<style:style style:name="gr1" style:family="graphic"><style:graphic-properties draw:stroke="none" draw:fill="none"/></style:style>` +
		`</office:automatic-styles>` +
		`<office:body><office:presentation>` + pages + `</office:presentation></office:body></office:document-content>`
}

// presStyles writes styles.xml: the slide's page layout at the deck's size,
// the notes page's, and the one master page every slide names.
func presStyles(p Presentation) string {
	w, h := pt(p.Width), pt(p.Height)
	orient := "landscape"
	if p.Width < p.Height {
		orient = "portrait"
	}
	return xmlDecl +
		`<office:document-styles ` + odfNamespaces + presNamespace + ` office:version="1.3">` +
		`<office:styles/>` +
		`<office:automatic-styles>` +
		`<style:page-layout style:name="PM1"><style:page-layout-properties fo:margin-top="0pt" fo:margin-bottom="0pt" fo:margin-left="0pt" fo:margin-right="0pt" fo:page-width="` + w + `" fo:page-height="` + h + `" style:print-orientation="` + orient + `"/></style:page-layout>` +
		`<style:page-layout style:name="PM2"><style:page-layout-properties fo:margin-top="0pt" fo:margin-bottom="0pt" fo:margin-left="0pt" fo:margin-right="0pt" fo:page-width="` + notesPageW + `" fo:page-height="` + notesPageH + `" style:print-orientation="portrait"/></style:page-layout>` +
		`<style:style style:name="Mdp1" style:family="drawing-page"><style:drawing-page-properties draw:fill="none"/></style:style>` +
		`</office:automatic-styles>` +
		`<office:master-styles><style:master-page style:name="Default" style:page-layout-name="PM1" draw:style-name="Mdp1">` +
		`<presentation:notes style:page-layout-name="PM2"/>` +
		`</style:master-page></office:master-styles></office:document-styles>`
}

// presMeta writes meta.xml, or "" when there is nothing to say.
func presMeta(p Presentation) string {
	var b strings.Builder
	for _, f := range []struct{ el, v string }{{"dc:title", p.Title}, {"dc:subject", p.Subject}, {"dc:creator", p.Author}} {
		if f.v != "" {
			b.WriteString(`<` + f.el + `>` + escText(f.v) + `</` + f.el + `>`)
		}
	}
	for _, k := range strings.Split(p.Keywords, ",") {
		if k = strings.TrimSpace(k); k != "" {
			b.WriteString(`<meta:keyword>` + escText(k) + `</meta:keyword>`)
		}
	}
	if !p.Created.IsZero() {
		t := p.Created.UTC().Format("2006-01-02T15:04:05Z")
		b.WriteString(`<meta:creation-date>` + t + `</meta:creation-date><dc:date>` + t + `</dc:date>`)
	}
	if b.Len() == 0 {
		return ""
	}
	return xmlDecl + `<office:document-meta xmlns:office="` + nsOffice + `" xmlns:dc="` + nsDC + `" xmlns:meta="` + nsMeta + `" office:version="1.3"><office:meta>` +
		b.String() + `</office:meta></office:document-meta>`
}

// packODP assembles the container the way [Write] does, with the
// presentation's media type. See the note on (*writer).pack for why the
// archive/zip errors are not threaded out.
func packODP(content, styles, meta string, images []imagePart) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stored(zw, partMimetype, []byte(mimetypeODP))
	store := func(name, data string) {
		f, _ := zw.Create(name)
		io.WriteString(f, data)
	}
	store(partManifest, buildManifest(mimetypeODP, meta != "", images))
	store(partContent, content)
	store(partStyles, styles)
	if meta != "" {
		store(partMeta, meta)
	}
	for _, img := range images {
		// ⛔ STORED: a PNG or a JPEG is already compressed, and deflating it
		// again costs time for nothing — the same choice LibreOffice makes.
		stored(zw, img.path, img.data)
	}
	zw.Close()
	return buf.Bytes()
}

// stored writes an uncompressed entry whose sizes and CRC are in its LOCAL
// header, with no data descriptor after it.
//
// ⛔⛔ archive/zip's CreateHeader leaves those fields zero, sets flag bit 3,
// and writes the real values in a descriptor AFTER the data. That is legal
// ZIP, and LibreOffice refuses it for the mimetype entry with nothing but
// "source file could not be loaded". Measured 2026-10-19 on LibreOffice's
// headless converter: every .odt [Write] had ever produced was refused, and
// changing this one entry — nothing else — made the same document open. Our
// own reader never noticed, because archive/zip reads descriptors fine.
func stored(zw *zip.Writer, name string, data []byte) {
	f, _ := zw.CreateRaw(&zip.FileHeader{
		Name: name, Method: zip.Store, CRC32: crc32.ChecksumIEEE(data),
		CompressedSize64: uint64(len(data)), UncompressedSize64: uint64(len(data)),
	})
	f.Write(data)
}

// presNamespace is the one namespace a presentation needs that a text
// document does not.
const presNamespace = ` xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0"`
