// WORK IN PROGRESS — not compiled yet.
package gtkui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"modpackmanager/internal/core"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

// editor widgets that refreshList rebuilds.
type editor struct {
	list   *gtk.Box
	filter *gtk.SearchEntry
	title  *adw.WindowTitle
}

var ed editor

func (a *App) showEditor(doc *core.Document) {
	a.doc = doc
	tv := adw.NewToolbarView()
	hb := adw.NewHeaderBar()
	ed.title = adw.NewWindowTitle(doc.Title(), "")
	hb.SetTitleWidget(ed.title)

	save := gtk.NewButtonFromIconName("document-save-symbolic")
	save.SetTooltipText("Save as .mpm (Ctrl+S)")
	save.ConnectClicked(a.save)
	hb.PackStart(save)

	add := gtk.NewButtonFromIconName("list-add-symbolic")
	add.SetTooltipText("Add mods from Modrinth / CurseForge")
	add.ConnectClicked(a.showSearch)
	hb.PackStart(add)

	group := gtk.NewButtonFromIconName("folder-new-symbolic")
	group.SetTooltipText("New group")
	group.ConnectClicked(a.newGroup)
	hb.PackStart(group)

	// The three export buttons the pack is built around.
	exports := gtk.NewBox(gtk.OrientationHorizontal, 0)
	exports.AddCSSClass("linked")
	for _, t := range []core.Target{core.TargetClient, core.TargetCommon, core.TargetServer} {
		t := t
		b := gtk.NewButtonWithLabel(t.Label())
		b.SetTooltipText(map[core.Target]string{
			core.TargetClient: "Export client-only mods",
			core.TargetCommon: "Export mods needed on client and server",
			core.TargetServer: "Export a server pack",
		}[t])
		b.ConnectClicked(func() { a.showExport(t) })
		exports.Append(b)
	}
	hb.PackEnd(exports)
	tv.AddTopBar(hb)

	ed.filter = gtk.NewSearchEntry()
	ed.filter.SetPlaceholderText("Filter mods — try side:client, group:\"World gen\", source:cf")
	ed.filter.ConnectSearchChanged(a.refreshList)
	bar := gtk.NewBox(gtk.OrientationHorizontal, 0)
	bar.SetMarginTop(6)
	bar.SetMarginBottom(6)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	ed.filter.SetHExpand(true)
	bar.Append(ed.filter)
	tv.AddTopBar(bar)

	ed.list = gtk.NewBox(gtk.OrientationVertical, 18)
	ed.list.SetMarginTop(12)
	ed.list.SetMarginBottom(24)
	clamp := adw.NewClamp()
	clamp.SetMaximumSize(900)
	clamp.SetChild(ed.list)
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(clamp)
	scroll.SetVExpand(true)
	tv.SetContent(scroll)
	a.toasts.SetChild(tv)
	a.refreshList()
}

func (a *App) markDirty() {
	a.doc.Dirty = true
	ed.title.SetTitle(a.doc.Title())
}

// refreshList rebuilds the grouped mod list.
func (a *App) refreshList() {
	if a.doc == nil || ed.list == nil {
		return
	}
	p := a.doc.Pack
	for c := ed.list.FirstChild(); c != nil; c = ed.list.FirstChild() {
		ed.list.Remove(c)
	}
	counts := p.SideCounts()
	ed.title.SetTitle(a.doc.Title())
	ed.title.SetSubtitle(fmt.Sprintf("Minecraft %s · %s %s · %d mods (%d common, %d client, %d server)",
		p.Minecraft, model.LoaderLabel(p.Loader), p.LoaderVersion, len(p.Mods), counts[model.SideBoth]+counts[model.SideUnknown], counts[model.SideClient], counts[model.SideServer]))
	f := core.ParseFilter(ed.filter.Text())
	terms := f.Terms
	shown := 0
	for _, sec := range p.Sections(func(m *model.Mod) bool { return f.Match(p, m) }) {
		if len(sec.Mods) == 0 && !f.Empty() {
			continue
		}
		grp := adw.NewPreferencesGroup()
		name := "Ungrouped"
		if sec.Group != nil {
			name = sec.Group.Name
		}
		grp.SetTitle(markup.Escape(markup.Pango, name))
		grp.SetDescription(fmt.Sprintf("%d mods", len(sec.Mods)))
		for _, m := range sec.Mods {
			grp.Add(a.modRow(m, terms))
			shown++
		}
		ed.list.Append(grp)
	}
	if shown == 0 && len(p.Mods) > 0 {
		st := adw.NewStatusPage()
		st.SetIconName("edit-find-symbolic")
		st.SetTitle("No matching mods")
		ed.list.Append(st)
	}
}

