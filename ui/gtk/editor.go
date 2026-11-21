package gtkui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"modpackmanager/internal/core"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
	"modpackmanager/internal/version"
)

// editor holds the widgets refreshList rebuilds.
type editor struct {
	list      *gtk.Box
	filter    *gtk.SearchEntry
	title     *adw.WindowTitle
	selecting bool
	selected  map[string]bool
	actionBar *gtk.ActionBar
	selLabel  *gtk.Label
	selectBtn *gtk.ToggleButton
	busy      *gtk.Spinner
	ctx       context.Context
	cancel    context.CancelFunc
}

func (a *App) showEditor(doc *core.Document) {
	if a.ed.cancel != nil {
		a.ed.cancel()
	}
	a.doc = doc
	a.ed = editor{selected: map[string]bool{}}
	a.ed.ctx, a.ed.cancel = context.WithCancel(context.Background())

	tv := adw.NewToolbarView()
	hb := adw.NewHeaderBar()
	a.ed.title = adw.NewWindowTitle(doc.Title(), "")
	hb.SetTitleWidget(a.ed.title)

	// "Add Mods" searches; its drop-down adds jar files from disk.
	add := adw.NewSplitButton()
	addContent := adw.NewButtonContent()
	addContent.SetIconName("list-add-symbolic")
	addContent.SetLabel("Add Mods")
	add.SetChild(addContent)
	add.AddCSSClass("suggested-action")
	add.SetTooltipText("Search Modrinth and CurseForge (Ctrl+Shift+A)")
	add.SetDropdownTooltip("More ways to add mods")
	addMenu := gio.NewMenu()
	addMenu.Append("Add JAR Files…", "win.add-jars")
	add.SetMenuModel(addMenu)
	add.ConnectClicked(a.showSearch)
	hb.PackStart(add)

	a.ed.selectBtn = gtk.NewToggleButton()
	a.ed.selectBtn.SetIconName("object-select-symbolic")
	a.ed.selectBtn.SetTooltipText("Select mods to group, set side or remove several at once")
	a.ed.selectBtn.ConnectToggled(func() {
		a.ed.selecting = a.ed.selectBtn.Active()
		if !a.ed.selecting {
			a.ed.selected = map[string]bool{}
		}
		a.ed.actionBar.SetRevealed(a.ed.selecting)
		a.refreshList()
	})
	hb.PackStart(a.ed.selectBtn)

	menu := gtk.NewMenuButton()
	menu.SetIconName("open-menu-symbolic")
	menu.SetTooltipText("Main menu")
	menu.SetPopover(a.mainMenu())
	hb.PackEnd(menu)

	// The three export buttons.
	exports := gtk.NewBox(gtk.OrientationHorizontal, 0)
	exports.AddCSSClass("linked")
	tips := map[core.Target]string{
		core.TargetClient: "Export the client-only mods",
		core.TargetCommon: "Export the mods needed on both client and server",
		core.TargetServer: "Export a server pack (server-only + common mods by default)",
	}
	for _, t := range []core.Target{core.TargetClient, core.TargetCommon, core.TargetServer} {
		t := t
		b := gtk.NewButtonWithLabel(t.Label())
		b.SetTooltipText(tips[t])
		b.ConnectClicked(func() { a.showExport(t) })
		exports.Append(b)
	}
	exportLabel := gtk.NewLabel("Export")
	exportLabel.AddCSSClass("dim-label")
	exportLabel.SetMarginEnd(2)
	hb.PackEnd(exports)
	hb.PackEnd(exportLabel)

	a.ed.busy = gtk.NewSpinner()
	a.ed.busy.SetTooltipText("Fetching mod info…")
	a.ed.busy.SetVisible(false)
	hb.PackStart(a.ed.busy)

	save := gtk.NewButtonFromIconName("document-save-symbolic")
	save.SetTooltipText("Save as .mpm (Ctrl+S)")
	save.ConnectClicked(a.save)
	hb.PackEnd(save)
	tv.AddTopBar(hb)

	a.ed.filter = gtk.NewSearchEntry()
	a.ed.filter.SetPlaceholderText(`Filter mods…  (side:client  group:"World gen"  source:cf)`)
	a.ed.filter.ConnectSearchChanged(a.refreshList)
	a.ed.filter.SetHExpand(true)
	fclamp := adw.NewClamp()
	fclamp.SetMaximumSize(900)
	fclamp.SetChild(a.ed.filter)
	fclamp.SetMarginTop(6)
	fclamp.SetMarginBottom(6)
	fclamp.SetMarginStart(12)
	fclamp.SetMarginEnd(12)
	tv.AddTopBar(fclamp)

	a.ed.list = gtk.NewBox(gtk.OrientationVertical, 22)
	a.ed.list.SetMarginTop(12)
	a.ed.list.SetMarginBottom(24)
	a.ed.list.SetMarginStart(12)
	a.ed.list.SetMarginEnd(12)
	clamp := adw.NewClamp()
	clamp.SetMaximumSize(900)
	clamp.SetChild(a.ed.list)
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(clamp)
	scroll.SetVExpand(true)
	tv.SetContent(scroll)

	// Bulk actions for selection mode.
	a.ed.actionBar = gtk.NewActionBar()
	a.ed.selLabel = gtk.NewLabel("0 selected")
	a.ed.actionBar.SetCenterWidget(a.ed.selLabel)
	moveBtn := gtk.NewButtonWithLabel("Move to Group…")
	moveBtn.ConnectClicked(func() { a.bulkMoveToGroup() })
	sideBtn := gtk.NewMenuButton()
	sideBtn.SetLabel("Set Side")
	sideBtn.SetPopover(a.sidePopover(func(s model.Side) { a.bulkSetSide(s) }))
	sideBtn.SetDirection(gtk.ArrowUp)
	a.ed.actionBar.PackStart(moveBtn)
	a.ed.actionBar.PackStart(sideBtn)
	rm := gtk.NewButtonWithLabel("Remove")
	rm.AddCSSClass("destructive-action")
	rm.ConnectClicked(a.bulkRemove)
	a.ed.actionBar.PackEnd(rm)
	a.ed.actionBar.SetRevealed(false)
	tv.AddBottomBar(a.ed.actionBar)

	// Jar files (or a mods folder) dropped anywhere on the editor are added.
	drop := gtk.NewDropTarget(gdk.GTypeFileList, gdk.ActionCopy)
	drop.ConnectDrop(func(v *coreglib.Value, _, _ float64) bool {
		fl, ok := v.GoValue().(*gdk.FileList)
		if !ok || a.doc != doc {
			return false
		}
		var paths []string
		for _, f := range fl.Files() {
			if p := f.Path(); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			a.toast("Only files on this computer can be added")
			return false
		}
		a.addJarPaths(paths)
		return true
	})
	tv.AddController(drop)
	tv.AddCSSClass("mpm-dropzone")

	a.toasts.SetChild(tv)
	a.installShortcuts()
	a.refreshList()
}

