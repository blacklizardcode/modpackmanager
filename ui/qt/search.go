package qtui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/core"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

var sourceChoices = []struct {
	label string
	srcs  []model.Source
	key   string
}{
	{"Modrinth", []model.Source{model.SourceModrinth}, "modrinth"},
	{"CurseForge", []model.Source{model.SourceCurseForge}, "curseforge"},
	{"Modrinth + CurseForge", []model.Source{model.SourceModrinth, model.SourceCurseForge}, "both"},
}
var typeChoices = []struct{ label, key string }{{"Mods", "mod"}, {"Resource packs", "resourcepack"}, {"Shaders", "shader"}}
var sortChoices = []struct{ label, key string }{{"Relevance", "relevance"}, {"Downloads", "downloads"}, {"Recently updated", "updated"}}

type searchDlg struct {
	a        *App
	d        *qt.QDialog
	entry    *qt.QLineEdit
	source   *qt.QComboBox
	ptype    *qt.QComboBox
	sortBy   *qt.QComboBox
	group    *qt.QComboBox
	list     *qt.QListWidget
	more     *qt.QPushButton
	status   *qt.QLabel
	browser  *qt.QTextBrowser
	addBtn   *qt.QPushButton
	timer    *qt.QTimer
	hits     []*core.Hit
	groupIDs []string
	page     int
	gen      int
	cancel   context.CancelFunc
	detail   *core.Hit
	dgen     int
	added    map[string]bool
	changed  bool
}

