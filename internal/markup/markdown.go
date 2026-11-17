package markup

import (
	"bytes"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	// Raw HTML is kept so FromHTML can interpret it; nothing is ever handed to
	// a browser engine, and FromHTML only understands a safe whitelist.
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

// MarkdownToHTML renders Markdown (GFM) to HTML.
func MarkdownToHTML(src string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<p>" + escape(QtRich, src) + "</p>"
	}
	return buf.String()
}

// FromMarkdown converts Markdown (with embedded HTML, as used on Modrinth)
// into display blocks.
func FromMarkdown(src string, d Dialect) []Block {
	return FromHTML(MarkdownToHTML(src), d)
}

// ToPlain flattens blocks to plain text (images become their alt text).
func ToPlain(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		switch b.Kind {
		case BlockCode:
			parts = append(parts, b.Text)
		case BlockImages:
			for _, im := range b.Images {
				if im.Alt != "" {
					parts = append(parts, im.Alt)
				}
			}
		case BlockTable:
			for _, r := range b.Rows {
				parts = append(parts, strings.Join(r, " "))
			}
		case BlockRule:
		default:
			parts = append(parts, b.Markup)
		}
	}
	return strings.Join(parts, "\n")
}

// MarkdownToPlain strips Markdown/HTML formatting.
func MarkdownToPlain(src string) string { return ToPlain(FromMarkdown(src, Plain)) }

// HTMLToPlain strips HTML formatting.
func HTMLToPlain(src string) string { return ToPlain(FromHTML(src, Plain)) }

// InlineMarkdown renders a one-line summary that may contain Markdown
// (**bold**, `code`, links) into inline markup without block structure.
func InlineMarkdown(src string, d Dialect) string {
	var parts []string
	for _, b := range FromMarkdown(src, d) {
		switch b.Kind {
		case BlockParagraph, BlockQuote, BlockListItem:
			parts = append(parts, b.Markup)
		case BlockCode:
			parts = append(parts, escape(d, b.Text))
		case BlockHeading:
			// Headings carry size markup that doesn't belong in a one-liner.
			return escape(d, strings.Join(strings.Fields(MarkdownToPlain(src)), " "))
		}
	}
	return strings.Join(parts, " ")
}

// Terms splits a query into lowercase search terms (ignoring key:value filters).
func Terms(query string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '"'
	}) {
		if strings.Contains(f, ":") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Highlight escapes plain text for a dialect and emphasises every occurrence
// of the given terms (case-insensitive).
func Highlight(text string, terms []string, d Dialect) string {
	if len(terms) == 0 || d == Plain {
		return escape(d, text)
	}
	lower := strings.ToLower(text)
	// Byte-wise case folding is only safe when lengths match.
	if len(lower) != len(text) {
		return escape(d, text)
	}
	marks := make([]bool, len(text))
	for _, t := range terms {
		if t == "" {
			continue
		}
		for i := 0; ; {
			j := strings.Index(lower[i:], t)
			if j < 0 {
				break
			}
			for k := i + j; k < i+j+len(t); k++ {
				marks[k] = true
			}
			i += j + len(t)
		}
	}
	var b strings.Builder
	open, closeTag := "<b>", "</b>"
	if d == Pango {
		open = `<span weight="bold" background="#f6d32d" bgalpha="45%">`
		closeTag = "</span>"
	} else {
		open = `<span style="font-weight:600; background-color:#f6d32d; color:#000">`
		closeTag = "</span>"
	}
	in := false
	start := 0
	for i := 0; i <= len(text); i++ {
		m := i < len(text) && marks[i]
		if i == len(text) || m != in {
			seg := text[start:i]
			if in {
				b.WriteString(open + escape(d, seg) + closeTag)
			} else {
				b.WriteString(escape(d, seg))
			}
			start = i
			in = m
		}
	}
	return b.String()
}

// Snippet returns up to max runes of text around the first matched term.
func Snippet(text string, terms []string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	lower := strings.ToLower(text)
	pos := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	start := 0
	if pos > 0 {
		// convert byte offset to rune offset
		start = len([]rune(text[:pos])) - max/4
		if start < 0 {
			start = 0
		}
	}
	end := min(start+max, len(r))
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}
