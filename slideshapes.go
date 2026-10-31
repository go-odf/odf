// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// A Shape is one element of an editable slide (ODF 1.3 Part 3 §10.3, §10.4):
// a rectangle, an ellipse or a path filled with a colour, a text box, or a
// picture. Shapes are drawn in order, the first at the back.
//
// Positions and sizes are in CSS pixels, as the slide's own size is.
type Shape struct {
	X, Y, W, H float64

	// Kind selects the element: ShapeRect (draw:rect, corners of Radius
	// pixels), ShapeEllipse (draw:ellipse), ShapePath (draw:path, §10.3.7:
	// Paths, closed outlines relative to the box, whose overlaps are holes,
	// svg:fill-rule evenodd), ShapeText (a draw:frame holding a
	// draw:text-box, §10.4.2–10.4.3, one paragraph of Text with no padding
	// and no wrapping) or ShapeImage (a draw:frame holding a draw:image of
	// Presentation.Images[Image-1]).
	Kind   ShapeKind
	Radius float64
	Paths  [][]PathCmd
	Text   []TextRun
	Image  int

	// Fill is the inside's colour; a zero alpha is no fill.
	Fill RGBA

	// NonZero fills a ShapePath by the nonzero rule instead of evenodd
	// (svg:fill-rule, §20.406): what an SVG path filled by its default rule
	// needs, where subpaths drawn the same way round overlap without a hole.
	NonZero bool

	// Name is the draw:name; for a picture it is also its svg:desc.
	Name string

	// Group, when not empty, makes the shape a group (draw:g, §10.3.15) of
	// these shapes, which a viewer selects and moves as one: an SVG drawing,
	// a formula. A draw:g has no box of its own: its members are placed in
	// page coordinates like any shape, and Kind and the rest are ignored. A
	// group may hold groups, MaxGroupDepth deep; every shape in it counts
	// towards the slide's limit.
	Group []Shape
}

// MaxGroupDepth bounds how deep groups nest: a reader walks the tree
// recursively.
const MaxGroupDepth = 32

// ShapeKind is what a Shape is.
type ShapeKind uint8

// The shapes.
const (
	ShapeRect ShapeKind = iota
	ShapeEllipse
	ShapePath
	ShapeText
	ShapeImage
)

// PathCmd is one command of a path: 'M' move to P[0], 'L' line to P[0], 'C'
// cubic Bézier through P[0] and P[1] to P[2], 'Z' close; in pixels.
type PathCmd struct {
	Op byte
	P  [3][2]float64
}

// RGBA is a colour with an alpha: 255 is opaque.
type RGBA struct{ R, G, B, A uint8 }

// TextRun is a stretch of text in one style. Font is a typeface name (an
// embedded one, see Presentation.Fonts, or one the viewer has); Size is in
// points. Link, when set, is an http, https or mailto URL.
type TextRun struct {
	Text         string
	Font         string
	Size         float64
	Bold, Italic bool
	Color        RGBA
	Link         string
	// Spacing is added after each character, in points (fo:letter-spacing).
	Spacing float64
}

// Image is a picture shapes refer to by its position in Presentation.Images,
// plus one, stored once however many slides show it.
type Image struct {
	Data []byte
	Type string // "png" or "jpeg"
}

// Font is a typeface embedded in the package (style:font-face with an
// svg:font-face-uri, §16.23–16.26): TrueType data for each of its faces, any
// of which may be nil. Embed only fonts whose licence allows it.
type Font struct {
	Family                          string
	Regular, Bold, Italic, BoldItal []byte
}

// Bounds on editable slides.
var (
	maxShapesPerSlide = 20000
	maxFontBytes      = 32 << 20
	maxFonts          = 64
	maxCoordinate     = float64(1 << 30)
)

