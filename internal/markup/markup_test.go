package markup

import (
	"encoding/xml"
	"strings"
	"testing"
)

// pangoValid checks that markup is well-formed XML, which is what Pango
// requires; malformed markup makes GTK show nothing.
func pangoValid(t *testing.T, s string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<markup>" + s + "</markup>"))
	d.Strict = true
	d.Entity = map[string]string{}
	for {
		_, err := d.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("invalid pango markup %q: %v", s, err)
		}
	}
}

const modrinthBody = `# Sodium

<center><a href="https://x.io"><img src="https://cdn/logo.png" alt="logo" width="200"></a></center>

[![Discord](https://img.shields.io/discord.svg)](https://discord.gg/x) [![CF](https://img.shields.io/cf.svg)](https://cf)

Sodium is a **free** and _open-source_ ` + "`renderer`" + ` <b>bold <i>nested</b> unclosed</i> & <3 "quotes"
with a [link](https://example.com?a=1&b=2) and a [bad link](javascript:alert(1)).

## Features
- Faster chunk rendering
- Fixes **many** bugs
  1. nested item
- [x] done
- [ ] todo

` + "```java\nint x = 1 < 2 && y;\n```" + `

| Feature | Status |
|---|---|
| *Chunks* | ✓ |

> A quote with **bold**

---
<details><summary>Spoiler</summary>

Hidden **text**
</details>

<script>alert("x")</script>
<img src="javascript:bad">
`

func TestMarkdownBlocksAreValidPango(t *testing.T) {
	blocks := FromMarkdown(modrinthBody, Pango)
	kinds := map[BlockKind]int{}
	for _, b := range blocks {
		kinds[b.Kind]++
		if b.Markup != "" {
			pangoValid(t, b.Markup)
		}
		for _, r := range b.Rows {
			for _, c := range r {
				pangoValid(t, c)
			}
		}
		if strings.Contains(b.Markup, "javascript:") {
			t.Fatalf("unsafe link kept: %q", b.Markup)
		}
		if strings.Contains(b.Markup, "alert") {
			t.Fatalf("script content leaked: %q", b.Markup)
		}
		for _, im := range b.Images {
			if strings.HasPrefix(im.Src, "javascript") {
				t.Fatal("unsafe image kept")
			}
		}
	}
	for _, k := range []BlockKind{BlockHeading, BlockParagraph, BlockListItem, BlockCode, BlockTable, BlockQuote, BlockRule, BlockImages} {
		if kinds[k] == 0 {
			t.Errorf("no block of kind %d in %+v", k, blocks)
		}
	}
	// The banner is its own row; the two badges of one paragraph share a row
	// and keep their links.
	var rows [][]Image
	for _, b := range blocks {
		if b.Kind == BlockImages {
			rows = append(rows, b.Images)
		}
	}
	if len(rows) != 2 || len(rows[0]) != 1 || rows[0][0].Width != 200 || len(rows[1]) != 2 || rows[1][0].Link != "https://discord.gg/x" {
		t.Fatalf("image rows %+v", rows)
	}
	// Code block keeps raw text, unescaped.
	for _, b := range blocks {
		if b.Kind == BlockCode && !strings.Contains(b.Text, "1 < 2 && y") {
			t.Fatalf("code %q", b.Text)
		}
	}
}

func TestQtRichValid(t *testing.T) {
	for _, b := range FromMarkdown(modrinthBody, QtRich) {
		if b.Markup != "" {
			pangoValid(t, b.Markup) // Qt output is XHTML-compatible too
		}
	}
}

