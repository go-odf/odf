# odf

An **ODT (OpenDocument Text) ⇄ [richdoc](https://github.com/go-richdoc/richdoc)**
converter, written in pure Go (CGO-free, including `GOOS=js`). `.ods`
spreadsheets are **read** as well.

`odf` reads an `.odt` package (a ZIP of XML) into the neutral `richdoc`
document model, and writes a minimal, valid OpenDocument Text package from a
`richdoc.Document`. The two directions are designed as a faithful round-trip.

```go
d, err := odf.Parse(src)   // .odt bytes -> *richdoc.Document
out, err := odf.Write(d)   // *richdoc.Document -> .odt bytes (valid ODF package)
```

`src` and the return of `Write` are the raw bytes of the `.odt` ZIP container.
`Parse` also accepts an `.ods` spreadsheet; `Write` always produces ODT.

## Spreadsheets (`.ods`)

A spreadsheet is the **same package** as a text document — the same ZIP, the
same `content.xml`, the same `table:table`/`-row`/`-cell` elements — inside an
`office:spreadsheet` body instead of an `office:text` one. So `Parse` reads one
too: each sheet comes back as a level-1 `Heading` carrying its `table:name`,
followed by the sheet's `Table`.

⛔ The reader used to walk `office:text` and nothing else, and the cost of that
was **silent**: an `.ods` parsed *without an error* into a document of no
blocks, so a conversion handed somebody an empty page and no reason for it. An
empty answer and a refusal are different things, and this was neither — it was
an empty answer wearing a success. `TestASpreadsheetIsReadAtAll` is that
regression.

Three things a spreadsheet does that running text does not, and each is handled
**only** when the body is `office:spreadsheet`:

| | |
| --- | --- |
| **sheet names** | a workbook of six sheets is otherwise six tables run together with nothing saying where one ends. The name becomes a level-1 heading — a sheet is a top-level division, and demoting it would nest every sheet under whatever came before. |
| **repeat attributes** | `table:number-columns-repeated` and `-rows-repeated`. Running text rarely carries them; a spreadsheet is *made* of them. |
| **trailing padding** | every row is squared off to the sheet width with one repeated empty cell. It is dropped — but only from the **end**: an empty cell *between* two full ones is a gap in the data and the shape of the row depends on it. |

⛔ **A repeat is a count in a file, so materialising what it asks for is an
allocation the input decides.** Capped at 1024 columns and 65536 rows — the
formats' own limits rather than round numbers. OpenSSL's own `lifecycles.ods`
pads its rows to 1024 columns and asks for **1 048 559 repeated empty rows**;
read uncapped and untrimmed, its six sheets would be six tables of a million
rows by a thousand columns. Read as written, they are 91 rows and 14 columns.

The caps are tested against **literals**, not against the constants themselves:
the first draft asserted `n > maxRepeatRows`, so raising the constant raised the
assertion with it and the mutation passed. The judge must not move with the
subject.

## API

```go
func Parse(src []byte) (*richdoc.Document, error)
func Write(d *richdoc.Document) ([]byte, error)
```

`Parse` opens the ZIP, reads `content.xml` (resolving span formatting against
the automatic styles there and the named styles in `styles.xml`), consults
`META-INF/manifest.xml` for embedded image media types, and maps the
`office:text` body onto `richdoc` blocks and inlines. Anything the model has no
node for is preserved verbatim through `RawInline`/`RawBlock` with
`Format: "odf"`, so nothing in the source is silently lost.

`Write` produces a minimal, valid OpenDocument package: `mimetype` is the first
entry and is **stored uncompressed** (the ODF package requirement), followed by
`META-INF/manifest.xml`, `content.xml`, `styles.xml`, an optional `meta.xml`,
and any embedded `Pictures/`.

## Reference-library note

Before writing this converter, the maintained Go landscape was checked. No
maintained library offers a bidirectional ODT reader/writer over a neutral
model: `sbinet.org/x/odf` is read-only, `knieriem/odf` and `AlexJarrah/go-ods`
target spreadsheets (ODS) but expose cell grids rather than a document model, `kpmy/odf` is a one-way generator, and the `cat`
family only extracts plain text. An ODT is a ZIP of XML that the Go standard
library (`archive/zip` + `encoding/xml`) handles directly, so an in-org
converter that maps ODF onto `richdoc` is justified. The package depends only
on the standard library and `richdoc`.

## Construct mapping

Both directions map as follows:

| ODF (`content.xml`) | richdoc |
| --- | --- |
| `office:spreadsheet` → `table:table` + `table:name` | `Heading` (level 1) + `Table` |
| `table:number-columns-repeated` / `-rows-repeated` (sheets only) | that many columns / rows, capped |
| `text:h` + `text:outline-level` | `Heading` (level 1–6) |
| `text:p` | `Paragraph` |
| `text:span` → `fo:font-weight="bold"` | `Strong` |
| `text:span` → `fo:font-style="italic"` | `Emph` |
| `text:span` → `style:text-line-through-style` (≠ none) | `Strikethrough` |
| `text:span` → monospace `style:font-name` | `Code` (inline) |
| `text:a` (`xlink:href`, `office:title`) | `Link` |
| `draw:frame`/`draw:image` (+ `svg:desc`/`svg:title`) | `Image` |
| `text:line-break` | `LineBreak` |
| `text:tab`, `text:s` | whitespace `Text` |
| `text:list` / `text:list-item` (number style ⇒ ordered) | `List` / `ListItem` |
| `table:table` / `table:table-row` / `table:table-cell` | `Table` |
| `text:p` + `odfgo:code-language` | `CodeBlock` |
| `text:section` + `odfgo:blockquote` | `BlockQuote` |
| `text:p` + `odfgo:thematic-break` | `ThematicBreak` |
| `text:p`/`text:span` + `odfgo:math` | `MathBlock` / `Math` |
| `text:note` (footnote/endnote) | `Footnote` |
| `text:bookmark` / `text:bookmark-start` | `Anchor` |
| `text:reference-ref` / `text:bookmark-ref` (`text:ref-name`) | `CrossRef` |
| any unrecognized element | `RawBlock` / `RawInline` (`Format: "odf"`) |

The `odfgo:` attributes live in a private namespace
(`https://github.com/go-odf/odf`); ODF consumers ignore foreign attributes, and
they let the converter re-recognize model nodes (code blocks, block quotes,
thematic breaks, math) that OpenDocument has no dedicated element for on the way
back in.

**Footnotes.** A `text:note` becomes a `Footnote` whose body is the block
content of `text:note-body`; the generated `text:note-citation` marker and the
`text:id` are dropped and regenerated on write (a per-writer `ftnN` id and
sequence number). Both `note-class="footnote"` and `note-class="endnote"` map to
`Footnote`, so an **endnote normalizes to a footnote** on the round-trip.

**Bookmarks.** A `text:bookmark` (and the range-opening `text:bookmark-start`)
maps to a point `Anchor{ID}`; a stray `text:bookmark-end` is consumed and
produces no node. `Write` emits the point form `text:bookmark`. If an `Anchor`
carries `Inlines`, they are written adjacent to the bookmark so the visible text
is never lost, but the exact round-trip target is `Anchor{ID}` with empty
`Inlines` ⇄ `<text:bookmark>`.

**Cross-references.** `Parse` accepts both `text:reference-ref` and
`text:bookmark-ref` (reading `text:ref-name`); `Write` emits `text:bookmark-ref`,
the natural pair for the `text:bookmark` it writes for an `Anchor`. ODF has no
first-class citation element, so a `CrossRef` of kind `RefCite` is a best-effort
`text:bookmark-ref` tagged `odfgo:cite="true"`, which `Parse` restores to
`RefCite`; without that attribute a ref parses as `RefLabel`.

### Model boundaries (routed through Raw)

OpenDocument constructs the `richdoc` model still has no node for — annotations
(`office:annotation`), tables of contents / indexes (`text:table-of-content`,
`text:*-index`), reference marks (`text:reference-mark*`), and any other
unrecognized element — are carried through unchanged as `RawInline`/`RawBlock`
with `Format: "odf"`, captured as the exact original XML bytes so nothing is
lost.

Embedded images have no byte field in `richdoc.Image`, so an embedded picture is
surfaced as a `data:` URI in `Image.URL` (and re-embedded on write); external
`http(s)` references pass through as-is.

## License

BSD-3-Clause. Copyright (c) the go-odf authors.