func (a *App) showSearch() {
	if a.doc == nil {
		return
	}
	s := &searchDlg{a: a, added: map[string]bool{}}
	s.d = qt.NewQDialog(a.win.QWidget)
	p := a.doc.Pack
	s.d.SetWindowTitle(fmt.Sprintf("Add Mods — Minecraft %s · %s", p.Minecraft, model.LoaderLabel(p.Loader)))
	s.d.Resize(1150, 760)

	v := qt.NewQVBoxLayout2()
	row := qt.NewQHBoxLayout2()
	s.entry = qt.NewQLineEdit2()
	s.entry.SetPlaceholderText("Search mods…")
	s.entry.SetClearButtonEnabled(true)
	row.AddWidget2(s.entry.QWidget, 1)
	s.source = qt.NewQComboBox2()
	for i, c := range sourceChoices {
		s.source.AddItem(c.label)
		if c.key == a.cfg.SearchSource {
			s.source.SetCurrentIndex(i)
		}
	}
	row.AddWidget(s.source.QWidget)
	s.ptype = qt.NewQComboBox2()
	for _, c := range typeChoices {
		s.ptype.AddItem(c.label)
	}
	row.AddWidget(s.ptype.QWidget)
	s.sortBy = qt.NewQComboBox2()
	for _, c := range sortChoices {
		s.sortBy.AddItem(c.label)
	}
	row.AddWidget(s.sortBy.QWidget)
	s.group = qt.NewQComboBox2()
	s.group.AddItem("Add to: Ungrouped")
	s.groupIDs = []string{""}
	for _, g := range p.Groups {
		s.group.AddItem("Add to: " + g.Name)
		s.groupIDs = append(s.groupIDs, g.ID)
	}
	s.group.SetToolTip("Group that added mods (and their dependencies) go into")
	row.AddWidget(s.group.QWidget)
	v.AddLayout(row.QLayout)

	split := qt.NewQSplitter3(qt.Horizontal)
	left := qt.NewQWidget2()
	lv := qt.NewQVBoxLayout2()
	lv.SetContentsMargins(0, 0, 0, 0)
	s.list = qt.NewQListWidget2()
	s.list.SetAlternatingRowColors(true)
	s.list.SetIconSize(qt.NewQSize2(48, 48))
	s.list.SetWordWrap(true)
	s.list.OnCurrentRowChanged(func(r int) {
		if r >= 0 && r < len(s.hits) {
			s.showDetails(s.hits[r])
		}
	})
	s.list.OnItemActivated(func(it *qt.QListWidgetItem) {
		if r := s.list.CurrentRow(); r >= 0 && r < len(s.hits) {
			s.add(s.hits[r])
		}
	})
	lv.AddWidget(s.list.QWidget)
	s.status = qt.NewQLabel2()
	s.status.SetWordWrap(true)
	lv.AddWidget(s.status.QWidget)
	s.more = qt.NewQPushButton3("Load more")
	s.more.SetVisible(false)
	s.more.OnClicked(func() { s.page++; s.run(false) })
	lv.AddWidget(s.more.QWidget)
	left.SetLayout(lv.QLayout)
	split.AddWidget(left)

	right := qt.NewQWidget2()
	rv := qt.NewQVBoxLayout2()
	rv.SetContentsMargins(0, 0, 0, 0)
	s.browser = qt.NewQTextBrowser2()
	s.browser.SetOpenLinks(false)
	s.browser.OnAnchorClicked(func(u *qt.QUrl) { qt.QDesktopServices_OpenUrl(u) })
	s.browser.SetHtml("<p style='color:gray'>Pick a result to see its full description.</p>")
	rv.AddWidget(s.browser.QWidget)
	s.addBtn = qt.NewQPushButton3("Add to Pack")
	s.addBtn.SetIcon(themeIcon("list-add"))
	s.addBtn.SetEnabled(false)
	s.addBtn.OnClicked(func() {
		if s.detail != nil {
			s.add(s.detail)
		}
	})
	rv.AddWidget(s.addBtn.QWidget)
	right.SetLayout(rv.QLayout)
	split.AddWidget(right)
	split.SetSizes([]int{480, 640})
	split.SetChildrenCollapsible(false)
	v.AddWidget2(split.QWidget, 1)

	bb := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Close)
	bb.OnRejected(s.d.Reject)
	v.AddWidget(bb.QWidget)
	s.d.SetLayout(v.QLayout)

	s.timer = qt.NewQTimer2(s.d.QObject)
	s.timer.SetSingleShot(true)
	s.timer.OnTimeout(func() { s.page = 0; s.run(true) })
	s.entry.OnTextChanged(func(string) { s.timer.Start(300) })
	s.entry.OnReturnPressed(func() { s.timer.Stop(); s.page = 0; s.run(true) })
	for _, cb := range []*qt.QComboBox{s.source, s.ptype, s.sortBy} {
		cb.OnCurrentIndexChanged(func(int) { s.page = 0; s.run(true) })
	}
	s.source.OnCurrentIndexChanged(func(i int) {
		a.cfg.SearchSource = sourceChoices[i].key
		_ = a.cfg.Save()
	})
	s.d.OnFinished(func(int) {
		if s.cancel != nil {
			s.cancel()
		}
		s.gen++
		s.dgen++
		if s.changed {
			a.markDirty()
			a.refresh()
		}
	})
	s.run(true)
	s.entry.SetFocus()
	s.d.Open()
}

func (s *searchDlg) run(clear bool) {
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.gen++
	gen := s.gen
	p := s.a.doc.Pack
	req := core.SearchRequest{
		Query: strings.TrimSpace(s.entry.Text()), Sources: sourceChoices[s.source.CurrentIndex()].srcs,
		Minecraft: p.Minecraft, Loader: p.Loader, ProjectType: typeChoices[s.ptype.CurrentIndex()].key,
		Sort: sortChoices[s.sortBy.CurrentIndex()].key, Page: s.page, PageSize: 20,
	}
	if clear {
		s.list.Clear()
		s.hits = nil
	}
	s.status.SetText("Searching…")
	s.more.SetVisible(false)
	go func() {
		res := s.a.svc.Search(ctx, req)
		ui(func() {
			if gen != s.gen {
				return
			}
			terms := markup.Terms(req.Query)
			for _, h := range res.Hits {
				s.appendHit(gen, h, terms)
			}
			var msgs []string
			for src, err := range res.Errors {
				if errors.Is(err, curseforge.ErrNoAPIKey) {
					msgs = append(msgs, "CurseForge needs an API key — set one in Preferences.")
				} else {
					msgs = append(msgs, fmt.Sprintf("%s search failed: %v", src.Label(), err))
				}
			}
			if len(s.hits) == 0 && len(msgs) == 0 {
				msgs = append(msgs, "No results. Try fewer words or another source.")
			}
			s.status.SetText(strings.Join(msgs, "\n"))
			s.status.SetVisible(len(msgs) > 0)
			s.more.SetVisible(res.HasMore && len(res.Hits) > 0)
		})
	}()
}

func formatCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func (s *searchDlg) inPack(h *core.Hit) bool {
	if s.added[h.Key()] {
		return true
	}
	m := &model.Mod{}
	switch h.Source {
	case model.SourceModrinth:
		m.Modrinth = &model.ModrinthRef{ProjectID: h.ID}
	case model.SourceCurseForge:
		var id int
		fmt.Sscan(h.ID, &id)
		m.CurseForge = &model.CurseForgeRef{ProjectID: id}
	}
	return s.a.doc.Pack.FindDuplicate(m) != nil
}

// appendHit adds a two-line rich row: bold (highlighted) title + meta, then
// the summary with Markdown stripped and matches highlighted.
func (s *searchDlg) appendHit(gen int, h *core.Hit, terms []string) {
	it := qt.NewQListWidgetItem()
	s.list.AddItemWithItem(it)
	s.hits = append(s.hits, h)

	w := qt.NewQWidget2()
	hl := qt.NewQHBoxLayout2()
	hl.SetContentsMargins(6, 6, 6, 6)
	icon := qt.NewQLabel2()
	icon.SetFixedSize2(48, 48)
	hl.AddWidget(icon.QWidget)
	meta := []string{}
	if h.Author != "" {
		meta = append(meta, "by "+h.Author)
	}
	src := h.Source.Label()
	if h.AlsoOn != "" {
		src += " + " + h.AlsoOn.Label()
	}
	meta = append(meta, formatCount(h.Downloads)+" downloads", src)
	plain := markup.MarkdownToPlain(h.Summary)
	sum := markup.InlineMarkdown(h.Summary, markup.QtRich)
	if len(terms) > 0 {
		sum = markup.Highlight(markup.Snippet(plain, terms, 160), terms, markup.QtRich)
	}
	side := ""
	if h.Side != model.SideUnknown && h.Side != "" {
		side = " · <span style='color:" + sideHex(h.Side) + "'>" + h.Side.Label() + "</span>"
	}
	if s.inPack(h) {
		side += " · <b>✓ in pack</b>"
	}
	text := qt.NewQLabel3("<b>" + markup.Highlight(h.Title, terms, markup.QtRich) + "</b><br/><small>" +
		markup.Escape(markup.QtRich, strings.Join(meta, " · ")) + side + "</small><br/>" + sum)
	text.SetTextFormat(qt.RichText)
	text.SetWordWrap(true)
	hl.AddWidget2(text.QWidget, 1)
	w.SetLayout(hl.QLayout)
	it.SetSizeHint(qt.NewQSize2(300, 92))
	s.list.SetItemWidget(it, w)

	if h.IconURL != "" {
		go func() {
			data, _, err := s.a.images.Load(context.Background(), h.IconURL, 96)
			ui(func() {
				if err != nil || gen != s.gen {
					return
				}
				pm := qt.NewQPixmap()
				if pm.LoadFromDataWithData(data) {
					icon.SetPixmap(pm.Scaled3(48, 48, qt.KeepAspectRatio, qt.SmoothTransformation))
				}
			})
		}()
	}
}

func sideHex(s model.Side) string {
	switch s {
	case model.SideClient:
		return "#3daee9"
	case model.SideServer:
		return "#f67400"
	case model.SideBoth:
		return "#27ae60"
	}
	return "#7f8c8d"
}