func TestCurseForgeHTML(t *testing.T) {
	src := `<p style="text-align:center"><img src="https://media.forgecdn.net/a.png" width="600"></p>
<h2>About</h2><p>JEI is an <strong>item</strong> and <em>recipe</em> viewing mod&nbsp;for Minecraft.<br>Second line</p>
<ul><li>One</li><li>Two<ul><li>Deep</li></ul></li></ul><ol start="3"><li>Three</li></ol>
<pre>code &lt;here&gt;</pre><table><tr><th>A</th><td>B</td></tr></table>`
	blocks := FromHTML(src, Pango)
	for _, b := range blocks {
		if b.Markup != "" {
			pangoValid(t, b.Markup)
		}
	}
	var got []string
	for _, b := range FromHTML(src, Plain) {
		got = append(got, b.Bullet+"|"+ToPlain([]Block{b}))
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"|About", "JEI is an item and recipe viewing mod for Minecraft.\nSecond line", "•|One", "•|Two", "•|Deep", "3.|Three", "|code <here>", "|A B"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	var deep *Block
	for i := range blocks {
		if strings.Contains(blocks[i].Markup, "Deep") {
			deep = &blocks[i]
		}
	}
	if deep == nil || deep.Level != 1 {
		t.Fatalf("nested level wrong: %+v", deep)
	}
}

func TestPlainAndHighlight(t *testing.T) {
	plain := MarkdownToPlain("**Fast** _rendering_ with `code` and [links](https://x)")
	if plain != "Fast rendering with code and links" {
		t.Fatalf("plain %q", plain)
	}
	h := Highlight("Sodium & Iris <shaders>", Terms("iris SOD"), Pango)
	pangoValid(t, h)
	if !strings.Contains(h, ">Sod</span>") || !strings.Contains(h, ">Iris</span>") || !strings.Contains(h, "&lt;shaders&gt;") {
		t.Fatalf("highlight %q", h)
	}
	if Highlight("x", nil, Pango) != "x" {
		t.Fatal("no-term highlight")
	}
	if got := Terms(`sodium side:client "iris"`); len(got) != 2 || got[0] != "sodium" || got[1] != "iris" {
		t.Fatalf("terms %v", got)
	}
	in := InlineMarkdown("A **fast** mod & `x`", Pango)
	pangoValid(t, in)
	if !strings.Contains(in, "<b>fast</b>") || strings.Contains(in, "**") {
		t.Fatalf("inline %q", in)
	}
	if InlineMarkdown("# Title", Pango) != "Title" {
		t.Fatalf("heading inline %q", InlineMarkdown("# Title", Pango))
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("filler ", 50) + "the magic word appears here " + strings.Repeat("tail ", 50)
	s := Snippet(long, []string{"magic"}, 60)
	if !strings.Contains(s, "magic") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("snippet %q", s)
	}
	if Snippet("short", nil, 60) != "short" {
		t.Fatal("short snippet")
	}
}

func FuzzMarkdownPango(f *testing.F) {
	f.Add(modrinthBody)
	f.Add("<b><i>x</b></i><a href='https://x'>y")
	f.Add("[![a](https://i)](https://l)**b**")
	f.Fuzz(func(t *testing.T, s string) {
		for _, b := range FromMarkdown(s, Pango) {
			if b.Markup == "" {
				continue
			}
			d := xml.NewDecoder(strings.NewReader("<m>" + b.Markup + "</m>"))
			d.Strict = true
			for {
				_, err := d.Token()
				if err != nil {
					if err.Error() != "EOF" {
						t.Fatalf("invalid markup for %q: %q: %v", s, b.Markup, err)
					}
					break
				}
			}
		}
	})
}

func TestBlocksToHTML(t *testing.T) {
	blocks := FromMarkdown(modrinthBody, QtRich)
	h := BlocksToHTML(blocks)
	for _, want := range []string{"<h1>Sodium</h1>", `<a href="https://discord.gg/x"><img src="https://img.shields.io/discord.svg"`, "<b>free</b>", "1 &lt; 2 &amp;&amp; y", "<table", "<hr/>", "☑"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in\n%s", want, h)
		}
	}
	if strings.Contains(h, "javascript:") || strings.Contains(h, "alert") {
		t.Fatal("unsafe content in HTML")
	}
	// Well-formed enough for a strict XML parser (Qt is lenient, but this
	// catches unbalanced tags).
	pangoValid(t, strings.ReplaceAll(h, "&nbsp;", "&#160;"))
	if got := ImageURLs(blocks); len(got) != 3 {
		t.Fatalf("image urls %v", got)
	}
}
