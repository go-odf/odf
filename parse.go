// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

package odf

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/go-richdoc/richdoc"
)

var (
	errNoContent  = errors.New("odf: package has no content.xml")
	errBadDataURI = errors.New("odf: malformed data URI in image")
)

// Parse converts the bytes of an ODT (OpenDocument Text) package into a
// [richdoc.Document].
//
// It opens the ZIP container, resolves inline formatting in content.xml against
// the automatic styles there and the named styles in styles.xml, reads embedded
// image bytes (surfaced as data: URIs) using META-INF/manifest.xml for their
// media types, and reads document metadata from meta.xml. Unrecognized elements
// are preserved verbatim as [richdoc.RawBlock] / [richdoc.RawInline] with Format
// "odf". A container that is not a ZIP, that lacks content.xml, that holds a
// corrupt entry, or whose XML is malformed returns an error.
func Parse(src []byte) (*richdoc.Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		return nil, err
	}
	p := &parser{
		parts:    make(map[string][]byte, len(zr.File)),
		styles:   make(map[string]styleProps),
		lists:    make(map[string]listInfo),
		manifest: make(map[string]string),
	}
	for _, f := range zr.File {
		data, err := slurp(f)
		if err != nil {
			return nil, err
		}
		p.parts[f.Name] = data
	}

	content, ok := p.parts[partContent]
	if !ok {
		return nil, errNoContent
	}
	p.content = content

	if data, ok := p.parts[partManifest]; ok {
		if err := p.parseManifest(data); err != nil {
			return nil, err
		}
	}
	if data, ok := p.parts[partStyles]; ok {
		if err := p.scanStyles(data); err != nil {
			return nil, err
		}
	}

	blocks, err := p.parseContent()
	if err != nil {
		return nil, err
	}

	doc := &richdoc.Document{Blocks: blocks}
	if data, ok := p.parts[partMeta]; ok {
		meta, err := parseMeta(data)
		if err != nil {
			return nil, err
		}
		doc.Meta = meta
	}
	return doc, nil
}

type parser struct {
	parts    map[string][]byte // ZIP entry name -> bytes
	styles   map[string]styleProps
	lists    map[string]listInfo
	manifest map[string]string // full-path -> media-type
	content  []byte
}

type styleProps struct {
	parent      string
	fontWeight  string
	fontStyle   string
	lineThrough string
	fontName    string
	textAlign   string
}

type listInfo struct {
	ordered bool
	start   int
}

// slurp reads a single ZIP entry in full.
func slurp(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// parseManifest records the media type of every package part.
func (p *parser) parseManifest(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "file-entry" {
			p.manifest[attrLocal(se, "full-path")] = attrLocal(se, "media-type")
		}
	}
}

// scanStyles collects every style:style and text:list-style in styles.xml.
func (p *parser) scanStyles(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "style":
			if err := p.collectStyle(dec, se); err != nil {
				return err
			}
		case "list-style":
			if err := p.collectListStyle(dec, se); err != nil {
				return err
			}
		}
	}
}

// parseContent walks content.xml, gathering the automatic styles and then the
// office:text body.
func (p *parser) parseContent() ([]richdoc.Block, error) {
	dec := xml.NewDecoder(bytes.NewReader(p.content))
	var blocks []richdoc.Block
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return blocks, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "automatic-styles":
			if err := p.collectStylesContainer(dec, se); err != nil {
				return nil, err
			}
		case "text":
			blocks, err = p.parseBlocks(dec, se.Name)
			if err != nil {
				return nil, err
			}
		}
	}
}

func (p *parser) collectStylesContainer(dec *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "style":
				if err := p.collectStyle(dec, e); err != nil {
					return err
				}
			case "list-style":
				if err := p.collectListStyle(dec, e); err != nil {
					return err
				}
			default:
				if err := dec.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if e.Name == start.Name {
				return nil
			}
		}
	}
}

func (p *parser) collectStyle(dec *xml.Decoder, start xml.StartElement) error {
	props := styleProps{parent: attrLocal(start, "parent-style-name")}
	name := attrLocal(start, "name")
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "text-properties":
				setIf(&props.fontWeight, attrLocal(e, "font-weight"))
				setIf(&props.fontStyle, attrLocal(e, "font-style"))
				setIf(&props.lineThrough, attrLocal(e, "text-line-through-style"))
				setIf(&props.fontName, attrLocal(e, "font-name"))
			case "paragraph-properties":
				setIf(&props.textAlign, attrLocal(e, "text-align"))
			}
			if err := dec.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if e.Name == start.Name {
				if name != "" {
					p.styles[name] = props
				}
				return nil
			}
		}
	}
}