func checkShapes(p Presentation) error {
	if len(p.Fonts) > maxFonts {
		return fmt.Errorf("odf: %d embedded fonts is past the %d allowed", len(p.Fonts), maxFonts)
	}
	for _, f := range p.Fonts {
		if f.Family == "" || len(f.Family) > 256 {
			return fmt.Errorf("odf: an embedded font needs a family name of at most 256 bytes")
		}
		for _, d := range [][]byte{f.Regular, f.Bold, f.Italic, f.BoldItal} {
			if len(d) > maxFontBytes {
				return fmt.Errorf("odf: an embedded face of %d bytes is past the %d allowed", len(d), maxFontBytes)
			}
		}
	}
	var total int64
	for i, img := range p.Images {
		if err := checkSlideImage(PresentationSlide{Image: img.Data, ImageType: img.Type}); err != nil {
			return fmt.Errorf("odf: image %d: %w", i+1, err)
		}
		total += int64(len(img.Data))
	}
	for _, s := range p.Slides {
		total += int64(len(s.Image))
	}
	if total > maxImagesBytes {
		return fmt.Errorf("odf: the pictures come to more than the %d bytes allowed", maxImagesBytes)
	}
	for i, s := range p.Slides {
		if n := countShapes(s.Shapes, 0); n > maxShapesPerSlide {
			return fmt.Errorf("odf: slide %d: %d shapes is past the %d allowed", i+1, n, maxShapesPerSlide)
		}
		for j, sh := range s.Shapes {
			if err := checkShape(sh, len(p.Images), 0); err != nil {
				return fmt.Errorf("odf: slide %d shape %d: %w", i+1, j+1, err)
			}
		}
	}
	return nil
}

// countShapes counts the shapes of a tree, groups and their members, and
// stops counting past the limit (or past MaxGroupDepth, which checkShape
// refuses): it bounds its own work.
func countShapes(shapes []Shape, depth int) int {
	n := len(shapes)
	if depth >= MaxGroupDepth {
		return n
	}
	for _, sh := range shapes {
		if n > maxShapesPerSlide {
			return n
		}
		n += countShapes(sh.Group, depth+1)
	}
	return n
}

// eachShape calls f on every shape of the tree, members after their group.
func eachShape(shapes []Shape, f func(Shape)) {
	for _, sh := range shapes {
		f(sh)
		eachShape(sh.Group, f)
	}
}

func finiteIn(v float64) bool { return v > -maxCoordinate && v < maxCoordinate }

func checkShape(sh Shape, images, depth int) error {
	if !finiteIn(sh.X) || !finiteIn(sh.Y) || !(sh.W >= 0 && sh.W < maxCoordinate) || !(sh.H >= 0 && sh.H < maxCoordinate) {
		return fmt.Errorf("its box is out of range")
	}
	if len(sh.Name) > maxTextBytes {
		return fmt.Errorf("its name is past the %d bytes allowed", maxTextBytes)
	}
	if len(sh.Group) > 0 {
		if depth+1 >= MaxGroupDepth {
			return fmt.Errorf("groups nest past the %d levels allowed", MaxGroupDepth)
		}
		for k, c := range sh.Group {
			if err := checkShape(c, images, depth+1); err != nil {
				return fmt.Errorf("member %d: %w", k+1, err)
			}
		}
		return nil
	}
	switch sh.Kind {
	case ShapeRect, ShapeEllipse:
	case ShapePath:
		for _, path := range sh.Paths {
			for _, c := range path {
				if !strings.ContainsRune("MLCZ", rune(c.Op)) || c.Op == 0 {
					return fmt.Errorf("path command %q is not M, L, C or Z", c.Op)
				}
				for _, pt := range c.P {
					if !finiteIn(pt[0]) || !finiteIn(pt[1]) {
						return fmt.Errorf("a path point is out of range")
					}
				}
			}
		}
	case ShapeText:
		for _, r := range sh.Text {
			if len(r.Text) > maxTextBytes || len(r.Font) > 256 {
				return fmt.Errorf("a text run or its font name is too long")
			}
			if !(r.Size >= 1 && r.Size <= 4000) {
				return fmt.Errorf("a font size of %v pt is outside 1 to 4000", r.Size)
			}
			if !(r.Spacing > -4000 && r.Spacing < 4000) {
				return fmt.Errorf("a letter spacing of %v pt is outside ±4000", r.Spacing)
			}
			if r.Link != "" && !linkOK(r.Link) {
				return fmt.Errorf("link %q is not an http, https or mailto URL", r.Link)
			}
		}
	case ShapeImage:
		if sh.Image < 1 || sh.Image > images {
			return fmt.Errorf("it shows image %d of %d", sh.Image, images)
		}
	default:
		return fmt.Errorf("shape kind %d is unknown", sh.Kind)
	}
	return nil
}