func (s *searchDlg) add(h *core.Hit) {
	if s.inPack(h) {
		s.status.SetText(h.Title + " is already in the pack.")
		s.status.SetVisible(true)
		return
	}
	s.addBtn.SetEnabled(false)
	s.addBtn.SetText("Adding…")
	gid := s.groupIDs[s.group.CurrentIndex()]
	doc := s.a.doc
	go func() {
		res, err := s.a.svc.AddProject(context.Background(), doc.Pack, h.Source, h.ID, gid)
		ui(func() {
			s.addBtn.SetText("Add to Pack")
			if err != nil {
				s.addBtn.SetEnabled(true)
				s.a.errorBox("Couldn't add "+h.Title, err.Error())
				return
			}
			s.added[h.Key()] = true
			s.changed = true
			s.addBtn.SetText("✓ Added")
			var names []string
			for _, m := range res.Added {
				names = append(names, m.DisplayName())
			}
			msg := "Added " + strings.Join(names, ", ")
			if len(names) == 0 {
				msg = h.Title + " is already in the pack."
			}
			if len(res.Warnings) > 0 {
				msg += "\n" + strings.Join(res.Warnings, "\n")
			}
			s.status.SetText(msg)
			s.status.SetVisible(true)
			s.a.status("%s", strings.Split(msg, "\n")[0])
		})
	}()
}

func (s *searchDlg) showDetails(h *core.Hit) {
	s.detail = h
	s.addBtn.SetEnabled(!s.inPack(h))
	if s.inPack(h) {
		s.addBtn.SetText("✓ In Pack")
	} else {
		s.addBtn.SetText("Add to Pack")
	}
	s.a.renderDetails(s.browser, h, &s.dgen)
}

// renderDetails shows a project's description in a QTextBrowser, loading
// images in the background. gen guards against stale callbacks.
func (a *App) renderDetails(b *qt.QTextBrowser, h *core.Hit, gen *int) {
	*gen++
	my := *gen
	head := "<h2>" + markup.Escape(markup.QtRich, h.Title) + "</h2>"
	if h.Author != "" {
		head += "<p>by " + markup.Escape(markup.QtRich, h.Author) + "</p>"
	}
	if h.Summary != "" {
		head += "<p><i>" + markup.InlineMarkdown(h.Summary, markup.QtRich) + "</i></p>"
	}
	b.SetHtml(head + "<p style='color:gray'>Loading description…</p>")
	go func() {
		d, err := a.svc.ProjectDetails(context.Background(), h)
		ui(func() {
			if *gen != my {
				return
			}
			if err != nil {
				msg := err.Error()
				if errors.Is(err, curseforge.ErrNoAPIKey) {
					msg = "Add a CurseForge API key in Preferences to see this description."
				}
				b.SetHtml(head + "<p style='color:gray'>" + markup.Escape(markup.QtRich, msg) + "</p>")
				return
			}
			info := []string{}
			if h.Side != model.SideUnknown && h.Side != "" {
				info = append(info, "<span style='color:"+sideHex(h.Side)+"'><b>"+h.Side.Label()+"</b></span>")
			}
			for _, l := range d.Links {
				info = append(info, `<a href="`+markup.Escape(markup.QtRich, l[1])+`">`+markup.Escape(markup.QtRich, l[0])+`</a>`)
			}
			if d.License != "" {
				info = append(info, "License: "+markup.Escape(markup.QtRich, d.License))
			}
			blocks := d.Blocks(markup.QtRich)
			html := head + "<p>" + strings.Join(info, " &nbsp;·&nbsp; ") + "</p><hr/>" + markup.BlocksToHTML(blocks)
			b.SetHtml(html)
			a.loadDocImages(b, html, markup.ImageURLs(blocks), gen, my)
		})
	}()
}

// loadDocImages fetches images, registers them as document resources under
// their URL and re-renders, keeping the scroll position.
func (a *App) loadDocImages(b *qt.QTextBrowser, html string, urls []string, gen *int, my int) {
	if len(urls) == 0 {
		return
	}
	pending := len(urls)
	for _, u := range urls {
		u := u
		go func() {
			data, _, err := a.images.Load(context.Background(), u, 560)
			ui(func() {
				if *gen != my {
					return
				}
				pending--
				if err == nil {
					img := qt.NewQImage()
					if img.LoadFromDataWithData(data) {
						b.Document().AddResource(int(qt.QTextDocument__ImageResource), qt.NewQUrl3(u), img.ToQVariant())
					}
				}
				if pending == 0 {
					sb := b.VerticalScrollBar()
					pos := sb.Value()
					b.SetHtml(html)
					sb.SetValue(pos)
				}
			})
		}()
	}
}
