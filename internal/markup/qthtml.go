package markup

import (
	"fmt"
	"strings"
)

// BlocksToHTML renders QtRich blocks as the HTML subset Qt's rich-text engine
// (QTextBrowser / QLabel) supports. Images keep their URLs as src; the UI
// registers the downloaded pixels under the same URL.
func BlocksToHTML(blocks []Block) string {
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Kind {
		case BlockParagraph:
			b.WriteString("<p>" + nl2br(bl.Markup) + "</p>\n")
		case BlockHeading:
			l := bl.Level
			if l < 1 || l > 6 {
				l = 3
			}
			// Qt sizes h1-h6 itself; strip our inline size span.
			b.WriteString(fmt.Sprintf("<h%d>%s</h%d>\n", l, stripHeadingSpan(bl.Markup), l))
		case BlockQuote:
			b.WriteString(`<blockquote style="color:palette(placeholder-text)"><p>` + nl2br(bl.Markup) + "</p></blockquote>\n")
		case BlockListItem:
			indent := 12 + 20*bl.Level
			b.WriteString(fmt.Sprintf(`<p style="margin-left:%dpx; margin-top:2px; margin-bottom:2px">%s&nbsp;&nbsp;%s</p>`+"\n", indent, escape(QtRich, bl.Bullet), nl2br(bl.Markup)))
		case BlockCode:
			b.WriteString(`<pre style="background-color:rgba(136,136,136,0.18); padding:8px">` + escape(QtRich, bl.Text) + "</pre>\n")
		case BlockRule:
			b.WriteString("<hr/>\n")
		case BlockTable:
			b.WriteString(`<table cellspacing="0" cellpadding="4" border="1" style="border-color:rgba(136,136,136,0.4); border-collapse:collapse">` + "\n")
			for _, r := range bl.Rows {
				b.WriteString("<tr>")
				for _, c := range r {
					b.WriteString("<td>" + c + "</td>")
				}
				b.WriteString("</tr>\n")
			}
			b.WriteString("</table>\n")
		case BlockImages:
			align := "left"
			if len(bl.Images) == 1 {
				align = "center"
			}
			b.WriteString(`<p align="` + align + `">`)
			for _, im := range bl.Images {
				attrs := `src="` + escape(QtRich, im.Src) + `" alt="` + escape(QtRich, im.Alt) + `"`
				if w := im.Width; w > 0 {
					attrs += fmt.Sprintf(` width="%d"`, min(w, 560))
				}
				img := "<img " + attrs + "/>"
				if im.Link != "" {
					img = `<a href="` + escape(QtRich, im.Link) + `">` + img + "</a>"
				}
				b.WriteString(img + " ")
			}
			b.WriteString("</p>\n")
		}
	}
	return b.String()
}

func nl2br(s string) string { return strings.ReplaceAll(s, "\n", "<br/>") }

func stripHeadingSpan(m string) string {
	if strings.HasPrefix(m, "<span style=") {
		if i := strings.Index(m, ">"); i >= 0 && strings.HasSuffix(m, "</span>") {
			return m[i+1 : len(m)-len("</span>")]
		}
	}
	return m
}

// ImageURLs lists every image source in the blocks (for prefetching).
func ImageURLs(blocks []Block) []string {
	var out []string
	seen := map[string]bool{}
	for _, b := range blocks {
		for _, im := range b.Images {
			if !seen[im.Src] {
				seen[im.Src] = true
				out = append(out, im.Src)
			}
		}
	}
	return out
}