func (p *parser) collectListStyle(dec *xml.Decoder, start xml.StartElement) error {
	name := attrLocal(start, "name")
	info := listInfo{start: 1}
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "list-level-style-number":
				info.ordered = true
				if sv := attrLocal(e, "start-value"); sv != "" {
					if n, err := strconv.Atoi(sv); err == nil {
						info.start = n
					}
				}
			case "list-level-style-bullet":
				info.ordered = false
			}
			if err := dec.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if e.Name == start.Name {
				if name != "" {
					p.lists[name] = info
				}
				return nil
			}
		}
	}
}

// parseBlocks reads block-level children until the end element matching end.
func (p *parser) parseBlocks(dec *xml.Decoder, end xml.Name) ([]richdoc.Block, error) {
	var blocks []richdoc.Block
	for {
		off := dec.InputOffset()
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
			bs, err := p.parseBlock(dec, e, off)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, bs...)
		}
	}
}

func (p *parser) parseBlock(dec *xml.Decoder, se xml.StartElement, off int64) ([]richdoc.Block, error) {
	switch se.Name.Local {
	case "h":
		return p.one(p.parseHeading(dec, se))
	case "p":
		return p.one(p.parseParagraph(dec, se))
	case "list":
		return p.one(p.parseList(dec, se))
	case "table":
		return p.one(p.parseTable(dec, se))
	case "section":
		return p.parseSection(dec, se)
	default:
		raw, err := p.captureRaw(dec, off)
		if err != nil {
			return nil, err
		}
		return []richdoc.Block{richdoc.RawBlock{Format: "odf", Text: raw}}, nil
	}
}

// one lifts a single-block result into the slice form parseBlock returns.
func (p *parser) one(b richdoc.Block, err error) ([]richdoc.Block, error) {
	if err != nil {
		return nil, err
	}
	return []richdoc.Block{b}, nil
}

func (p *parser) parseHeading(dec *xml.Decoder, se xml.StartElement) (richdoc.Block, error) {
	level := 1
	if lv := attrLocal(se, "outline-level"); lv != "" {
		if n, err := strconv.Atoi(lv); err == nil && n >= 1 {
			level = n
			if level > 6 {
				level = 6
			}
		}
	}
	inlines, err := p.parseInlines(dec, se.Name)
	if err != nil {
		return nil, err
	}
	return richdoc.Heading{Level: level, Inlines: inlines}, nil
}

func (p *parser) parseParagraph(dec *xml.Decoder, se xml.StartElement) (richdoc.Block, error) {
	if _, ok := attrNS(se, nsODFGo, "thematic-break"); ok {
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return richdoc.ThematicBreak{}, nil
	}
	if v, ok := attrNS(se, nsODFGo, "math"); ok && v == "block" {
		tex, err := p.readFlatText(dec, se.Name, false)
		if err != nil {
			return nil, err
		}
		return richdoc.MathBlock{TeX: tex}, nil
	}
	if lang, ok := attrNS(se, nsODFGo, "code-language"); ok {
		text, err := p.readFlatText(dec, se.Name, true)
		if err != nil {
			return nil, err
		}
		return richdoc.CodeBlock{Language: lang, Text: text}, nil
	}
	inlines, err := p.parseInlines(dec, se.Name)
	if err != nil {
		return nil, err
	}
	return richdoc.Paragraph{Inlines: inlines}, nil
}

// readFlatText reads the character content of an element until its end, with
// text:line-break mapped to a newline when breaks is set (code blocks) and
// ignored otherwise (math).
func (p *parser) readFlatText(dec *xml.Decoder, end xml.Name, breaks bool) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch e := tok.(type) {
		case xml.CharData:
			b.Write(e)
		case xml.StartElement:
			if breaks && e.Name.Local == "line-break" {
				b.WriteByte('\n')
			}
			if err := dec.Skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			if e.Name == end {
				return b.String(), nil
			}
		}
	}
}

// parseSection maps a marked text:section to a BlockQuote; an unmarked section
// is transparent, so its children are flattened into the surrounding blocks.
func (p *parser) parseSection(dec *xml.Decoder, se xml.StartElement) ([]richdoc.Block, error) {
	blocks, err := p.parseBlocks(dec, se.Name)
	if err != nil {
		return nil, err
	}
	if _, ok := attrNS(se, nsODFGo, "blockquote"); ok {
		return []richdoc.Block{richdoc.BlockQuote{Blocks: blocks}}, nil
	}
	return blocks, nil
}

