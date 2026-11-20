package gtkui

import (
	"context"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	glib "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"modpackmanager/internal/imgutil"
	"modpackmanager/internal/markup"
)

var images = imgutil.NewLoader()

// openURI opens a link in the user's browser.
func (a *App) openURI(uri string) {
	if uri == "" {
		return
	}
	gtk.NewURILauncher(uri).Launch(context.Background(), &a.win.Window, nil)
}

// richLabel is a wrapping, selectable label showing Pango markup whose
// links open in the browser.
func (a *App) richLabel(m string) *gtk.Label {
	l := gtk.NewLabel("")
	l.SetMarkup(m)
	l.SetWrap(true)
	l.SetWrapMode(pango.WrapWordChar)
	l.SetXAlign(0)
	l.SetSelectable(true)
	l.SetHAlign(gtk.AlignFill)
	l.ConnectActivateLink(func(uri string) bool {
		a.openURI(uri)
		return true
	})
	return l
}

// renderBlocks turns description blocks into a vertical box of widgets.
func (a *App) renderBlocks(ctx context.Context, blocks []markup.Block) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 10)
	box.AddCSSClass("mpm-rich")
	for _, b := range blocks {
		switch b.Kind {
		case markup.BlockParagraph:
			box.Append(a.richLabel(b.Markup))
		case markup.BlockHeading:
			l := a.richLabel(b.Markup)
			if b.Level <= 2 {
				l.SetMarginTop(8)
			}
			box.Append(l)
		case markup.BlockQuote:
			l := a.richLabel(b.Markup)
			l.AddCSSClass("mpm-quote")
			box.Append(l)
		case markup.BlockListItem:
			row := gtk.NewBox(gtk.OrientationHorizontal, 8)
			row.SetMarginStart(8 + 18*b.Level)
			bullet := gtk.NewLabel(b.Bullet)
			bullet.SetVAlign(gtk.AlignStart)
			bullet.AddCSSClass("dim-label")
			row.Append(bullet)
			l := a.richLabel(b.Markup)
			l.SetHExpand(true)
			row.Append(l)
			box.Append(row)
		case markup.BlockCode:
			l := gtk.NewLabel(b.Text)
			l.SetXAlign(0)
			l.SetSelectable(true)
			l.AddCSSClass("monospace")
			l.SetWrap(true)
			l.SetWrapMode(pango.WrapChar)
			frame := gtk.NewBox(gtk.OrientationVertical, 0)
			frame.AddCSSClass("card")
			frame.AddCSSClass("mpm-code")
			frame.Append(l)
			box.Append(frame)
		case markup.BlockRule:
			box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
		case markup.BlockTable:
			grid := gtk.NewGrid()
			grid.SetColumnSpacing(18)
			grid.SetRowSpacing(6)
			for r, row := range b.Rows {
				for c, cell := range row {
					l := a.richLabel(cell)
					grid.Attach(l, c, r, 1, 1)
				}
			}
			box.Append(grid)
		case markup.BlockImages:
			box.Append(a.imageRow(ctx, b.Images))
		}
	}
	return box
}

func (a *App) imageRow(ctx context.Context, imgs []markup.Image) gtk.Widgetter {
	// A plain row: badges sit side by side and shrink if space runs out.
	flow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	flow.SetHAlign(gtk.AlignStart)
	if len(imgs) == 1 {
		flow.SetHAlign(gtk.AlignCenter)
	}
	big := len(imgs) == 1
	for _, im := range imgs {
		im := im
		pic := gtk.NewPicture()
		pic.SetCanShrink(true)
		pic.SetContentFit(gtk.ContentFitContain)
		pic.SetAlternativeText(im.Alt)
		if im.Alt != "" {
			pic.SetTooltipText(im.Alt)
		}
		maxSide := 720
		if !big {
			maxSide = 360
		}
		var w gtk.Widgetter = pic
		if im.Link != "" {
			btn := gtk.NewButton()
			btn.AddCSSClass("flat")
			btn.SetChild(pic)
			btn.SetTooltipText(im.Link)
			btn.ConnectClicked(func() { a.openURI(im.Link) })
			w = btn
		}
		flow.Append(w)
		a.loadPicture(ctx, im.Src, maxSide, func(tex *gdk.Texture) {
			pic.SetPaintable(tex)
			// Keep badges at their natural (small) size, big images bounded.
			h := tex.Height()
			if im.Height > 0 {
				h = im.Height
			}
			if h > 400 {
				h = 400
			}
			pic.SetSizeRequest(-1, h)
		}, func() {
			// Couldn't load (e.g. unsupported format): fall back to alt text.
			if im.Alt != "" {
				flow.Remove(w)
				l := gtk.NewLabel(im.Alt)
				l.AddCSSClass("dim-label")
				flow.Append(l)
			} else {
				flow.Remove(w)
			}
		})
	}
	return flow
}

// loadPicture fetches an image in the background and hands the texture to
// done on the main thread.
func (a *App) loadPicture(ctx context.Context, url string, maxSide int, done func(*gdk.Texture), failed func()) {
	if url == "" {
		if failed != nil {
			failed()
		}
		return
	}
	go func() {
		data, _, err := images.Load(ctx, url, maxSide)
		ui(func() {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if failed != nil {
					failed()
				}
				return
			}
			tex, err := gdk.NewTextureFromBytes(glib.NewBytesWithGo(data))
			if err != nil || tex == nil {
				if failed != nil {
					failed()
				}
				return
			}
			done(tex)
		})
	}()
}

// iconWidget returns a fixed-size rounded icon that fills in asynchronously.
func (a *App) iconWidget(ctx context.Context, url string, size int, fallback string) *gtk.Image {
	img := gtk.NewImageFromIconName(fallback)
	img.SetPixelSize(size)
	img.AddCSSClass("mpm-icon")
	if url != "" {
		a.loadPicture(ctx, url, size*2, func(t *gdk.Texture) { img.SetFromPaintable(t) }, nil)
	}
	return img
}

const appCSS = `
.mpm-icon { border-radius: 10px; }
.mpm-quote { border-left: 3px solid alpha(currentColor, 0.3); padding-left: 10px; opacity: 0.85; }
.mpm-code { padding: 10px 12px; }
.mpm-side { font-size: smaller; font-weight: bold; padding: 2px 8px; border-radius: 99px; }
.mpm-side.client { background: alpha(@accent_bg_color, 0.18); color: @accent_color; }
.mpm-side.server { background: alpha(@warning_bg_color, 0.22); color: @warning_color; }
.mpm-side.both   { background: alpha(@success_bg_color, 0.18); color: @success_color; }
.mpm-side.unknown{ background: alpha(currentColor, 0.10); }
.mpm-source { font-size: smaller; opacity: 0.7; }
.mpm-dropzone:drop(active) { box-shadow: inset 0 0 0 3px alpha(@accent_bg_color, 0.7); background-color: alpha(@accent_bg_color, 0.06); }
`

func loadCSS() {
	p := gtk.NewCSSProvider()
	p.LoadFromString(appCSS)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), p, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

// sideBadge is a small coloured pill.
func sideBadge(side string, label string) *gtk.Label {
	l := gtk.NewLabel(label)
	l.AddCSSClass("mpm-side")
	l.AddCSSClass(strings.ToLower(side))
	l.SetVAlign(gtk.AlignCenter)
	return l
}
