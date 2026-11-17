package markup

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// FromHTML converts an HTML fragment (e.g. a CurseForge description, or raw
// HTML embedded in Markdown) into blocks.
func FromHTML(src string, d Dialect) []Block {
	c := &htmlConv{d: d}
	c.reset()
	c.run(src)
	c.flush(BlockParagraph)
	return c.bl.out
}

type htmlConv struct {
	d       Dialect
	bl      blockList
	in      *inline
	images  []Image
	linkStk []string // hrefs of open <a> elements ("" for unsafe)
	listStk []listState
	quote   int
	heading int
	pre     *strings.Builder
	skip    int // inside <script>/<style>/<iframe>
	// table state
	table   [][]string
	row     []string
	cell    *inline
	inTable int
}

type listState struct {
	ordered bool
	n       int
}

func (c *htmlConv) reset() { c.in = &inline{d: c.d} }

func (c *htmlConv) cur() *inline {
	if c.cell != nil {
		return c.cell
	}
	return c.in
}

// flush ends the current text block.
func (c *htmlConv) flush(kind BlockKind) {
	if len(c.images) > 0 && c.in.empty() {
		c.bl.add(Block{Kind: BlockImages, Images: c.images})
		c.images = nil
	}
	if !c.in.empty() {
		next := c.in.continuation()
		b := Block{Kind: kind, Markup: c.in.String()}
		switch {
		case c.heading > 0:
			b.Kind, b.Level = BlockHeading, c.heading
			b.Markup = headingMarkup(c.d, c.heading, b.Markup)
		case len(c.listStk) > 0:
			ls := &c.listStk[len(c.listStk)-1]
			b.Kind, b.Level = BlockListItem, len(c.listStk)-1
			if ls.ordered {
				b.Bullet = strconv.Itoa(ls.n) + "."
			} else {
				b.Bullet = "•"
			}
		case c.quote > 0:
			b.Kind = BlockQuote
		}
		c.bl.add(b)
		c.in = next
	} else {
		// Drop empty open/close pairs (e.g. a link that only wrapped an image).
		c.in = c.in.continuation()
	}
	if len(c.images) > 0 {
		c.bl.add(Block{Kind: BlockImages, Images: c.images})
		c.images = nil
	}
}