func (p *parser) parseList(dec *xml.Decoder, se xml.StartElement) (richdoc.Block, error) {
	info := p.lists[attrLocal(se, "style-name")]
	if info.start < 1 {
		info.start = 1
	}
	_, tight := attrNS(se, nsODFGo, "tight")
	var items []richdoc.ListItem
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if e.Name.Local == "list-item" || e.Name.Local == "list-header" {
				blocks, err := p.parseBlocks(dec, e.Name)
				if err != nil {
					return nil, err
				}
				items = append(items, richdoc.ListItem{Blocks: blocks})
			} else if err := dec.Skip(); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if e.Name == se.Name {
				return richdoc.List{Ordered: info.ordered, Start: info.start, Tight: tight, Items: items}, nil
			}
		}
	}
}

func (p *parser) parseTable(dec *xml.Decoder, se xml.StartElement) (richdoc.Block, error) {
	var header []richdoc.Cell
	var headerAlign []richdoc.Alignment
	var rows [][]richdoc.Cell
	var firstAlign []richdoc.Alignment
	haveHeader := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "table-header-rows":
				hrows, haligns, err := p.parseRows(dec, e.Name)
				if err != nil {
					return nil, err
				}
				for i, row := range hrows {
					if i == 0 {
						header, headerAlign, haveHeader = row, haligns[0], true
					} else {
						rows = append(rows, row)
					}
				}
			case "table-row":
				cells, aligns, err := p.parseRow(dec, e.Name)
				if err != nil {
					return nil, err
				}
				if firstAlign == nil {
					firstAlign = aligns
				}
				rows = append(rows, cells)
			default:
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if e.Name == se.Name {
				ref := firstAlign
				if haveHeader {
					ref = headerAlign
				}
				return richdoc.Table{Align: trimAlign(ref), Header: header, Rows: rows}, nil
			}
		}
	}
}

func (p *parser) parseRows(dec *xml.Decoder, end xml.Name) ([][]richdoc.Cell, [][]richdoc.Alignment, error) {
	var rows [][]richdoc.Cell
	var aligns [][]richdoc.Alignment
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if e.Name.Local == "table-row" {
				cells, al, err := p.parseRow(dec, e.Name)
				if err != nil {
					return nil, nil, err
				}
				rows = append(rows, cells)
				aligns = append(aligns, al)
			} else if err := dec.Skip(); err != nil {
				return nil, nil, err
			}
		case xml.EndElement:
			if e.Name == end {
				return rows, aligns, nil
			}
		}
	}
}

func (p *parser) parseRow(dec *xml.Decoder, end xml.Name) ([]richdoc.Cell, []richdoc.Alignment, error) {
	var cells []richdoc.Cell
	var aligns []richdoc.Alignment
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if e.Name.Local == "table-cell" {
				cell, a, err := p.parseCell(dec, e.Name)
				if err != nil {
					return nil, nil, err
				}
				cells = append(cells, cell)
				aligns = append(aligns, a)
			} else if err := dec.Skip(); err != nil {
				return nil, nil, err
			}
		case xml.EndElement:
			if e.Name == end {
				return cells, aligns, nil
			}
		}
	}
}

func (p *parser) parseCell(dec *xml.Decoder, end xml.Name) (richdoc.Cell, richdoc.Alignment, error) {
	var inlines []richdoc.Inline
	align := richdoc.AlignDefault
	first := true
	for {
		tok, err := dec.Token()
		if err != nil {
			return richdoc.Cell{}, align, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if e.Name.Local == "p" {
				if first {
					align = p.resolve(attrLocal(e, "style-name")).alignment()
					first = false
				}
				ins, err := p.parseInlines(dec, e.Name)
				if err != nil {
					return richdoc.Cell{}, align, err
				}
				if len(inlines) > 0 {
					inlines = append(inlines, richdoc.LineBreak{})
				}
				inlines = append(inlines, ins...)
			} else if err := dec.Skip(); err != nil {
				return richdoc.Cell{}, align, err
			}
		case xml.EndElement:
			if e.Name == end {
				return richdoc.Cell{Inlines: inlines}, align, nil
			}
		}
	}
}

