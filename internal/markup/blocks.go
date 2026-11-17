// Package markup turns Modrinth Markdown and CurseForge HTML descriptions into
// a small list of display blocks that both UI toolkits can render natively
// (Pango markup for GTK, Qt rich text for Qt). It also produces plain text
// for searching and match-highlighted snippets.
package markup

import (
	"html"
	"strings"
)

// Dialect is the target inline markup language.
type Dialect int

const (
	Pango  Dialect = iota // GTK / Pango markup
	QtRich                // Qt rich text (HTML subset)
	Plain                 // no markup at all (search indexing, tooltips)
)

// BlockKind enumerates block types.
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockCode
	BlockQuote
	BlockListItem
	BlockRule
	BlockImages
	BlockTable
)

// Image is an inline/standalone picture.
type Image struct {
	Src  string
	Alt  string
	Link string // where clicking the image should go, if anywhere
	// Width/Height hints from HTML attributes (0 = unknown).
	Width, Height int
}

// Block is one renderable unit.
type Block struct {
	Kind   BlockKind
	Markup string // inline markup in the requested dialect (paragraph, heading, quote, list item)
	Text   string // raw text (code blocks)
	Level  int    // heading level 1-6, list depth (0-based)
	Bullet string // list marker ("•", "1.", "☐", "☑")
	Images []Image
	Rows   [][]string // table cells (markup); first row is the header
}

// inline builds inline markup for one dialect.
type inline struct {
	d       Dialect
	b       strings.Builder
	stack   []tagPair // currently open elements
	hasText bool
}

type tagPair struct{ open, close string }

func (in *inline) text(s string) {
	if s == "" {
		return
	}
	cur := in.b.String()
	if strings.HasPrefix(s, " ") && (!in.hasText || strings.HasSuffix(cur, " ") || strings.HasSuffix(cur, "\n")) {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			return
		}
	}
	if strings.TrimSpace(s) != "" {
		in.hasText = true
	}
	in.b.WriteString(escape(in.d, s))
}

// cleanText drops characters that are illegal in XML (Pango rejects the
// whole string otherwise) and repairs invalid UTF-8.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0xFFFE && r <= 0xFFFF) || (r >= 0xD800 && r <= 0xDFFF) {
			return -1
		}
		return r
	}, s)
}

func escape(d Dialect, s string) string {
	s = cleanText(s)
	if d == Plain {
		return s
	}
	if d == Pango {
		// Pango accepts the XML entities only.
		r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
		return r.Replace(s)
	}
	return html.EscapeString(s)
}

// Escape is exported for the UIs.
func Escape(d Dialect, s string) string { return escape(d, s) }

func (in *inline) open(tag, close string) {
	if in.d == Plain {
		tag, close = "", ""
	}
	in.b.WriteString(tag)
	in.stack = append(in.stack, tagPair{tag, close})
}

func (in *inline) closeTop() {
	if n := len(in.stack); n > 0 {
		in.b.WriteString(in.stack[n-1].close)
		in.stack = in.stack[:n-1]
	}
}

func (in *inline) closeAll() {
	for len(in.stack) > 0 {
		in.closeTop()
	}
}

// continuation returns a fresh builder with the same elements re-opened, so
// formatting (e.g. a link) continues across a forced block split.
func (in *inline) continuation() *inline {
	n := &inline{d: in.d}
	for _, t := range in.stack {
		n.open(t.open, t.close)
	}
	return n
}

func (in *inline) bold()   { in.open("<b>", "</b>") }
func (in *inline) italic() { in.open("<i>", "</i>") }
func (in *inline) strike() { in.open("<s>", "</s>") }
func (in *inline) under()  { in.open("<u>", "</u>") }

func (in *inline) code() {
	if in.d == Pango {
		in.open(`<span font_family="monospace" background="#888888" bgalpha="22%">`, "</span>")
	} else {
		in.open(`<code style="background-color:rgba(136,136,136,0.22)">`, "</code>")
	}
}

func (in *inline) link(href string) {
	href = strings.TrimSpace(href)
	if href == "" || !safeURL(href) {
		in.open("", "")
		return
	}
	in.open(`<a href="`+escape(in.d, href)+`">`, "</a>")
}

func (in *inline) lineBreak() { in.b.WriteString("\n") }

// String returns the markup with all open elements closed, trimmed.
func (in *inline) String() string {
	in.closeAll()
	return strings.TrimSpace(collapseNewlines(in.b.String()))
}

func (in *inline) empty() bool { return !in.hasText }

func collapseNewlines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func safeURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:") || strings.HasPrefix(l, "#")
}

func headingMarkup(d Dialect, level int, inner string) string {
	if d == Plain {
		return inner
	}
	if d == Pango {
		size := map[int]string{1: "xx-large", 2: "x-large", 3: "large", 4: "medium", 5: "medium", 6: "small"}[level]
		if size == "" {
			size = "medium"
		}
		return `<span size="` + size + `" weight="bold">` + inner + `</span>`
	}
	size := map[int]string{1: "xx-large", 2: "x-large", 3: "large", 4: "medium", 5: "medium", 6: "small"}[level]
	if size == "" {
		size = "medium"
	}
	return `<span style="font-size:` + size + `; font-weight:600">` + inner + `</span>`
}

// blockList accumulates blocks and merges adjacent image blocks.
type blockList struct {
	out []Block
}

func (bl *blockList) add(b Block) {
	if b.Kind == BlockImages && len(b.Images) == 0 {
		return
	}
	if (b.Kind == BlockParagraph || b.Kind == BlockQuote || b.Kind == BlockHeading || b.Kind == BlockListItem) && strings.TrimSpace(b.Markup) == "" {
		return
	}
	if b.Kind == BlockCode && strings.TrimSpace(b.Text) == "" {
		return
	}
	bl.out = append(bl.out, b)
}