func attr(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func attrOK(t html.Token, key string) (string, bool) {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func (c *htmlConv) currentLink() string {
	for i := len(c.linkStk) - 1; i >= 0; i-- {
		if c.linkStk[i] != "" {
			return c.linkStk[i]
		}
	}
	return ""
}

func (c *htmlConv) run(src string) {
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		t := z.Token()
		switch tt {
		case html.TextToken:
			if c.skip > 0 {
				continue
			}
			if c.pre != nil {
				c.pre.WriteString(t.Data)
				continue
			}
			txt := collapseSpace(t.Data)
			if txt == "" {
				continue
			}
			if txt == " " && c.cur().empty() {
				continue
			}
			c.cur().text(txt)
		case html.StartTagToken, html.SelfClosingTagToken:
			c.start(t, tt == html.SelfClosingTagToken)
		case html.EndTagToken:
			c.end(t)
		}
	}
}

func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func (c *htmlConv) start(t html.Token, selfClosing bool) {
	if c.skip > 0 {
		if !selfClosing && (t.DataAtom == atom.Script || t.DataAtom == atom.Style || t.DataAtom == atom.Iframe) {
			c.skip++
		}
		return
	}
	in := c.cur()
	switch t.DataAtom {
	case atom.Script, atom.Style, atom.Iframe:
		if !selfClosing {
			c.skip++
		}
	case atom.B, atom.Strong:
		in.bold()
	case atom.I, atom.Em:
		in.italic()
	case atom.S, atom.Del, atom.Strike:
		in.strike()
	case atom.U, atom.Ins:
		in.under()
	case atom.Code, atom.Kbd, atom.Samp:
		if c.pre == nil {
			in.code()
		}
	case atom.A:
		href := attr(t, "href")
		if !safeURL(href) {
			href = ""
		}
		c.linkStk = append(c.linkStk, href)
		in.link(href)
	case atom.Br:
		in.lineBreak()
	case atom.Img:
		src := attr(t, "src")
		if src == "" || !safeURL(src) {
			return
		}
		w, _ := strconv.Atoi(strings.TrimSuffix(attr(t, "width"), "px"))
		h, _ := strconv.Atoi(strings.TrimSuffix(attr(t, "height"), "px"))
		img := Image{Src: src, Alt: attr(t, "alt"), Link: c.currentLink(), Width: w, Height: h}
		if c.cell != nil {
			c.cell.text(img.Alt)
			return
		}
		// Images split text: flush pending text first so order is preserved,
		// but keep consecutive images (badge rows) together.
		if !c.in.empty() {
			c.flush(BlockParagraph)
		} else {
			c.in = c.in.continuation() // drop empty wrappers like <a></a>
		}
		c.images = append(c.images, img)
	case atom.P, atom.Div, atom.Center, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Details, atom.Summary, atom.Figure, atom.Figcaption:
		if c.cell == nil {
			c.flush(BlockParagraph)
		}
		if t.DataAtom == atom.Summary {
			c.in.bold()
		}
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		c.flush(BlockParagraph)
		c.heading = int(t.Data[1] - '0')
	case atom.Ul, atom.Ol:
		c.flush(BlockParagraph)
		ls := listState{ordered: t.DataAtom == atom.Ol}
		if n, err := strconv.Atoi(attr(t, "start")); err == nil && n > 0 {
			ls.n = n - 1
		}
		c.listStk = append(c.listStk, ls)
	case atom.Input:
		if strings.EqualFold(attr(t, "type"), "checkbox") {
			if _, checked := attrOK(t, "checked"); checked {
				in.text("☑ ")
			} else {
				in.text("☐ ")
			}
		}
	case atom.Li:
		c.flush(BlockParagraph)
		if n := len(c.listStk); n > 0 {
			c.listStk[n-1].n++
		}
	case atom.Blockquote:
		c.flush(BlockParagraph)
		c.quote++
	case atom.Pre:
		c.flush(BlockParagraph)
		c.pre = &strings.Builder{}
	case atom.Hr:
		c.flush(BlockParagraph)
		c.bl.add(Block{Kind: BlockRule})
	case atom.Table:
		c.flush(BlockParagraph)
		c.inTable++
		c.table = nil
	case atom.Tr:
		c.row = nil
	case atom.Td, atom.Th:
		c.cell = &inline{d: c.d}
		if t.DataAtom == atom.Th {
			c.cell.bold()
		}
	}
}

func (c *htmlConv) end(t html.Token) {
	if c.skip > 0 {
		if t.DataAtom == atom.Script || t.DataAtom == atom.Style || t.DataAtom == atom.Iframe {
			c.skip--
		}
		return
	}
	in := c.cur()
	switch t.DataAtom {
	case atom.B, atom.Strong, atom.I, atom.Em, atom.S, atom.Del, atom.Strike, atom.U, atom.Ins:
		in.closeTop()
	case atom.Code, atom.Kbd, atom.Samp:
		if c.pre == nil {
			in.closeTop()
		}
	case atom.A:
		if n := len(c.linkStk); n > 0 {
			c.linkStk = c.linkStk[:n-1]
		}
		in.closeTop()
	case atom.P, atom.Div, atom.Center, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Details, atom.Figure, atom.Figcaption:
		if c.cell == nil {
			c.flush(BlockParagraph)
		}
	case atom.Summary:
		c.in.closeTop()
		c.flush(BlockParagraph)
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		c.flush(BlockParagraph)
		c.heading = 0
	case atom.Li:
		c.flush(BlockParagraph)
	case atom.Ul, atom.Ol:
		c.flush(BlockParagraph)
		if n := len(c.listStk); n > 0 {
			c.listStk = c.listStk[:n-1]
		}
	case atom.Blockquote:
		c.flush(BlockParagraph)
		if c.quote > 0 {
			c.quote--
		}
	case atom.Pre:
		if c.pre != nil {
			c.bl.add(Block{Kind: BlockCode, Text: strings.Trim(c.pre.String(), "\n")})
			c.pre = nil
		}
	case atom.Td, atom.Th:
		if c.cell != nil {
			c.row = append(c.row, c.cell.String())
			c.cell = nil
		}
	case atom.Tr:
		if len(c.row) > 0 {
			c.table = append(c.table, c.row)
		}
		c.row = nil
	case atom.Table:
		if c.inTable > 0 {
			c.inTable--
		}
		if len(c.table) > 0 {
			c.bl.add(Block{Kind: BlockTable, Rows: c.table})
		}
		c.table = nil
	}
}