// parseInlines reads inline children until the end element matching end.
// Adjacent character data (including entity-split runs) is merged into a single
// Text node.
func (p *parser) parseInlines(dec *xml.Decoder, end xml.Name) ([]richdoc.Inline, error) {
	var inlines []richdoc.Inline
	var pending strings.Builder
	flush := func() {
		if pending.Len() > 0 {
			inlines = append(inlines, richdoc.Text{Value: pending.String()})
			pending.Reset()
		}
	}
	for {
		off := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.CharData:
			pending.Write(e)
		case xml.EndElement:
			if e.Name == end {
				flush()
				return inlines, nil
			}
		case xml.StartElement:
			flush()
			ins, err := p.parseInlineElement(dec, e, off)
			if err != nil {
				return nil, err
			}
			inlines = append(inlines, ins...)
		}
	}
}

func (p *parser) parseInlineElement(dec *xml.Decoder, se xml.StartElement, off int64) ([]richdoc.Inline, error) {
	switch se.Name.Local {
	case "span":
		return p.parseSpan(dec, se)
	case "a":
		return p.parseLink(dec, se)
	case "line-break":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return []richdoc.Inline{richdoc.LineBreak{}}, nil
	case "tab":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return []richdoc.Inline{richdoc.Text{Value: "\t"}}, nil
	case "s":
		n := 1
		if c := attrLocal(se, "c"); c != "" {
			if v, err := strconv.Atoi(c); err == nil && v > 0 {
				n = v
			}
		}
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return []richdoc.Inline{richdoc.Text{Value: strings.Repeat(" ", n)}}, nil
	case "frame":
		img, err := p.parseImage(dec, se)
		if err != nil {
			return nil, err
		}
		return []richdoc.Inline{img}, nil
	default:
		raw, err := p.captureRaw(dec, off)
		if err != nil {
			return nil, err
		}
		return []richdoc.Inline{richdoc.RawInline{Format: "odf", Text: raw}}, nil
	}
}

func (p *parser) parseSpan(dec *xml.Decoder, se xml.StartElement) ([]richdoc.Inline, error) {
	if v, ok := attrNS(se, nsODFGo, "math"); ok && v == "inline" {
		tex, err := p.readFlatText(dec, se.Name, false)
		if err != nil {
			return nil, err
		}
		return []richdoc.Inline{richdoc.Math{TeX: tex}}, nil
	}
	props := p.resolve(attrLocal(se, "style-name"))
	if props.mono() {
		text, err := p.readFlatText(dec, se.Name, false)
		if err != nil {
			return nil, err
		}
		return []richdoc.Inline{richdoc.Code{Value: text}}, nil
	}
	inner, err := p.parseInlines(dec, se.Name)
	if err != nil {
		return nil, err
	}
	out := inner
	if props.strike() {
		out = []richdoc.Inline{richdoc.Strikethrough{Inlines: out}}
	}
	if props.italic() {
		out = []richdoc.Inline{richdoc.Emph{Inlines: out}}
	}
	if props.bold() {
		out = []richdoc.Inline{richdoc.Strong{Inlines: out}}
	}
	return out, nil
}

func (p *parser) parseLink(dec *xml.Decoder, se xml.StartElement) ([]richdoc.Inline, error) {
	href := attrLocal(se, "href")
	title := attrLocal(se, "title")
	inner, err := p.parseInlines(dec, se.Name)
	if err != nil {
		return nil, err
	}
	return []richdoc.Inline{richdoc.Link{URL: href, Title: title, Inlines: inner}}, nil
}

func (p *parser) parseImage(dec *xml.Decoder, se xml.StartElement) (richdoc.Image, error) {
	var href, alt, title string
	for {
		tok, err := dec.Token()
		if err != nil {
			return richdoc.Image{}, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "image":
				href = attrLocal(e, "href")
				if err := dec.Skip(); err != nil {
					return richdoc.Image{}, err
				}
			case "title":
				title, err = p.readFlatText(dec, e.Name, false)
				if err != nil {
					return richdoc.Image{}, err
				}
			case "desc":
				alt, err = p.readFlatText(dec, e.Name, false)
				if err != nil {
					return richdoc.Image{}, err
				}
			default:
				if err := dec.Skip(); err != nil {
					return richdoc.Image{}, err
				}
			}
		case xml.EndElement:
			if e.Name == se.Name {
				return richdoc.Image{URL: p.imageURL(href), Alt: alt, Title: title}, nil
			}
		}
	}
}