func (a *App) mainMenu() *gtk.Popover {
	pop := gtk.NewPopover()
	box := gtk.NewBox(gtk.OrientationVertical, 2)
	item := func(label string, f func()) {
		b := gtk.NewButtonWithLabel(label)
		b.AddCSSClass("flat")
		if l, ok := b.Child().(*gtk.Label); ok {
			l.SetXAlign(0)
		}
		b.ConnectClicked(func() { pop.Popdown(); f() })
		box.Append(b)
	}
	item("Pack Settings…", a.showPackSettings)
	item("New Group…", a.newGroup)
	item("Save As…", a.saveAs)
	item("Export Full Pack…", func() { a.showExport(core.TargetFull) })
	item("Refresh Mod Info", a.enrich)
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	item("Open Another Pack…", func() { a.confirmDiscard(a.chooseOpen) })
	item("Close Pack", func() { a.confirmDiscard(func() { a.doc = nil; a.showWelcome() }) })
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	item("Preferences…", a.showPreferences)
	item("About Modpack Manager", a.showAbout)
	pop.SetChild(box)
	return pop
}

func (a *App) installShortcuts() {
	if a.shortcutsInstalled {
		return
	}
	a.shortcutsInstalled = true
	sc := gtk.NewShortcutController()
	sc.SetScope(gtk.ShortcutScopeGlobal)
	add := func(trigger string, f func()) {
		sc.AddShortcut(gtk.NewShortcut(gtk.NewShortcutTriggerParseString(trigger), gtk.NewCallbackAction(func(gtk.Widgetter, *glibv2.Variant) bool {
			if a.doc == nil {
				return false
			}
			f()
			return true
		})))
	}
	add("<Control>s", a.save)
	add("<Control><Shift>s", a.saveAs)
	add("<Control>o", func() { a.confirmDiscard(a.chooseOpen) })
	add("<Control>f", func() { a.ed.filter.GrabFocus() })
	add("<Control><Shift>a", a.showSearch)
	add("<Control><Shift>j", a.chooseJars)
	a.win.AddController(sc)

	// Used by the "Add Mods" drop-down menu.
	addJars := gio.NewSimpleAction("add-jars", nil)
	addJars.ConnectActivate(func(*glibv2.Variant) {
		if a.doc != nil {
			a.chooseJars()
		}
	})
	a.win.AddAction(addJars)
}

func (a *App) markDirty() {
	if a.doc == nil {
		return
	}
	a.doc.Dirty = true
	a.ed.title.SetTitle(a.doc.Title())
}

