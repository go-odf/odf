// Copyright (c) the go-odf authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package odf converts between ODT (OpenDocument Text) packages and the neutral
// [github.com/go-richdoc/richdoc] document model.
//
// An ODT file is a ZIP container of XML parts. [Parse] opens the container,
// reads content.xml (resolving inline formatting against the automatic styles
// there and the named styles in styles.xml), consults META-INF/manifest.xml for
// embedded image media types, and maps the office:text body onto richdoc blocks
// and inlines. [Write] produces a minimal, valid OpenDocument Text package: the
// mimetype entry is written first and stored uncompressed, as the OpenDocument
// packaging specification requires.
//
// The two directions are designed as a faithful round-trip:
// Parse(Write(d)) is semantically equal to d for the supported model.
//
// OpenDocument has no dedicated element for several richdoc nodes (code blocks,
// block quotes, thematic breaks, inline/display math); those are written using
// standard ODF container elements tagged with attributes in a private
// namespace ("https://github.com/go-odf/odf"), which ODF consumers ignore and
// which let Parse re-recognize the node. Constructs the model has no node for at
// all (footnotes, bookmarks, cross-references, and any other unrecognized
// element) are preserved verbatim through [richdoc.RawInline] / [richdoc.RawBlock]
// with Format "odf", so nothing in the source is lost.
//
// The package is pure Go and builds with CGO disabled, including for
// GOOS=js/GOARCH=wasm.
package odf

// mimetype is the required media type of an OpenDocument Text package. It is
// stored, uncompressed, as the first entry of the ZIP container.
const mimetypeODT = "application/vnd.oasis.opendocument.text"

// OpenDocument XML namespace URIs.
const (
	nsOffice   = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
	nsText     = "urn:oasis:names:tc:opendocument:xmlns:text:1.0"
	nsStyle    = "urn:oasis:names:tc:opendocument:xmlns:style:1.0"
	nsFO       = "urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0"
	nsTable    = "urn:oasis:names:tc:opendocument:xmlns:table:1.0"
	nsDraw     = "urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
	nsSVG      = "urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0"
	nsManifest = "urn:oasis:names:tc:opendocument:xmlns:manifest:1.0"
	nsMeta     = "urn:oasis:names:tc:opendocument:xmlns:meta:1.0"
	nsXLink    = "http://www.w3.org/1999/xlink"
	nsDC       = "http://purl.org/dc/elements/1.1/"
	nsODFGo    = "https://github.com/go-odf/odf"
)

// Names of the ODF parts inside the ZIP container.
const (
	partMimetype = "mimetype"
	partContent  = "content.xml"
	partStyles   = "styles.xml"
	partMeta     = "meta.xml"
	partManifest = "META-INF/manifest.xml"
	dirPictures  = "Pictures/"
)

// Fixed automatic-style names emitted by Write and recognized by Parse.
const (
	styleBold   = "T_b"
	styleItalic = "T_i"
	styleStrike = "T_s"
	styleMono   = "T_c"
	styleAlignL = "P_l"
	styleAlignC = "P_c"
	styleAlignR = "P_r"
	styleHR     = "P_hr"
	styleCode   = "P_code"
	// monoFont is emitted for [richdoc.Code] and recognized (case-insensitively,
	// together with any "courier"/"mono" family) as monospace on the way back.
	monoFont = "Courier New"
)
