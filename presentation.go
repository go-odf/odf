// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"encoding/xml"

	"github.com/go-richdoc/richdoc"
)

// A presentation and a drawing are the same document: office:presentation and
// office:drawing both hold draw:page elements, and a page holds SHAPES rather
// than running text. The words are inside those shapes, which is why walking
// office:text found nothing in either — the same way it once found nothing in
// a spreadsheet.
//
// ⛔ Every rule below was read off a file LibreOffice WROTE, not off the
// specification. Three of them contradict what the specification alone
// suggests, and the one rule the specification does suggest — "the title is
// the frame with presentation:class='title'" — is the one that does not work:
// Impress drops that attribute from any frame that is not a master-page
// placeholder, so a converted deck has no classed title frame at all.
// maxPages bounds a drawing the way maxParts bounds a package. A page costs a
// heading and a walk, so the ceiling is generous; it is here so that a file
// claiming four million pages is refused rather than walked.
const maxPages = 65536

// parsePages walks a presentation or drawing body. The caller has already
// consumed the body's start element.
func (p *parser) parsePages(dec *xml.Decoder, end xml.Name) ([]richdoc.Block, error) {
	var blocks []richdoc.Block
	pages := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.EndElement:
			if e.Name == end {
				return blocks, nil
			}
		case xml.StartElement:
			if e.Name.Local != "page" {
				// presentation:settings, draw:layer-set and the rest describe
				// how to SHOW the pages. None of it is content.
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			pages++
			if pages > maxPages {
				return nil, errTooManyPages{limit: maxPages}
			}
			bs, err := p.parsePage(dec, e)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, bs...)
		}
	}
}

// parsePage turns one draw:page into a heading and the blocks found in its
// shapes.
//
// ⛔ The heading is the page's own draw:name, NOT a guess at which shape holds
// the title. Impress keeps draw:name through a conversion and drops
// presentation:class from every frame it does not consider a placeholder, so a
// title-by-class rule finds nothing on a real deck while looking perfectly
// reasonable in the source. A name is dull — "page1" on an untitled slide —
// but it is always there and it is always right.
func (p *parser) parsePage(dec *xml.Decoder, se xml.StartElement) ([]richdoc.Block, error) {
	blocks := []richdoc.Block{nameHeading(elemName(se))}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.EndElement:
			if e.Name == se.Name {
				return blocks, nil
			}
		case xml.StartElement:
			// ⛔ Speaker notes are not on the slide. presentation:notes holds a
			// frame with text in it that reads exactly like body text, and
			// walking into it puts words the audience never saw into the
			// document as though they had been on screen.
			if e.Name.Local == "notes" {
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			bs, err := p.parseShape(dec, e)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, bs...)
		}
	}
}

// parseShape finds the content inside one shape on a page.
//
// ⛔ It does not enumerate the shape types. ODF has dozens — draw:frame,
// draw:custom-shape, draw:rect, draw:ellipse, draw:polygon, draw:connector,
// draw:caption, draw:g and more — and any of them may carry text. A list of
// the ones I happened to think of would silently drop the words in the others,
// so this DESCENDS instead and collects the text it meets, whatever is wrapped
// around it. The shape's own geometry contributes nothing and falls out
// naturally: draw:enhanced-geometry holds no text:p.

// elemName reads a namespaced name attribute: table:name on a sheet,
// draw:name on a page. The namespace is INSISTED upon, because an unprefixed
// "name" attribute on some other element is not this.
func elemName(se xml.StartElement) string {
	for _, a := range se.Attr {
		if a.Name.Local == "name" && a.Name.Space != "" {
			return a.Value
		}
	}
	return ""
}

// nameHeading is the heading that separates one sheet or page from the next,
// so that a reader can tell which slide a sentence was on.
//
// ⛔ A VALUE, not a pointer. richdoc's blocks are a closed interface set whose
// marker method has a value receiver, so *Heading satisfies Block too — it
// compiles, it type-switches, and it matches no case any consumer wrote. The
// first draft returned one: the headings reached the document, the block count
// was right, and they came out of the PDF chain as nothing at all.
func nameHeading(name string) richdoc.Block {
	return richdoc.Heading{
		Level:   1,
		Inlines: []richdoc.Inline{richdoc.Text{Value: name}},
	}
}
func (p *parser) parseShape(dec *xml.Decoder, se xml.StartElement) ([]richdoc.Block, error) {
	var blocks []richdoc.Block
	var href, alt, title string
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.EndElement:
			if depth == 0 && e.Name == se.Name {
				// ⛔ The picture is emitted only if the shape held nothing
				// else. Impress writes a draw:image PREVIEW into the same
				// frame as a table — Pictures/TablePreview1.svm — and
				// emitting it would put a broken reference, in a format
				// nothing on the web reads, next to the content it is only a
				// picture OF. A frame holding nothing else is a real picture
				// frame, and then the picture IS the content.
				if len(blocks) == 0 && href != "" {
					return []richdoc.Block{richdoc.Paragraph{
						Inlines: []richdoc.Inline{richdoc.Image{
							URL: p.imageURL(href), Alt: alt, Title: title,
						}},
					}}, nil
				}
				return blocks, nil
			}
			depth--
		case xml.StartElement:
			switch e.Name.Local {
			case "p", "h", "list", "table":
				bs, err := p.parseBlock(dec, e, dec.InputOffset())
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, bs...)
			case "image":
				// The href is on the element ITSELF. parseImage is
				// frame-shaped — it descends looking for a child named
				// "image" — so handing it this element returned a picture
				// with no URL at all, which read as "the picture was lost".
				href = attrLocal(e, "href")
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			case "title":
				// svg:title and svg:desc are the frame's own children, beside
				// draw:image rather than inside it, so they are collected
				// here on the way past.
				if title, err = p.readFlatText(dec, e.Name, false); err != nil {
					return nil, err
				}
			case "desc":
				if alt, err = p.readFlatText(dec, e.Name, false); err != nil {
					return nil, err
				}
			default:
				depth++
			}
		}
	}
}