// refreshList rebuilds the grouped mod list.
func (a *App) refreshList() {
	if a.doc == nil || a.ed.list == nil {
		return
	}
	p := a.doc.Pack
	for c := a.ed.list.FirstChild(); c != nil; c = a.ed.list.FirstChild() {
		a.ed.list.Remove(c)
	}
	counts := p.SideCounts()
	a.ed.title.SetTitle(a.doc.Title())
	a.ed.title.SetSubtitle(fmt.Sprintf("Minecraft %s · %s %s · %d mods", p.Minecraft, model.LoaderLabel(p.Loader), p.LoaderVersion, len(p.Mods)))
	a.ed.selLabel.SetText(fmt.Sprintf("%d selected", len(a.ed.selected)))

	if len(p.Mods) == 0 && len(p.Groups) == 0 {
		st := adw.NewStatusPage()
		st.SetIconName("list-add-symbolic")
		st.SetTitle("No mods yet")
		st.SetDescription("Add mods from Modrinth or CurseForge, or drop .jar files here.")
		btns := gtk.NewBox(gtk.OrientationHorizontal, 12)
		btns.SetHAlign(gtk.AlignCenter)
		b := gtk.NewButtonWithLabel("Add Mods")
		b.AddCSSClass("suggested-action")
		b.AddCSSClass("pill")
		b.ConnectClicked(a.showSearch)
		j := gtk.NewButtonWithLabel("Add JAR Files…")
		j.AddCSSClass("pill")
		j.ConnectClicked(a.chooseJars)
		btns.Append(b)
		btns.Append(j)
		st.SetChild(btns)
		a.ed.list.Append(st)
		return
	}

	// Side summary chips double as quick filters.
	chips := gtk.NewBox(gtk.OrientationHorizontal, 6)
	for _, s := range []model.Side{model.SideBoth, model.SideClient, model.SideServer, model.SideUnknown} {
		s := s
		if counts[s] == 0 && s == model.SideUnknown {
			continue
		}
		b := gtk.NewButton()
		b.SetChild(sideBadge(string(s), fmt.Sprintf("%s  %d", s.Label(), counts[s])))
		b.AddCSSClass("flat")
		b.SetTooltipText("Show only " + strings.ToLower(s.Label()) + " mods")
		b.ConnectClicked(func() { a.ed.filter.SetText("side:" + string(s)) })
		chips.Append(b)
	}
	a.ed.list.Append(chips)

	f := core.ParseFilter(a.ed.filter.Text())
	shown := 0
	for _, sec := range p.Sections(func(m *model.Mod) bool { return f.Match(p, m) }) {
		if len(sec.Mods) == 0 && !f.Empty() {
			continue
		}
		grp := adw.NewPreferencesGroup()
		name := "Ungrouped"
		if sec.Group != nil {
			name = sec.Group.Name
			grp.SetHeaderSuffix(a.groupMenu(sec.Group))
		}
		grp.SetTitle(markup.Escape(markup.Pango, name))
		grp.SetDescription(fmt.Sprintf("%d mod%s", len(sec.Mods), plural(len(sec.Mods))))
		if len(sec.Mods) == 0 {
			empty := adw.NewActionRow()
			empty.SetTitle("Empty group")
			empty.SetSubtitle("Select mods and use “Move to Group”, or pick this group when adding mods.")
			grp.Add(empty)
		}
		for _, m := range sec.Mods {
			grp.Add(a.modRow(m, f.Terms))
			shown++
		}
		a.ed.list.Append(grp)
	}
	if shown == 0 && !f.Empty() {
		st := adw.NewStatusPage()
		st.SetIconName("edit-find-symbolic")
		st.SetTitle("No matching mods")
		st.SetDescription("Filters: side:client|server|both|unknown, source:modrinth|curseforge|local, group:name, type:mod|resourcepack|shader")
		a.ed.list.Append(st)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

var sideOrder = []model.Side{model.SideBoth, model.SideClient, model.SideServer, model.SideUnknown}

func (a *App) modRow(m *model.Mod, terms []string) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetUseMarkup(true)
	row.SetTitle(markup.Highlight(m.DisplayName(), terms, markup.Pango))
	var sub string
	if len(terms) > 0 && m.Summary != "" {
		plain := markup.MarkdownToPlain(m.Summary)
		sub = markup.Highlight(markup.Snippet(plain, terms, 140), terms, markup.Pango)
	} else {
		sub = markup.InlineMarkdown(m.Summary, markup.Pango)
	}
	if sub == "" {
		sub = markup.Escape(markup.Pango, m.File.Name())
	}
	row.SetSubtitle(sub)
	row.SetSubtitleLines(1)
	row.SetTitleLines(1)

	if a.ed.selecting {
		cb := gtk.NewCheckButton()
		cb.SetActive(a.ed.selected[m.ID])
		cb.SetVAlign(gtk.AlignCenter)
		cb.ConnectToggled(func() {
			if cb.Active() {
				a.ed.selected[m.ID] = true
			} else {
				delete(a.ed.selected, m.ID)
			}
			a.ed.selLabel.SetText(fmt.Sprintf("%d selected", len(a.ed.selected)))
		})
		row.AddPrefix(cb)
		row.SetActivatableWidget(cb)
	} else {
		if m.IconURL == "" && m.File.Stored() {
			row.AddPrefix(a.jarIconWidget(m, 32))
		} else {
			row.AddPrefix(a.iconWidget(a.ed.ctx, m.IconURL, 32, "application-x-addon-symbolic"))
		}
		row.SetActivatable(true)
		row.ConnectActivated(func() { a.showModDetails(m) })
	}

	src := gtk.NewLabel(m.SourceLabel())
	src.AddCSSClass("mpm-source")
	row.AddSuffix(src)

	// Side selector; picking one locks it against metadata refreshes.
	dd := gtk.NewDropDownFromStrings([]string{"Client & server", "Client only", "Server only", "Unknown"})
	dd.SetVAlign(gtk.AlignCenter)
	dd.SetTooltipText("Where this mod is installed")
	for i, s := range sideOrder {
		if s == m.Side || (m.Side == "" && s == model.SideUnknown) {
			dd.SetSelected(uint(i))
		}
	}
	dd.NotifyProperty("selected", func() {
		ns := sideOrder[dd.Selected()]
		if ns != m.Side {
			m.Side, m.SideLocked = ns, true
			a.markDirty()
		}
	})
	row.AddSuffix(dd)

	if !a.ed.selecting {
		mb := gtk.NewMenuButton()
		mb.SetIconName("view-more-symbolic")
		mb.AddCSSClass("flat")
		mb.SetVAlign(gtk.AlignCenter)
		mb.SetTooltipText("More")
		mb.SetPopover(a.modMenu(m))
		row.AddSuffix(mb)
	}
	return row
}

func (a *App) modMenu(m *model.Mod) *gtk.Popover {
	pop := gtk.NewPopover()
	box := gtk.NewBox(gtk.OrientationVertical, 2)
	item := func(label string, f func()) {
		b := gtk.NewButtonWithLabel(label)
		b.AddCSSClass("flat")
		if l, ok := b.Child().(*gtk.Label); ok {
			l.SetXAlign(0)
		}
		b.ConnectClicked(func() { pop.Popdown(); f() })
		box.Append(b)
	}
	item("Details", func() { a.showModDetails(m) })
	if u := m.WebURL(); u != "" {
		item("Open Project Page", func() { a.openURI(u) })
	}
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	hdr := gtk.NewLabel("Move to group")
	hdr.AddCSSClass("dim-label")
	hdr.SetXAlign(0)
	hdr.SetMarginStart(8)
	box.Append(hdr)
	move := func(gid string) {
		if m.GroupID != gid {
			m.GroupID = gid
			a.markDirty()
			a.refreshList()
		}
	}
	for _, g := range a.doc.Pack.Groups {
		g := g
		mark := ""
		if g.ID == m.GroupID {
			mark = "✓ "
		}
		item(mark+g.Name, func() { move(g.ID) })
	}
	if m.GroupID != "" {
		item("Ungrouped", func() { move("") })
	}
	item("New Group…", func() {
		a.promptText("New group", "e.g. Performance", "", "Create", func(name string) {
			g := a.doc.Pack.AddGroup(name)
			move(g.ID)
		})
	})
	box.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	rm := gtk.NewButtonWithLabel("Remove from Pack")
	rm.AddCSSClass("flat")
	rm.AddCSSClass("error")
	rm.ConnectClicked(func() {
		pop.Popdown()
		a.doc.Pack.RemoveMod(m.ID)
		a.markDirty()
		a.refreshList()
		a.undoToast(fmt.Sprintf("Removed %s", m.DisplayName()), func() { a.doc.Pack.Mods = append(a.doc.Pack.Mods, m) })
	})
	box.Append(rm)
	pop.SetChild(box)
	return pop
}

// undoToast shows a toast with an Undo button.
func (a *App) undoToast(msg string, undo func()) {
	t := adw.NewToast(msg)
	t.SetButtonLabel("Undo")
	t.SetTimeout(6)
	t.ConnectButtonClicked(func() {
		undo()
		a.markDirty()
		a.refreshList()
	})
	a.toasts.AddToast(t)
}

func (a *App) groupMenu(g *model.Group) gtk.Widgetter {
	mb := gtk.NewMenuButton()
	mb.SetIconName("view-more-symbolic")
	mb.AddCSSClass("flat")
	mb.SetTooltipText("Group options")
	pop := gtk.NewPopover()
	box := gtk.NewBox(gtk.OrientationVertical, 2)
	item := func(label string, f func()) {
		b := gtk.NewButtonWithLabel(label)
		b.AddCSSClass("flat")
		if l, ok := b.Child().(*gtk.Label); ok {
			l.SetXAlign(0)
		}
		b.ConnectClicked(func() { pop.Popdown(); f() })
		box.Append(b)
	}
	item("Rename…", func() {
		a.promptText("Rename group", "Group name", g.Name, "Rename", func(name string) {
			g.Name = name
			a.markDirty()
			a.refreshList()
		})
	})
	item("Move Up", func() { a.doc.Pack.MoveGroup(g.ID, -1); a.markDirty(); a.refreshList() })
	item("Move Down", func() { a.doc.Pack.MoveGroup(g.ID, 1); a.markDirty(); a.refreshList() })
	item("Filter to This Group", func() { a.ed.filter.SetText(`group:"` + g.Name + `"`) })
	del := gtk.NewButtonWithLabel("Delete Group")
	del.AddCSSClass("flat")
	del.AddCSSClass("error")
	del.ConnectClicked(func() {
		pop.Popdown()
		var members []*model.Mod
		for _, m := range a.doc.Pack.Mods {
			if m.GroupID == g.ID {
				members = append(members, m)
			}
		}
		idx := 0
		for i, x := range a.doc.Pack.Groups {
			if x == g {
				idx = i
			}
		}
		a.doc.Pack.RemoveGroup(g.ID)
		a.markDirty()
		a.refreshList()
		a.undoToast(fmt.Sprintf("Deleted group %s (mods kept)", g.Name), func() {
			gs := a.doc.Pack.Groups
			gs = append(gs[:idx], append([]*model.Group{g}, gs[idx:]...)...)
			a.doc.Pack.Groups = gs
			for _, m := range members {
				m.GroupID = g.ID
			}
		})
	})
	box.Append(del)
	pop.SetChild(box)
	mb.SetPopover(pop)
	return mb
}

func (a *App) sidePopover(f func(model.Side)) *gtk.Popover {
	pop := gtk.NewPopover()
	box := gtk.NewBox(gtk.OrientationVertical, 2)
	for _, s := range sideOrder {
		s := s
		b := gtk.NewButtonWithLabel(s.Label())
		b.AddCSSClass("flat")
		b.ConnectClicked(func() { pop.Popdown(); f(s) })
		box.Append(b)
	}
	pop.SetChild(box)
	return pop
}

func (a *App) selectedMods() []*model.Mod {
	var out []*model.Mod
	for _, m := range a.doc.Pack.Mods {
		if a.ed.selected[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

func (a *App) bulkSetSide(s model.Side) {
	ms := a.selectedMods()
	for _, m := range ms {
		m.Side, m.SideLocked = s, true
	}
	if len(ms) > 0 {
		a.markDirty()
		a.refreshList()
		a.toast("Set %d mod%s to %s", len(ms), plural(len(ms)), strings.ToLower(s.Label()))
	}
}

func (a *App) bulkRemove() {
	ms := a.selectedMods()
	if len(ms) == 0 {
		return
	}
	for _, m := range ms {
		a.doc.Pack.RemoveMod(m.ID)
	}
	a.ed.selected = map[string]bool{}
	a.markDirty()
	a.refreshList()
	a.undoToast(fmt.Sprintf("Removed %d mod%s", len(ms), plural(len(ms))), func() { a.doc.Pack.Mods = append(a.doc.Pack.Mods, ms...) })
}

func (a *App) bulkMoveToGroup() {
	ms := a.selectedMods()
	if len(ms) == 0 {
		a.toast("Select some mods first")
		return
	}
	d := adw.NewAlertDialog("Move to Group", fmt.Sprintf("Move %d mod%s to:", len(ms), plural(len(ms))))
	names := []string{"Ungrouped"}
	ids := []string{""}
	for _, g := range a.doc.Pack.Groups {
		names = append(names, g.Name)
		ids = append(ids, g.ID)
	}
	names = append(names, "New group…")
	dd := gtk.NewDropDownFromStrings(names)
	entry := gtk.NewEntry()
	entry.SetPlaceholderText("New group name")
	entry.SetActivatesDefault(true)
	if len(a.doc.Pack.Groups) > 0 {
		dd.SetSelected(1)
		entry.SetVisible(false)
	} else {
		dd.SetSelected(uint(len(names) - 1)) // no groups yet: create one
	}
	dd.NotifyProperty("selected", func() { entry.SetVisible(int(dd.Selected()) == len(names)-1) })
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.Append(dd)
	box.Append(entry)
	d.SetExtraChild(box)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("move", "Move")
	d.SetResponseAppearance("move", adw.ResponseSuggested)
	d.SetDefaultResponse("move")
	d.ConnectResponse(func(r string) {
		if r != "move" {
			return
		}
		i := int(dd.Selected())
		gid := ""
		if i == len(names)-1 {
			name := strings.TrimSpace(entry.Text())
			if name == "" {
				return
			}
			gid = a.doc.Pack.AddGroup(name).ID
		} else {
			gid = ids[i]
		}
		for _, m := range ms {
			m.GroupID = gid
		}
		a.ed.selected = map[string]bool{}
		a.ed.selectBtn.SetActive(false)
		a.markDirty()
		a.refreshList()
		a.toast("Moved %d mod%s to %s", len(ms), plural(len(ms)), a.doc.Pack.GroupName(gid))
	})
	d.Present(a.win)
}

func (a *App) promptText(title, placeholder, initial, action string, ok func(string)) {
	d := adw.NewAlertDialog(title, "")
	entry := gtk.NewEntry()
	entry.SetPlaceholderText(placeholder)
	entry.SetText(initial)
	entry.SetActivatesDefault(true)
	d.SetExtraChild(entry)
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("ok", action)
	d.SetResponseAppearance("ok", adw.ResponseSuggested)
	d.SetDefaultResponse("ok")
	d.ConnectResponse(func(r string) {
		if r == "ok" && strings.TrimSpace(entry.Text()) != "" {
			ok(strings.TrimSpace(entry.Text()))
		}
	})
	d.Present(a.win)
	entry.GrabFocus()
}

func (a *App) newGroup() {
	a.promptText("New group", "e.g. Performance", "", "Create", func(name string) {
		a.doc.Pack.AddGroup(name)
		a.markDirty()
		a.refreshList()
	})
}

func (a *App) showModDetails(m *model.Mod) {
	d := adw.NewDialog()
	d.SetTitle(m.DisplayName())
	d.SetContentWidth(720)
	d.SetContentHeight(720)
	tv := adw.NewToolbarView()
	tv.AddTopBar(adw.NewHeaderBar())
	box := gtk.NewBox(gtk.OrientationVertical, 12)
	ctx, cancel := context.WithCancel(context.Background())
	d.ConnectClosed(cancel)

	// Installed-file facts first, then the project description.
	facts := adw.NewPreferencesGroup()
	facts.SetMarginTop(12)
	facts.SetMarginStart(18)
	facts.SetMarginEnd(18)
	fact := func(k, v string) {
		if v == "" {
			return
		}
		r := adw.NewActionRow()
		r.SetTitle(k)
		r.SetSubtitle(markup.Escape(markup.Pango, v))
		r.SetSubtitleSelectable(true)
		facts.Add(r)
	}
	fact("File", m.File.Path)
	if m.File.Stored() {
		fact("Stored in this pack", core.FormatSize(int64(len(m.File.Data))))
	}
	if m.Local != nil && m.PrimarySource() == model.SourceLocal {
		fact("Version", m.Local.Version)
		fact("Mod ID", m.Local.ModID)
		fact("Made for", core.LoaderLabels(m.Local.Loaders))
	}
	if m.Modrinth != nil {
		fact("Modrinth version", m.Modrinth.VersionNumber)
	}
	if m.CurseForge != nil {
		fact("CurseForge IDs", fmt.Sprintf("project %d, file %d", m.CurseForge.ProjectID, m.CurseForge.FileID))
	}
	sideNote := m.Side.Label()
	if m.SideLocked {
		sideNote += " (set by you)"
	}
	fact("Side", sideNote)
	notes := adw.NewEntryRow()
	notes.SetTitle("Notes")
	notes.SetText(m.Notes)
	notes.ConnectChanged(func() {
		if notes.Text() != m.Notes {
			m.Notes = notes.Text()
			a.markDirty()
		}
	})
	facts.Add(notes)
	box.Append(facts)

	if h := core.HitFromMod(m); h != nil {
		box.Append(a.detailsWidget(ctx, h, m, nil, true, nil))
	} else if m.Summary != "" {
		// A jar from disk has no project page; show what its metadata says.
		desc := gtk.NewLabel(m.Summary)
		desc.SetWrap(true)
		desc.SetXAlign(0)
		desc.SetSelectable(true)
		desc.SetMarginStart(18)
		desc.SetMarginEnd(18)
		desc.SetMarginBottom(18)
		box.Append(desc)
	}
	sc := gtk.NewScrolledWindow()
	sc.SetChild(box)
	sc.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	sc.SetVExpand(true)
	tv.SetContent(sc)
	d.SetChild(tv)
	d.Present(a.win)
}

// ---------------------------------------------------------------------------
// Saving

func (a *App) save() {
	if a.doc.Path != "" {
		a.saveTo(a.doc.Path, nil)
		return
	}
	a.saveAs()
}

func (a *App) saveAs() { a.saveAsThen(nil) }

func (a *App) saveAsThen(after func()) {
	d := gtk.NewFileDialog()
	d.SetTitle("Save Modpack")
	d.SetInitialName(filepath.Base(a.doc.SuggestedSavePath()))
	if dir := filepath.Dir(a.doc.SuggestedSavePath()); dir != "." && dir != "" {
		d.SetInitialFolder(gio.NewFileForPath(dir))
	}
	f := gtk.NewFileFilter()
	f.SetName("Modpack Manager (.mpm)")
	f.AddPattern("*.mpm")
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(f.Object)
	d.SetFilters(filters)
	d.Save(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		file, err := d.SaveFinish(res)
		if err != nil || file == nil {
			return
		}
		a.saveTo(file.Path(), after)
	})
}

func (a *App) saveTo(path string, after func()) {
	if a.doc.Path != "" && filepath.Clean(path) == filepath.Clean(a.doc.Path) && a.doc.ChangedOnDisk() {
		a.confirmOverwrite(path, after)
		return
	}
	a.writeDoc(path, after)
}

func (a *App) writeDoc(path string, after func()) {
	if err := a.doc.Save(path); err != nil {
		a.error("Couldn't save", err.Error())
		return
	}
	a.cfg.AddRecent(a.doc.Path)
	_ = a.cfg.Save()
	a.refreshList()
	a.toast("Saved %s", filepath.Base(a.doc.Path))
	if after != nil {
		after()
	}
}

// confirmDiscard runs next immediately, or after asking about unsaved changes.
func (a *App) confirmDiscard(next func()) {
	if a.doc == nil || !a.doc.Dirty {
		next()
		return
	}
	d := adw.NewAlertDialog("Save changes?", fmt.Sprintf("“%s” has unsaved changes.", a.doc.Pack.Name))
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("discard", "Discard")
	d.AddResponse("save", "Save")
	d.SetResponseAppearance("discard", adw.ResponseDestructive)
	d.SetResponseAppearance("save", adw.ResponseSuggested)
	d.SetDefaultResponse("save")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		switch r {
		case "discard":
			a.doc.Dirty = false
			next()
		case "save":
			if a.doc.Path != "" {
				a.saveTo(a.doc.Path, next)
			} else {
				a.saveAsThen(next)
			}
		}
	})
	d.Present(a.win)
}

// ---------------------------------------------------------------------------
// Export

func (a *App) showExport(t core.Target) {
	opt := core.DefaultExportOptions(t)
	p := a.doc.Pack
	d := adw.NewDialog()
	d.SetTitle("Export " + t.Label())
	d.SetContentWidth(520)
	tv := adw.NewToolbarView()
	tv.AddTopBar(adw.NewHeaderBar())
	page := gtk.NewBox(gtk.OrientationVertical, 18)
	page.SetMarginTop(12)
	page.SetMarginBottom(18)
	page.SetMarginStart(18)
	page.SetMarginEnd(18)

	summary := gtk.NewLabel("")
	summary.SetWrap(true)
	summary.SetXAlign(0)
	page.Append(summary)

	grp := adw.NewPreferencesGroup()
	formats := []core.Format{core.FormatMrpack, core.FormatCurseForge, core.FormatZip}
	fmtRow := adw.NewComboRow()
	fmtRow.SetTitle("Format")
	fmtRow.SetModel(gtk.NewStringList([]string{core.FormatMrpack.Label(), core.FormatCurseForge.Label(), core.FormatZip.Label()}))
	for i, f := range formats {
		if f == opt.Format {
			fmtRow.SetSelected(uint(i))
		}
	}
	grp.Add(fmtRow)
	common := adw.NewSwitchRow()
	common.SetTitle("Include common mods")
	common.SetSubtitle("Adds the client-and-server mods so the export installs on its own")
	common.SetActive(opt.IncludeCommon)
	if t == core.TargetClient || t == core.TargetServer {
		grp.Add(common)
	}
	embed := adw.NewSwitchRow()
	embed.SetTitle("Bundle non-Modrinth jars")
	embed.SetSubtitle("Needed for modrinth.com uploads. Check the mods' licenses before sharing.")
	grp.Add(embed)
	page.Append(grp)

	update := func() {
		opt.Format = formats[fmtRow.Selected()]
		opt.IncludeCommon = common.Active()
		opt.EmbedNonModrinth = embed.Active()
		embed.SetVisible(opt.Format == core.FormatMrpack)
		ms := core.SelectMods(p, t, opt.IncludeCommon)
		os := core.SelectOverrides(p, t, opt.IncludeCommon)
		unknown := 0
		for _, m := range ms {
			if m.Side == model.SideUnknown || m.Side == "" {
				unknown++
			}
		}
		txt := fmt.Sprintf("<b>%d mods</b> and %d override files will be exported.", len(ms), len(os))
		if n := core.CountStored(ms); n > 0 {
			txt += fmt.Sprintf(" %d JAR file%s stored in this pack will be bundled.", n, plural(n))
		}
		if opt.Format == core.FormatZip && core.CountStored(ms) < len(ms) {
			txt += " Jars are downloaded and checked against their hashes."
		}
		if unknown > 0 {
			txt += fmt.Sprintf("\n%d of them have no side information and are treated as common.", unknown)
		}
		summary.SetMarkup(txt)
	}
	fmtRow.NotifyProperty("selected", update)
	common.NotifyProperty("active", update)
	embed.NotifyProperty("active", update)
	update()

	progress := gtk.NewProgressBar()
	progress.SetShowText(true)
	progress.SetVisible(false)
	page.Append(progress)

	btn := gtk.NewButtonWithLabel("Export…")
	btn.AddCSSClass("suggested-action")
	btn.AddCSSClass("pill")
	btn.SetHAlign(gtk.AlignCenter)
	page.Append(btn)
	btn.ConnectClicked(func() {
		fd := gtk.NewFileDialog()
		fd.SetTitle("Export " + t.Label())
		fd.SetInitialName(core.SuggestedFileName(p, opt))
		fd.Save(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
			file, err := fd.SaveFinish(res)
			if err != nil || file == nil {
				return
			}
			out := file.Path()
			btn.SetSensitive(false)
			progress.SetVisible(true)
			ctx, cancel := context.WithCancel(context.Background())
			d.ConnectClosed(cancel)
			o := opt
			go func() {
				data, rep, err := a.svc.Export(ctx, p, o, nil, func(stage string, done, total int) {
					ui(func() {
						progress.SetText(stage)
						if total > 0 {
							progress.SetFraction(float64(done) / float64(total))
						}
					})
				})
				if err == nil {
					err = os.WriteFile(out, data, 0o644)
				}
				ui(func() {
					btn.SetSensitive(true)
					progress.SetVisible(false)
					if err != nil {
						if ctx.Err() == nil {
							a.error("Export failed", err.Error())
						}
						return
					}
					d.Close()
					msg := fmt.Sprintf("Exported %d mods to %s.", rep.Mods, filepath.Base(out))
					var details []string
					if len(rep.Skipped) > 0 {
						details = append(details, fmt.Sprintf("%d mods couldn't be downloaded and were left out:\n• %s", len(rep.Skipped), strings.Join(rep.Skipped, "\n• ")))
					}
					if len(rep.Warnings) > 0 {
						details = append(details, "• "+strings.Join(rep.Warnings, "\n• "))
					}
					if len(details) == 0 {
						a.toast("%s", msg)
						return
					}
					a.error(msg, strings.Join(details, "\n\n"))
				})
			}()
		})
	})
	tv.SetContent(page)
	d.SetChild(tv)
	d.Present(a.win)
}

