package gtkui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/core"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

type searchView struct {
	a        *App
	dlg      *adw.Dialog
	entry    *gtk.SearchEntry
	source   *gtk.DropDown
	ptype    *gtk.DropDown
	sortBy   *gtk.DropDown
	group    *gtk.DropDown
	results  *gtk.ListBox
	status   *gtk.Label
	more     *gtk.Button
	spinner  *gtk.Spinner
	details  *gtk.ScrolledWindow
	page     int
	gen      int
	cancel   context.CancelFunc
	timer    glib.SourceHandle
	hits     []*core.Hit // in row order; GTK hands signals fresh wrappers, so look up by index
	detailCx context.CancelFunc
	groupIDs []string
	dirty    bool
	toasts   *adw.ToastOverlay
	addBtns  map[string][]*gtk.Button // hit key -> its Add buttons (row + details)
}

func (s *searchView) toast(format string, args ...any) {
	t := adw.NewToast(fmt.Sprintf(format, args...))
	t.SetTimeout(4)
	s.toasts.AddToast(t)
}

var sourceChoices = []struct {
	label string
	srcs  []model.Source
	key   string
}{
	{"Modrinth", []model.Source{model.SourceModrinth}, "modrinth"},
	{"CurseForge", []model.Source{model.SourceCurseForge}, "curseforge"},
	{"Both", []model.Source{model.SourceModrinth, model.SourceCurseForge}, "both"},
}

var typeChoices = []struct{ label, key string }{{"Mods", "mod"}, {"Resource packs", "resourcepack"}, {"Shaders", "shader"}}
var sortChoices = []struct{ label, key string }{{"Relevance", "relevance"}, {"Downloads", "downloads"}, {"Recently updated", "updated"}}

func labels[T any](xs []T, f func(T) string) []string {
	var out []string
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func (a *App) showSearch() {
	s := &searchView{a: a, addBtns: map[string][]*gtk.Button{}}
	s.dlg = adw.NewDialog()
	s.dlg.SetTitle("Add Mods")
	s.dlg.SetContentWidth(1150)
	s.dlg.SetContentHeight(780)

	tv := adw.NewToolbarView()
	hb := adw.NewHeaderBar()
	p := a.doc.Pack
	hb.SetTitleWidget(adw.NewWindowTitle("Add Mods", fmt.Sprintf("Minecraft %s · %s", p.Minecraft, model.LoaderLabel(p.Loader))))
	tv.AddTopBar(hb)

	bar := gtk.NewBox(gtk.OrientationHorizontal, 8)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginBottom(8)
	s.entry = gtk.NewSearchEntry()
	s.entry.SetPlaceholderText("Search mods…")
	s.entry.SetHExpand(true)
	bar.Append(s.entry)

	s.source = gtk.NewDropDownFromStrings(labels(sourceChoices, func(c struct {
		label string
		srcs  []model.Source
		key   string
	}) string {
		return c.label
	}))
	for i, c := range sourceChoices {
		if c.key == a.cfg.SearchSource {
			s.source.SetSelected(uint(i))
		}
	}
	s.source.SetTooltipText("Where to search")
	bar.Append(s.source)
	s.ptype = gtk.NewDropDownFromStrings(labels(typeChoices, func(c struct{ label, key string }) string { return c.label }))
	bar.Append(s.ptype)
	s.sortBy = gtk.NewDropDownFromStrings(labels(sortChoices, func(c struct{ label, key string }) string { return c.label }))
	bar.Append(s.sortBy)

	groupNames := []string{"Add to: Ungrouped"}
	s.groupIDs = []string{""}
	for _, g := range p.Groups {
		groupNames = append(groupNames, "Add to: "+g.Name)
		s.groupIDs = append(s.groupIDs, g.ID)
	}
	s.group = gtk.NewDropDownFromStrings(groupNames)
	s.group.SetTooltipText("Group that added mods (and their dependencies) go into")
	bar.Append(s.group)
	tv.AddTopBar(bar)

	// Results column
	s.results = gtk.NewListBox()
	s.results.AddCSSClass("boxed-list")
	s.results.SetSelectionMode(gtk.SelectionSingle)
	s.results.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row != nil {
			if i := row.Index(); i >= 0 && i < len(s.hits) {
				s.showDetails(s.hits[i])
			}
		}
	})
	s.status = gtk.NewLabel("")
	s.status.AddCSSClass("dim-label")
	s.status.SetWrap(true)
	s.status.SetMarginTop(24)
	s.spinner = gtk.NewSpinner()
	s.more = gtk.NewButtonWithLabel("Load more")
	s.more.AddCSSClass("pill")
	s.more.SetHAlign(gtk.AlignCenter)
	s.more.SetVisible(false)
	s.more.ConnectClicked(func() { s.page++; s.run(false) })

	col := gtk.NewBox(gtk.OrientationVertical, 12)
	col.SetMarginStart(12)
	col.SetMarginEnd(6)
	col.SetMarginBottom(12)
	col.Append(s.results)
	col.Append(s.spinner)
	col.Append(s.status)
	col.Append(s.more)
	left := gtk.NewScrolledWindow()
	left.SetChild(col)
	left.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	left.SetSizeRequest(400, -1)

	s.details = gtk.NewScrolledWindow()
	s.details.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	s.details.SetPropagateNaturalWidth(false)
	s.setDetailsPlaceholder()

	paned := gtk.NewPaned(gtk.OrientationHorizontal)
	paned.SetStartChild(left)
	paned.SetEndChild(s.details)
	paned.SetPosition(430)
	paned.SetShrinkStartChild(false)
	paned.SetShrinkEndChild(true)
	paned.SetResizeStartChild(false)
	tv.SetContent(paned)
	s.toasts = adw.NewToastOverlay()
	s.toasts.SetChild(tv)
	s.dlg.SetChild(s.toasts)

	s.entry.ConnectSearchChanged(func() { s.schedule() })
	s.entry.ConnectActivate(func() { s.runNow() })
	for _, dd := range []*gtk.DropDown{s.source, s.ptype, s.sortBy} {
		dd.NotifyProperty("selected", func() { s.runNow() })
	}
	s.source.NotifyProperty("selected", func() {
		a.cfg.SearchSource = sourceChoices[s.source.Selected()].key
		_ = a.cfg.Save()
	})
	s.dlg.ConnectClosed(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.detailCx != nil {
			s.detailCx()
		}
		if s.dirty {
			a.markDirty()
			a.refreshList()
		}
	})
	s.dlg.Present(a.win)
	s.entry.GrabFocus()
	s.runNow()
}