// imageURL turns a draw:image href into an Image URL: an embedded picture is
// read from the package and returned as a base64 data: URI; anything else (an
// external reference or an unresolved path) is returned unchanged.
func (p *parser) imageURL(href string) string {
	if href == "" {
		return ""
	}
	name := strings.TrimPrefix(href, "./")
	data, ok := p.parts[name]
	if !ok {
		return href
	}
	mt := p.manifest[name]
	if mt == "" {
		mt = mediaForExt(name)
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// captureRaw returns the exact source bytes of the element that starts at byte
// offset off, consuming the element from the decoder.
func (p *parser) captureRaw(dec *xml.Decoder, off int64) (string, error) {
	if err := dec.Skip(); err != nil {
		return "", err
	}
	return string(p.content[off:dec.InputOffset()]), nil
}

// resolve returns the effective properties of a style, following parent-style
// references.
func (p *parser) resolve(name string) styleProps {
	return p.resolveSeen(name, map[string]bool{})
}

func (p *parser) resolveSeen(name string, seen map[string]bool) styleProps {
	if name == "" || seen[name] {
		return styleProps{}
	}
	seen[name] = true
	base, ok := p.styles[name]
	if !ok {
		return styleProps{}
	}
	eff := styleProps{}
	if base.parent != "" {
		eff = p.resolveSeen(base.parent, seen)
	}
	setIf(&eff.fontWeight, base.fontWeight)
	setIf(&eff.fontStyle, base.fontStyle)
	setIf(&eff.lineThrough, base.lineThrough)
	setIf(&eff.fontName, base.fontName)
	setIf(&eff.textAlign, base.textAlign)
	return eff
}

func (s styleProps) bold() bool   { return s.fontWeight == "bold" }
func (s styleProps) italic() bool { return s.fontStyle == "italic" }
func (s styleProps) strike() bool { return s.lineThrough != "" && s.lineThrough != "none" }
func (s styleProps) mono() bool {
	f := strings.ToLower(s.fontName)
	return strings.Contains(f, "courier") || strings.Contains(f, "mono")
}

func (s styleProps) alignment() richdoc.Alignment {
	switch s.textAlign {
	case "left", "start":
		return richdoc.AlignLeft
	case "center":
		return richdoc.AlignCenter
	case "right", "end":
		return richdoc.AlignRight
	default:
		return richdoc.AlignDefault
	}
}

// parseMeta reads meta.xml into a metadata map, or nil when it holds nothing.
func parseMeta(data []byte) (map[string]string, error) {
	std := map[string]string{
		"title":       "title",
		"creator":     "author",
		"date":        "date",
		"subject":     "subject",
		"description": "description",
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	meta := map[string]string{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Space == nsDC {
			if key, ok := std[se.Name.Local]; ok {
				text, err := readElemText(dec, se.Name)
				if err != nil {
					return nil, err
				}
				meta[key] = text
			}
			continue
		}
		if se.Name.Local == "user-defined" {
			key := attrLocal(se, "name")
			text, err := readElemText(dec, se.Name)
			if err != nil {
				return nil, err
			}
			if key != "" {
				meta[key] = text
			}
		}
	}
	if len(meta) == 0 {
		return nil, nil
	}
	return meta, nil
}

func readElemText(dec *xml.Decoder, end xml.Name) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch e := tok.(type) {
		case xml.CharData:
			b.Write(e)
		case xml.EndElement:
			if e.Name == end {
				return b.String(), nil
			}
		}
	}
}

// attrLocal returns the value of the attribute with the given local name.
func attrLocal(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// attrNS returns the value of the attribute with the given namespace and local
// name, and whether it is present.
func attrNS(se xml.StartElement, ns, local string) (string, bool) {
	for _, a := range se.Attr {
		if a.Name.Space == ns && a.Name.Local == local {
			return a.Value, true
		}
	}
	return "", false
}

func setIf(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// trimAlign drops trailing default alignments, returning nil for an all-default
// slice so an unaligned table round-trips to a nil Align.
func trimAlign(a []richdoc.Alignment) []richdoc.Alignment {
	end := len(a)
	for end > 0 && a[end-1] == richdoc.AlignDefault {
		end--
	}
	if end == 0 {
		return nil
	}
	out := make([]richdoc.Alignment, end)
	copy(out, a[:end])
	return out
}

func mediaForExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(name, ".gif"):
		return "image/gif"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