// ---------------------------------------------------------------------------
// Pack settings

func (a *App) showPackSettings() {
	p := a.doc.Pack
	d := adw.NewDialog()
	d.SetTitle("Pack Settings")
	d.SetContentWidth(520)
	tv := adw.NewToolbarView()
	tv.AddTopBar(adw.NewHeaderBar())
	page := adw.NewPreferencesPage()

	info := adw.NewPreferencesGroup()
	info.SetTitle("Info")
	entry := func(title, val string, set func(string)) *adw.EntryRow {
		r := adw.NewEntryRow()
		r.SetTitle(title)
		r.SetText(val)
		r.ConnectChanged(func() {
			set(r.Text())
			a.markDirty()
		})
		info.Add(r)
		return r
	}
	entry("Name", p.Name, func(s string) { p.Name = s })
	entry("Version", p.Version, func(s string) { p.Version = s })
	entry("Author", p.Author, func(s string) { p.Author = s })
	entry("Summary", p.Summary, func(s string) { p.Summary = s })
	page.Add(info)

	game := adw.NewPreferencesGroup()
	game.SetTitle("Game")
	game.SetDescription("Search results and new mods are filtered to this Minecraft version and loader.")
	mc := adw.NewEntryRow()
	mc.SetTitle("Minecraft version")
	mc.SetText(p.Minecraft)
	mc.ConnectChanged(func() { p.Minecraft = strings.TrimSpace(mc.Text()); a.markDirty() })
	game.Add(mc)

	loaderRow := adw.NewComboRow()
	loaderRow.SetTitle("Mod loader")
	loaderRow.SetModel(gtk.NewStringList([]string{"Fabric", "Forge", "NeoForge", "Quilt"}))
	for i, l := range model.Loaders {
		if l == p.Loader {
			loaderRow.SetSelected(uint(i))
		}
	}
	game.Add(loaderRow)
	lv := adw.NewEntryRow()
	lv.SetTitle("Loader version")
	lv.SetText(p.LoaderVersion)
	lv.ConnectChanged(func() { p.LoaderVersion = strings.TrimSpace(lv.Text()); a.markDirty() })
	latest := gtk.NewButtonWithLabel("Latest")
	latest.SetVAlign(gtk.AlignCenter)
	latest.AddCSSClass("flat")
	latest.SetTooltipText("Fetch the newest stable loader version for this Minecraft version")
	latest.ConnectClicked(func() {
		latest.SetSensitive(false)
		loader, ver := p.Loader, p.Minecraft
		go func() {
			vs, err := core.LoaderVersions(context.Background(), loader, ver)
			ui(func() {
				latest.SetSensitive(true)
				if err != nil || len(vs) == 0 {
					a.toast("No %s version found for Minecraft %s", model.LoaderLabel(loader), ver)
					return
				}
				lv.SetText(vs[0])
			})
		}()
	})
	lv.AddSuffix(latest)
	game.Add(lv)
	loaderRow.NotifyProperty("selected", func() {
		nl := model.Loaders[loaderRow.Selected()]
		if nl != p.Loader {
			p.Loader = nl
			lv.SetText("")
			a.markDirty()
		}
	})
	page.Add(game)

	tv.SetContent(page)
	d.SetChild(tv)
	d.ConnectClosed(a.refreshList)
	d.Present(a.win)
}