func (s *searchView) setDetailsPlaceholder() {
	st := adw.NewStatusPage()
	st.SetIconName("system-search-symbolic")
	st.SetTitle("Pick a result")
	st.SetDescription("Its full description shows up here.")
	s.details.SetChild(st)
}

// schedule debounces typing.
func (s *searchView) schedule() {
	if s.timer != 0 {
		glib.SourceRemove(s.timer)
	}
	s.timer = glib.TimeoutAdd(300, func() bool {
		s.timer = 0
		s.runNow()
		return false
	})
}

func (s *searchView) runNow() {
	s.page = 0
	s.run(true)
}

func (s *searchView) request() core.SearchRequest {
	p := s.a.doc.Pack
	return core.SearchRequest{
		Query:       strings.TrimSpace(s.entry.Text()),
		Sources:     sourceChoices[s.source.Selected()].srcs,
		Minecraft:   p.Minecraft,
		Loader:      p.Loader,
		ProjectType: typeChoices[s.ptype.Selected()].key,
		Sort:        sortChoices[s.sortBy.Selected()].key,
		Page:        s.page,
		PageSize:    20,
	}
}

func (s *searchView) run(clear bool) {
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.gen++
	gen := s.gen
	req := s.request()
	if clear {
		for r := s.results.FirstChild(); r != nil; r = s.results.FirstChild() {
			s.results.Remove(r)
		}
		s.hits = nil
	}
	s.status.SetText("")
	s.more.SetVisible(false)
	s.spinner.SetVisible(true)
	s.spinner.Start()
	go func() {
		res := s.a.svc.Search(ctx, req)
		ui(func() {
			if gen != s.gen {
				return
			}
			s.spinner.Stop()
			s.spinner.SetVisible(false)
			terms := markup.Terms(req.Query)
			for _, h := range res.Hits {
				row := s.resultRow(ctx, h, terms)
				s.results.Append(row)
				s.hits = append(s.hits, h)
			}
			var msgs []string
			for src, err := range res.Errors {
				if errors.Is(err, curseforge.ErrNoAPIKey) {
					msgs = append(msgs, "CurseForge needs an API key — add one in Preferences (main menu).")
				} else if ctx.Err() == nil {
					msgs = append(msgs, fmt.Sprintf("%s search failed: %v", src.Label(), err))
				}
			}
			if len(s.hits) == 0 && len(msgs) == 0 {
				msgs = append(msgs, "No results. Try fewer words, or a different source.")
			}
			s.status.SetText(strings.Join(msgs, "\n"))
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

func (s *searchView) inPack(h *core.Hit) bool {
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

func (s *searchView) resultRow(ctx context.Context, h *core.Hit, terms []string) *gtk.ListBoxRow {
	row := gtk.NewListBoxRow()
	box := gtk.NewBox(gtk.OrientationHorizontal, 12)
	box.SetMarginTop(10)
	box.SetMarginBottom(10)
	box.SetMarginStart(10)
	box.SetMarginEnd(10)
	icon := s.a.iconWidget(ctx, h.IconURL, 48, "application-x-addon-symbolic")
	icon.SetVAlign(gtk.AlignStart)
	box.Append(icon)

	text := gtk.NewBox(gtk.OrientationVertical, 3)
	text.SetHExpand(true)
	title := gtk.NewLabel("")
	title.SetMarkup("<b>" + markup.Highlight(h.Title, terms, markup.Pango) + "</b>")
	title.SetXAlign(0)
	title.SetEllipsize(pango.EllipsizeEnd)
	text.Append(title)

	meta := []string{}
	if h.Author != "" {
		meta = append(meta, "by "+h.Author)
	}
	meta = append(meta, formatCount(h.Downloads)+" downloads", h.Source.Label())
	if h.AlsoOn != "" {
		meta[len(meta)-1] += " + " + h.AlsoOn.Label()
	}
	ml := gtk.NewLabel(strings.Join(meta, " · "))
	ml.AddCSSClass("mpm-source")
	ml.SetXAlign(0)
	ml.SetEllipsize(pango.EllipsizeEnd)
	text.Append(ml)

	// Summary: Markdown stripped of block structure, with matches highlighted.
	sum := gtk.NewLabel("")
	plain := markup.MarkdownToPlain(h.Summary)
	if len(terms) > 0 {
		sum.SetMarkup(markup.Highlight(markup.Snippet(plain, terms, 160), terms, markup.Pango))
	} else {
		sum.SetMarkup(markup.InlineMarkdown(h.Summary, markup.Pango))
	}
	sum.SetXAlign(0)
	sum.SetWrap(true)
	sum.SetLines(2)
	sum.SetEllipsize(pango.EllipsizeEnd)
	sum.AddCSSClass("dim-label")
	text.Append(sum)
	box.Append(text)

	right := gtk.NewBox(gtk.OrientationVertical, 6)
	right.SetVAlign(gtk.AlignCenter)
	if h.Side != model.SideUnknown && h.Side != "" {
		right.Append(sideBadge(string(h.Side), h.Side.Label()))
	}
	add := gtk.NewButtonWithLabel("Add")
	add.AddCSSClass("suggested-action")
	add.AddCSSClass("pill")
	if s.inPack(h) {
		add.SetLabel("Added")
		add.SetSensitive(false)
	}
	add.ConnectClicked(func() { s.add(h, add) })
	s.addBtns[h.Key()] = append(s.addBtns[h.Key()], add)
	right.Append(add)
	box.Append(right)
	row.SetChild(box)
	return row
}

func (s *searchView) add(h *core.Hit, btn *gtk.Button) {
	btns := append([]*gtk.Button{btn}, s.addBtns[h.Key()]...)
	set := func(label string, sensitive bool) {
		for _, b := range btns {
			b.SetLabel(label)
			b.SetSensitive(sensitive)
		}
	}
	set("Adding…", false)
	gid := s.groupIDs[s.group.Selected()]
	doc := s.a.doc
	go func() {
		res, err := s.a.svc.AddProject(context.Background(), doc.Pack, h.Source, h.ID, gid)
		ui(func() {
			if err != nil {
				set("Add", true)
				s.a.error("Couldn't add "+h.Title, err.Error())
				return
			}
			set("Added", false)
			s.dirty = true
			switch n := len(res.Added); {
			case n == 0:
				s.toast("%s is already in the pack", h.Title)
			case n == 1:
				s.toast("Added %s", res.Added[0].DisplayName())
			default:
				var deps []string
				for _, m := range res.Added[1:] {
					deps = append(deps, m.DisplayName())
				}
				s.toast("Added %s with %s", res.Added[0].DisplayName(), strings.Join(deps, ", "))
			}
			for _, w := range res.Warnings {
				s.toast("%s", w)
			}
		})
	}()
}

func (s *searchView) showDetails(h *core.Hit) {
	if s.detailCx != nil {
		s.detailCx()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.detailCx = cancel
	s.details.SetChild(s.a.detailsWidget(ctx, h, nil, func(btn *gtk.Button) { s.add(h, btn) }, s.inPack(h), func(btn *gtk.Button) {
		s.addBtns[h.Key()] = append(s.addBtns[h.Key()], btn)
	}))
}

// detailsWidget shows a project's header, links and rendered description.
// It is shared by the search dialog and the mod list.
func (a *App) detailsWidget(ctx context.Context, h *core.Hit, mod *model.Mod, onAdd func(*gtk.Button), added bool, register func(*gtk.Button)) gtk.Widgetter {
	outer := gtk.NewBox(gtk.OrientationVertical, 14)
	outer.SetMarginTop(18)
	outer.SetMarginBottom(24)
	outer.SetMarginStart(18)
	outer.SetMarginEnd(18)

	head := gtk.NewBox(gtk.OrientationHorizontal, 14)
	head.Append(a.iconWidget(ctx, h.IconURL, 64, "application-x-addon-symbolic"))
	tb := gtk.NewBox(gtk.OrientationVertical, 4)
	tb.SetHExpand(true)
	t := gtk.NewLabel("")
	t.SetMarkup(`<span size="x-large" weight="bold">` + markup.Escape(markup.Pango, h.Title) + `</span>`)
	t.SetXAlign(0)
	t.SetWrap(true)
	tb.Append(t)
	if h.Author != "" {
		al := gtk.NewLabel("by " + h.Author)
		al.SetXAlign(0)
		al.AddCSSClass("dim-label")
		tb.Append(al)
	}
	if h.Summary != "" {
		sl := a.richLabel(markup.InlineMarkdown(h.Summary, markup.Pango))
		tb.Append(sl)
	}
	head.Append(tb)
	if onAdd != nil {
		btn := gtk.NewButtonWithLabel("Add to pack")
		btn.AddCSSClass("suggested-action")
		btn.AddCSSClass("pill")
		btn.SetVAlign(gtk.AlignStart)
		if added {
			btn.SetLabel("Added")
			btn.SetSensitive(false)
		}
		btn.ConnectClicked(func() { onAdd(btn) })
		if register != nil {
			register(btn)
		}
		head.Append(btn)
	}
	outer.Append(head)

	links := gtk.NewBox(gtk.OrientationHorizontal, 6)
	outer.Append(links)
	body := gtk.NewBox(gtk.OrientationVertical, 0)
	sp := gtk.NewSpinner()
	sp.Start()
	sp.SetSizeRequest(32, 32)
	body.Append(sp)
	outer.Append(body)

	go func() {
		d, err := a.svc.ProjectDetails(ctx, h)
		ui(func() {
			if ctx.Err() != nil {
				return
			}
			body.Remove(sp)
			if err != nil {
				msg := err.Error()
				if errors.Is(err, curseforge.ErrNoAPIKey) {
					msg = "Add a CurseForge API key in Preferences to see this description."
				}
				l := gtk.NewLabel(msg)
				l.SetWrap(true)
				l.AddCSSClass("dim-label")
				body.Append(l)
				return
			}
			if h.Side != model.SideUnknown && h.Side != "" {
				links.Append(sideBadge(string(h.Side), h.Side.Label()))
			}
			for _, lk := range d.Links {
				lk := lk
				b := gtk.NewButtonWithLabel(lk[0])
				b.AddCSSClass("flat")
				b.SetVAlign(gtk.AlignCenter)
				b.SetTooltipText(lk[1])
				b.ConnectClicked(func() { a.openURI(lk[1]) })
				links.Append(b)
			}
			if d.License != "" {
				ll := gtk.NewLabel("License: " + d.License)
				ll.AddCSSClass("dim-label")
				ll.SetXAlign(0)
				body.Append(ll)
			}
			blocks := d.Blocks(markup.Pango)
			if len(blocks) == 0 {
				l := gtk.NewLabel("No description.")
				l.AddCSSClass("dim-label")
				body.Append(l)
				return
			}
			body.Append(a.renderBlocks(ctx, blocks))
		})
	}()
	return outer
}