// linkOK accepts the URLs a run may open: a link that runs a script or reaches
// into the file system has no business in a slide.
func linkOK(s string) bool {
	if len(s) > 8192 {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != ""
	}
	return false
}

// xmlChars drops what XML 1.0 cannot carry at all (§2.2 Char): control
// characters but tab, line feed and carriage return, and U+FFFE/U+FFFF.
// Escaping does not help there — a document holding them is not well-formed.
func xmlChars(s string) string {
	ok := true
	for _, r := range s {
		if !xmlChar(r) {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if xmlChar(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func xmlChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

// ptLen writes a length in pixels as points, three decimals at most.
func ptLen(px float64) string {
	return strconv.FormatFloat(math.Round(px*750)/1000, 'f', -1, 64) + "pt"
}

func hexColor(c RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// shapeStyles collects the automatic styles the shapes need, one per
// distinct look, named in the order first met so output is deterministic.
type shapeStyles struct {
	graphic, text  map[string]string
	gOrder, tOrder []string
}

func newShapeStyles() *shapeStyles {
	return &shapeStyles{graphic: map[string]string{}, text: map[string]string{}}
}

// graphicStyle is the style:graphic-properties of a filled shape, a text
// frame (no fill, no padding, no wrapping) or a picture frame.
func (ss *shapeStyles) graphicStyle(props string) string {
	if n, ok := ss.graphic[props]; ok {
		return n
	}
	n := "sg" + strconv.Itoa(len(ss.gOrder)+1)
	ss.graphic[props] = n
	ss.gOrder = append(ss.gOrder, props)
	return n
}

func (ss *shapeStyles) textStyle(r TextRun) string {
	var p strings.Builder
	if r.Font != "" {
		p.WriteString(` style:font-name="` + escAttr(xmlChars(r.Font)) + `" fo:font-family="` + escAttr(xmlChars(r.Font)) + `"`)
	}
	p.WriteString(` fo:font-size="` + strconv.FormatFloat(math.Round(r.Size*100)/100, 'f', -1, 64) + `pt" fo:color="` + hexColor(r.Color) + `"`)
	if r.Color.A < 255 {
		p.WriteString(` loext:opacity="` + strconv.Itoa(int(r.Color.A)*100/255) + `%"`)
	}
	if sp := math.Round(r.Spacing*100) / 100; sp != 0 {
		p.WriteString(` fo:letter-spacing="` + strconv.FormatFloat(sp, 'f', -1, 64) + `pt"`)
	}
	if r.Bold {
		p.WriteString(` fo:font-weight="bold"`)
	} else {
		p.WriteString(` fo:font-weight="normal"`)
	}
	if r.Italic {
		p.WriteString(` fo:font-style="italic"`)
	} else {
		p.WriteString(` fo:font-style="normal"`)
	}
	key := p.String()
	if n, ok := ss.text[key]; ok {
		return n
	}
	n := "st" + strconv.Itoa(len(ss.tOrder)+1)
	ss.text[key] = n
	ss.tOrder = append(ss.tOrder, key)
	return n
}

// xml writes the collected styles, plus the one paragraph style text boxes
// use: no margins, no indent, single line height.
func (ss *shapeStyles) xml() string {
	var b strings.Builder
	for _, props := range ss.gOrder {
		b.WriteString(`<style:style style:name="` + ss.graphic[props] + `" style:family="graphic"><style:graphic-properties` + props + `/></style:style>`)
	}
	for _, props := range ss.tOrder {
		b.WriteString(`<style:style style:name="` + ss.text[props] + `" style:family="text"><style:text-properties` + props + `/></style:style>`)
	}
	b.WriteString(`<style:style style:name="sp1" style:family="paragraph"><style:paragraph-properties fo:margin-left="0pt" fo:margin-right="0pt" fo:margin-top="0pt" fo:margin-bottom="0pt" fo:text-indent="0pt" fo:line-height="100%"/></style:style>`)
	return b.String()
}

func fillProps(c RGBA) string {
	if c.A == 0 {
		return ` draw:stroke="none" draw:fill="none"`
	}
	s := ` draw:stroke="none" draw:fill="solid" draw:fill-color="` + hexColor(c) + `"`
	if c.A < 255 {
		s += ` draw:opacity="` + strconv.FormatFloat(math.Round(float64(c.A)*1000/255)/10, 'f', -1, 64) + `%"`
	}
	return s
}

// writeShapes writes a page's shapes. imagePath gives Images[i-1]'s path.
func writeShapes(b *strings.Builder, shapes []Shape, ss *shapeStyles, imagePath func(int) (path, media string)) {
	next := 1
	writeShapeList(b, shapes, &next, ss, imagePath)
}

// writeShapeList writes shapes in order, numbering the unnamed ones from
// *next on, the members of a group after the group itself.
func writeShapeList(b *strings.Builder, shapes []Shape, next *int, ss *shapeStyles, imagePath func(int) (path, media string)) {
	for _, sh := range shapes {
		num := strconv.Itoa(*next)
		*next++
		box := ` svg:x="` + ptLen(sh.X) + `" svg:y="` + ptLen(sh.Y) + `" svg:width="` + ptLen(sh.W) + `" svg:height="` + ptLen(sh.H) + `"`
		name := xmlChars(sh.Name)
		if len(sh.Group) > 0 {
			if name == "" {
				name = "Group " + num
			}
			b.WriteString(`<draw:g draw:name="` + escAttr(name) + `">`)
			writeShapeList(b, sh.Group, next, ss, imagePath)
			b.WriteString(`</draw:g>`)
			continue
		}
		switch sh.Kind {
		case ShapeImage:
			if name == "" {
				name = "Picture " + num
			}
			path, media := imagePath(sh.Image)
			st := ss.graphicStyle(` draw:stroke="none" draw:fill="none"`)
			b.WriteString(`<draw:frame draw:name="` + escAttr(name) + `" draw:style-name="` + st + `" draw:layer="layout"` + box + `>`)
			b.WriteString(`<draw:image xlink:href="` + path + `" xlink:type="simple" xlink:show="embed" xlink:actuate="onLoad" draw:mime-type="` + media + `"/>`)
			b.WriteString(`<svg:desc>` + escText(name) + `</svg:desc></draw:frame>`)
		case ShapeText:
			if name == "" {
				name = "Text " + num
			}
			st := ss.graphicStyle(` draw:stroke="none" draw:fill="none" fo:padding-top="0pt" fo:padding-bottom="0pt" fo:padding-left="0pt" fo:padding-right="0pt" fo:wrap-option="no-wrap" draw:auto-grow-height="false" draw:auto-grow-width="false" draw:textarea-vertical-align="top"`)
			b.WriteString(`<draw:frame draw:name="` + escAttr(name) + `" draw:style-name="` + st + `" draw:layer="layout"` + box + `><draw:text-box><text:p text:style-name="sp1">`)
			for _, r := range sh.Text {
				span := `<text:span text:style-name="` + ss.textStyle(r) + `">` + escText(xmlChars(r.Text)) + `</text:span>`
				if r.Link != "" {
					span = `<text:a xlink:type="simple" xlink:href="` + escAttr(r.Link) + `">` + span + `</text:a>`
				}
				b.WriteString(span)
			}
			b.WriteString(`</text:p></draw:text-box></draw:frame>`)
		default:
			if name == "" {
				name = "Shape " + num
			}
			props := fillProps(sh.Fill)
			switch sh.Kind {
			case ShapeEllipse:
				b.WriteString(`<draw:ellipse draw:name="` + escAttr(name) + `" draw:style-name="` + ss.graphicStyle(props) + `" draw:layer="layout"` + box + `/>`)
			case ShapePath:
				if sh.NonZero {
					props += ` svg:fill-rule="nonzero"`
				} else {
					props += ` svg:fill-rule="evenodd"`
				}
				w, h := math.Max(sh.W, 0.01), math.Max(sh.H, 0.01)
				// The viewBox is the box in hundredths of a pixel.
				b.WriteString(`<draw:path draw:name="` + escAttr(name) + `" draw:style-name="` + ss.graphicStyle(props) + `" draw:layer="layout"` + box +
					` svg:viewBox="0 0 ` + vb(w) + ` ` + vb(h) + `" svg:d="`)
				for _, path := range sh.Paths {
					for _, c := range path {
						switch c.Op {
						case 'M', 'L':
							b.WriteString(string(c.Op) + vb(c.P[0][0]) + " " + vb(c.P[0][1]) + " ")
						case 'C':
							b.WriteString("C" + vb(c.P[0][0]) + " " + vb(c.P[0][1]) + " " + vb(c.P[1][0]) + " " + vb(c.P[1][1]) + " " + vb(c.P[2][0]) + " " + vb(c.P[2][1]) + " ")
						case 'Z':
							b.WriteString("Z ")
						}
					}
				}
				b.WriteString(`"/>`)
			default:
				radius := ""
				if sh.Radius > 0 {
					radius = ` draw:corner-radius="` + ptLen(math.Min(sh.Radius, math.Min(sh.W, sh.H)/2)) + `"`
				}
				b.WriteString(`<draw:rect draw:name="` + escAttr(name) + `" draw:style-name="` + ss.graphicStyle(props) + `" draw:layer="layout"` + box + radius + `/>`)
			}
		}
	}
}

// vb is a coordinate in the path's viewBox: hundredths of a pixel.
func vb(px float64) string { return strconv.FormatInt(int64(math.Round(px*100)), 10) }

// fontDecls writes office:font-face-decls: one style:font-face per embedded
// face, its file under Fonts/, and the faces of every other family the runs
// name, declared so a viewer knows to look for them.
func fontDecls(p Presentation, faces []fontPart) string {
	var b strings.Builder
	b.WriteString(`<office:font-face-decls>`)
	declared := map[string]bool{}
	for _, f := range faces {
		b.WriteString(`<style:font-face style:name="` + escAttr(f.styleName) + `" svg:font-family="` + escAttr(f.family) + `"` + f.attrs + `><svg:font-face-src><svg:font-face-uri xlink:href="` + f.path + `" xlink:type="simple"><svg:font-face-format svg:string="truetype"/></svg:font-face-uri></svg:font-face-src></style:font-face>`)
		declared[f.styleName] = true
	}
	var names []string
	for _, s := range p.Slides {
		eachShape(s.Shapes, func(sh Shape) {
			for _, r := range sh.Text {
				if r.Font != "" && !declared[r.Font] {
					declared[r.Font] = true
					names = append(names, xmlChars(r.Font))
				}
			}
		})
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString(`<style:font-face style:name="` + escAttr(n) + `" svg:font-family="` + escAttr(n) + `"/>`)
	}
	b.WriteString(`</office:font-face-decls>`)
	return b.String()
}

// fontPart is one embedded face: its file and its declaration.
type fontPart struct {
	path, family, styleName, attrs string
	data                           []byte
}

func fontParts(p Presentation) []fontPart {
	var out []fontPart
	for i, f := range p.Fonts {
		family := xmlChars(f.Family)
		for j, face := range []struct {
			data          []byte
			suffix, attrs string
		}{
			{f.Regular, "", ` svg:font-weight="normal" svg:font-style="normal"`},
			{f.Bold, " Bold", ` svg:font-weight="bold" svg:font-style="normal"`},
			{f.Italic, " Italic", ` svg:font-weight="normal" svg:font-style="italic"`},
			{f.BoldItal, " Bold Italic", ` svg:font-weight="bold" svg:font-style="italic"`},
		} {
			if face.data == nil {
				continue
			}
			// The regular face's declaration carries the family's own name,
			// which is what style:font-name refers to.
			out = append(out, fontPart{
				path:      "Fonts/font" + strconv.Itoa(i+1) + "_" + strconv.Itoa(j) + ".ttf",
				family:    family,
				styleName: family + face.suffix,
				attrs:     face.attrs,
				data:      face.data,
			})
		}
	}
	return out
}