func (a *App) showPreferences() {
	d := adw.NewPreferencesDialog()
	page := adw.NewPreferencesPage()
	g := adw.NewPreferencesGroup()
	g.SetTitle("CurseForge")
	g.SetDescription("CurseForge's API needs a key. Get a free one at console.curseforge.com. It's stored only on this computer.")
	key := adw.NewPasswordEntryRow()
	key.SetTitle("API key")
	key.SetText(a.cfg.CurseForgeAPIKey)
	key.ConnectChanged(func() {
		a.cfg.CurseForgeAPIKey = strings.TrimSpace(key.Text())
		_ = a.cfg.Save()
		a.svc.SetCurseForgeKey(a.cfg.APIKey())
	})
	g.Add(key)
	page.Add(g)
	d.Add(page)
	d.Present(a.win)
}

func (a *App) showAbout() {
	ab := adw.NewAboutDialog()
	ab.SetApplicationName("Modpack Manager")
	ab.SetApplicationIcon("package-x-generic")
	ab.SetVersion(version.String())
	ab.SetComments("Edit Minecraft modpacks from Modrinth and CurseForge.")
	ab.Present(a.win)
}

// ---------------------------------------------------------------------------
// Adding jar files

func (a *App) chooseJars() {
	if a.doc == nil {
		return
	}
	d := gtk.NewFileDialog()
	d.SetTitle("Add JAR Files")
	d.SetAcceptLabel("Add")
	f := gtk.NewFileFilter()
	f.SetName("Mod files (.jar)")
	f.AddSuffix("jar")
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(f.Object)
	d.SetFilters(filters)
	if a.cfg.LastDir != "" {
		d.SetInitialFolder(gio.NewFileForPath(a.cfg.LastDir))
	}
	d.OpenMultiple(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		files, err := d.OpenMultipleFinish(res)
		if err != nil || files == nil {
			return // cancelled
		}
		var paths []string
		for i := uint(0); i < files.NItems(); i++ {
			if obj := files.Item(i); obj != nil {
				if p := (&gio.File{Object: obj}).Path(); p != "" {
					paths = append(paths, p)
				}
			}
		}
		if len(paths) > 0 {
			a.cfg.LastDir = filepath.Dir(paths[0])
			_ = a.cfg.Save()
		}
		a.addJarPaths(paths)
	})
}