func (a *App) modRow(m *model.Mod, terms []string) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetUseMarkup(true)
	row.SetTitle(markup.Highlight(m.DisplayName(), terms, markup.Pango))
	sub := markup.InlineMarkdown(m.Summary, markup.Pango)
	if sub == "" {
		sub = markup.Escape(markup.Pango, m.File.Name())
	}
	row.SetSubtitle(sub)
	row.SetSubtitleLines(1)

	// Side selector
	sides := gtk.NewStringList([]string{"Client & server", "Client only", "Server only", "Unknown"})
	dd := gtk.NewDropDown(sides, nil)
	dd.SetVAlign(gtk.AlignCenter)
	idx := map[model.Side]uint{model.SideBoth: 0, model.SideClient: 1, model.SideServer: 2, model.SideUnknown: 3}
	dd.SetSelected(idx[m.Side])
	dd.NotifyProperty("selected", func() {
		ns := []model.Side{model.SideBoth, model.SideClient, model.SideServer, model.SideUnknown}[dd.Selected()]
		if ns != m.Side {
			m.Side, m.SideLocked = ns, true
			a.markDirty()
		}
	})
	row.AddSuffix(dd)
	return row
}

func (a *App) save() {
	if a.doc.Path != "" {
		a.saveTo(a.doc.Path)
		return
	}
	d := gtk.NewFileDialog()
	d.SetTitle("Save Modpack")
	d.SetInitialName(filepath.Base(a.doc.SuggestedSavePath()))
	d.Save(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SaveFinish(res)
		if err != nil || f == nil {
			return
		}
		a.saveTo(f.Path())
	})
}

func (a *App) saveTo(path string) {
	if err := a.doc.Save(path); err != nil {
		a.error("Couldn't save", err.Error())
		return
	}
	a.cfg.AddRecent(a.doc.Path)
	_ = a.cfg.Save()
	a.refreshList()
	a.toast("Saved %s", filepath.Base(a.doc.Path))
}

func (a *App) newGroup() {
	d := adw.NewAlertDialog("New group", "")
	entry := gtk.NewEntry()
	entry.SetPlaceholderText("e.g. Performance")
	d.SetExtraChild(entry)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("create", "Create")
	d.SetResponseAppearance("create", adw.ResponseSuggested)
	d.ConnectResponse(func(r string) {
		if r == "create" && entry.Text() != "" {
			a.doc.Pack.AddGroup(entry.Text())
			a.markDirty()
			a.refreshList()
		}
	})
	d.Present(a.win)
}

// showExport asks for format/options, then writes the file.
func (a *App) showExport(t core.Target) {
	opt := core.DefaultExportOptions(t)
	d := gtk.NewFileDialog()
	d.SetTitle("Export " + t.Label())
	d.SetInitialName(core.SuggestedFileName(a.doc.Pack, opt))
	d.Save(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SaveFinish(res)
		if err != nil || f == nil {
			return
		}
		out := f.Path()
		a.toast("Exporting %s…", t.Label())
		go func() {
			data, rep, err := a.svc.Export(context.Background(), a.doc.Pack, opt, nil, nil)
			if err == nil {
				err = os.WriteFile(out, data, 0o644)
			}
			ui(func() {
				if err != nil {
					a.error("Export failed", err.Error())
					return
				}
				a.toast("Exported %d mods to %s", rep.Mods, filepath.Base(out))
			})
		}()
	})
}

// showSearch: add-mods dialog (next step).
func (a *App) showSearch() { a.toast("Search coming next") }