// addJarPaths reads and looks up the jars off the main thread, then adds them.
func (a *App) addJarPaths(paths []string) {
	doc := a.doc
	if doc == nil || len(paths) == 0 {
		return
	}
	a.ed.busy.SetVisible(true)
	a.ed.busy.Start()
	ctx := a.ed.ctx
	go func() {
		prep := a.svc.PrepareJars(ctx, core.ReadJarFiles(paths), core.JarOptions{})
		ui(func() {
			if a.doc != doc {
				return // the pack was closed meanwhile
			}
			a.ed.busy.Stop()
			a.ed.busy.SetVisible(false)
			results := prep.Apply(doc.Pack)
			if core.JarsChanged(results) {
				a.markDirty()
				a.refreshList()
			}
			title, problems := core.JarSummary(results, prep.Report)
			if len(problems) == 0 {
				a.toast("%s", title)
				return
			}
			a.error(title, "• "+strings.Join(problems, "\n• "))
		})
	}()
}

// jarIconWidget shows the icon inside a stored jar, if it has a usable one.
func (a *App) jarIconWidget(m *model.Mod, size int) *gtk.Image {
	img := gtk.NewImageFromIconName("application-x-addon-symbolic")
	img.SetPixelSize(size)
	img.AddCSSClass("mpm-icon")
	key := strings.ToLower(m.File.SHA1)
	if tex, ok := a.jarIcons[key]; ok {
		if tex != nil {
			img.SetFromPaintable(tex)
		}
		return img
	}
	data := m.File.Data
	go func() {
		png := core.JarIcon(data, size*2)
		ui(func() {
			var tex *gdk.Texture
			if png != nil {
				tex, _ = gdk.NewTextureFromBytes(glibv2.NewBytesWithGo(png))
			}
			a.jarIcons[key] = tex // nil: no icon, don't look again
			if tex != nil {
				img.SetFromPaintable(tex)
			}
		})
	}()
	return img
}
