package qtui

import (
	"context"
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"modpackmanager/internal/core"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

const (
	roleModID   = int(qt.UserRole)
	roleGroupID = int(qt.UserRole) + 1
	colName     = 0
	colSide     = 1
	colSource   = 2
	colSummary  = 3
)

func (a *App) buildEditor() *qt.QWidget {
	w := qt.NewQWidget2()
	v := qt.NewQVBoxLayout2()
	v.SetContentsMargins(8, 8, 8, 8)

	top := qt.NewQHBoxLayout2()
	a.filter = qt.NewQLineEdit2()
	a.filter.SetPlaceholderText(`Filter mods…  (side:client  group:"World gen"  source:cf)`)
	a.filter.SetClearButtonEnabled(true)
	a.filter.OnTextChanged(func(string) { a.refresh() })
	top.AddWidget(a.filter.QWidget)
	a.summary = qt.NewQLabel2()
	top.AddWidget(a.summary.QWidget)
	v.AddLayout(top.QLayout)

	a.tree = qt.NewQTreeWidget2()
	a.tree.SetHeaderLabels([]string{"Name", "Side", "Source", "Description"})
	a.tree.SetSelectionMode(qt.QAbstractItemView__ExtendedSelection)
	a.tree.SetIconSize(qt.NewQSize2(24, 24))
	a.tree.SetAlternatingRowColors(true)
	a.tree.SetUniformRowHeights(true)
	a.tree.SetContextMenuPolicy(qt.CustomContextMenu)
	hdr := a.tree.Header()
	hdr.SetSectionResizeMode2(colName, qt.QHeaderView__ResizeToContents)
	hdr.SetSectionResizeMode2(colSide, qt.QHeaderView__ResizeToContents)
	hdr.SetSectionResizeMode2(colSource, qt.QHeaderView__ResizeToContents)
	hdr.SetStretchLastSection(true)
	a.tree.OnCustomContextMenuRequested(func(pos *qt.QPoint) { a.contextMenu(pos) })
	a.tree.OnItemDoubleClicked(func(it *qt.QTreeWidgetItem, _ int) {
		if m := a.modOf(it); m != nil {
			a.showModDetails(m)
		}
	})
	a.tree.OnItemExpanded(func(it *qt.QTreeWidgetItem) { a.setCollapsed(it, false) })
	a.tree.OnItemCollapsed(func(it *qt.QTreeWidgetItem) { a.setCollapsed(it, true) })
	v.AddWidget(a.tree.QWidget)

	hint := qt.NewQLabel3("Tip: select several mods (Ctrl/Shift-click) and right-click to group them, set their side or remove them.")
	hint.SetStyleSheet("color: palette(placeholder-text)")
	v.AddWidget(hint.QWidget)
	w.SetLayout(v.QLayout)
	return w
}

func (a *App) setCollapsed(it *qt.QTreeWidgetItem, c bool) {
	if a.doc == nil {
		return
	}
	if g := a.doc.Pack.Group(it.Data(0, roleGroupID).ToString()); g != nil && g.Collapsed != c {
		g.Collapsed = c
	}
}

func (a *App) showEditor(doc *core.Document) {
	a.doc = doc
	a.filter.SetText("")
	for _, ac := range a.editActs {
		ac.SetEnabled(true)
	}
	a.stack.SetCurrentWidget(a.editorW)
	a.refresh()
}

func sideText(s model.Side) string {
	if s == "" {
		s = model.SideUnknown
	}
	return s.Label()
}

func sideColor(s model.Side) *qt.QBrush {
	switch s {
	case model.SideClient:
		return qt.NewQBrush3(qt.NewQColor3(0x3d, 0xae, 0xe9)) // Breeze blue
	case model.SideServer:
		return qt.NewQBrush3(qt.NewQColor3(0xf6, 0x74, 0x00)) // Breeze orange
	case model.SideBoth:
		return qt.NewQBrush3(qt.NewQColor3(0x27, 0xae, 0x60)) // Breeze green
	}
	return qt.NewQBrush3(qt.NewQColor3(0x7f, 0x8c, 0x8d))
}

// refresh rebuilds the tree from the pack.
func (a *App) refresh() {
	if a.doc == nil {
		return
	}
	a.gen++
	gen := a.gen
	p := a.doc.Pack
	a.updateTitle()
	c := p.SideCounts()
	a.summary.SetText(fmt.Sprintf("  Minecraft %s · %s %s · %d mods: <span style='color:#27ae60'>%d common</span>, <span style='color:#3daee9'>%d client</span>, <span style='color:#f67400'>%d server</span>",
		p.Minecraft, model.LoaderLabel(p.Loader), p.LoaderVersion, len(p.Mods), c[model.SideBoth]+c[model.SideUnknown], c[model.SideClient], c[model.SideServer]))

	a.tree.Clear()
	f := core.ParseFilter(a.filter.Text())
	bold := qt.NewQFont5(a.tree.Font())
	bold.SetBold(true)
	for _, sec := range p.Sections(func(m *model.Mod) bool { return f.Match(p, m) }) {
		if len(sec.Mods) == 0 && !f.Empty() {
			continue
		}
		name, gid := "Ungrouped", ""
		if sec.Group != nil {
			name, gid = sec.Group.Name, sec.Group.ID
		}
		gi := qt.NewQTreeWidgetItem2([]string{fmt.Sprintf("%s  (%d)", name, len(sec.Mods))})
		gi.SetFont(0, bold)
		gi.SetIcon(0, themeIcon("folder", "folder-open"))
		gi.SetData(0, roleGroupID, qt.NewQVariant14(gid))
		gi.SetFirstColumnSpanned(true)
		a.tree.AddTopLevelItem(gi)
		for _, m := range sec.Mods {
			mi := qt.NewQTreeWidgetItem2([]string{m.DisplayName(), sideText(m.Side), m.PrimarySource().Label(), markup.MarkdownToPlain(m.Summary)})
			mi.SetData(0, roleModID, qt.NewQVariant14(m.ID))
			mi.SetForeground(colSide, sideColor(m.Side))
			tip := m.File.Name()
			if m.SideLocked {
				tip += "\nSide set by you"
			}
			mi.SetToolTip(colName, tip)
			mi.SetToolTip(colSummary, markup.MarkdownToPlain(m.Summary))
			a.setIcon(gen, mi, m.IconURL)
			gi.AddChild(mi)
		}
		gi.SetExpanded(sec.Group == nil || !sec.Group.Collapsed || !f.Empty())
	}
}

// setIcon loads a mod icon in the background. Tree items are destroyed on
// every refresh, so the callback checks the generation first.
func (a *App) setIcon(gen int, it *qt.QTreeWidgetItem, url string) {
	if url == "" {
		it.SetIcon(0, themeIcon("application-x-addon", "package-x-generic"))
		return
	}
	if ic, ok := a.icons[url]; ok {
		it.SetIcon(0, ic)
		return
	}
	go func() {
		data, _, err := a.images.Load(context.Background(), url, 64)
		ui(func() {
			if err != nil {
				return
			}
			ic, ok := a.icons[url]
			if !ok {
				pm := qt.NewQPixmap()
				if !pm.LoadFromDataWithData(data) {
					return
				}
				ic = qt.NewQIcon2(pm)
				a.icons[url] = ic
			}
			if gen == a.gen {
				it.SetIcon(0, ic)
			}
		})
	}()
}

func (a *App) modOf(it *qt.QTreeWidgetItem) *model.Mod {
	if it == nil || a.doc == nil {
		return nil
	}
	return a.doc.Pack.ModByID(it.Data(0, roleModID).ToString())
}

func (a *App) selectedMods() []*model.Mod {
	var out []*model.Mod
	for _, it := range a.tree.SelectedItems() {
		if m := a.modOf(it); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (a *App) contextMenu(pos *qt.QPoint) {
	if a.doc == nil {
		return
	}
	it := a.tree.ItemAt(pos)
	menu := qt.NewQMenu(a.win.QWidget)
	global := a.tree.Viewport().MapToGlobalWithQPoint(pos)

	// Right-click on a group header.
	if it != nil && a.modOf(it) == nil {
		gid := it.Data(0, roleGroupID).ToString()
		if g := a.doc.Pack.Group(gid); g != nil {
			menu.AddAction2(themeIcon("edit-rename"), "Rename Group…").OnTriggered(func() { a.renameGroup(g) })
			menu.AddAction2(themeIcon("go-up"), "Move Up").OnTriggered(func() { a.doc.Pack.MoveGroup(g.ID, -1); a.markDirty(); a.refresh() })
			menu.AddAction2(themeIcon("go-down"), "Move Down").OnTriggered(func() { a.doc.Pack.MoveGroup(g.ID, 1); a.markDirty(); a.refresh() })
			menu.AddSeparator()
			menu.AddAction2(themeIcon("edit-delete"), "Delete Group (keep mods)").OnTriggered(func() {
				a.doc.Pack.RemoveGroup(g.ID)
				a.markDirty()
				a.refresh()
			})
		}
		menu.AddSeparator()
		menu.AddAction2(themeIcon("folder-new"), "New Group…").OnTriggered(a.newGroup)
		menu.Popup(global)
		return
	}

	mods := a.selectedMods()
	if len(mods) == 0 {
		menu.AddAction2(themeIcon("list-add"), "Add Mods…").OnTriggered(a.showSearch)
		menu.AddAction2(themeIcon("folder-new"), "New Group…").OnTriggered(a.newGroup)
		menu.Popup(global)
		return
	}
	label := mods[0].DisplayName()
	if len(mods) > 1 {
		label = fmt.Sprintf("%d mods", len(mods))
	}
	if len(mods) == 1 {
		m := mods[0]
		menu.AddAction2(themeIcon("documentinfo", "help-about"), "Details…").OnTriggered(func() { a.showModDetails(m) })
		if u := m.WebURL(); u != "" {
			menu.AddAction2(themeIcon("internet-web-browser", "applications-internet"), "Open Project Page").OnTriggered(func() {
				qt.QDesktopServices_OpenUrl(qt.NewQUrl3(u))
			})
		}
		menu.AddSeparator()
	}

	gm := menu.AddMenu2(themeIcon("folder"), "Move "+label+" to Group")
	for _, g := range a.doc.Pack.Groups {
		g := g
		gm.AddActionWithText(g.Name).OnTriggered(func() { a.moveTo(mods, g.ID) })
	}
	gm.AddActionWithText("Ungrouped").OnTriggered(func() { a.moveTo(mods, "") })
	gm.AddSeparator()
	gm.AddAction2(themeIcon("folder-new"), "New Group…").OnTriggered(func() {
		var ok bool
		name := qt.QInputDialog_GetText4(a.win.QWidget, "New Group", "Group name:", qt.QLineEdit__Normal, "", &ok)
		if ok && strings.TrimSpace(name) != "" {
			a.moveTo(mods, a.doc.Pack.AddGroup(name).ID)
		}
	})

	sm := menu.AddMenu2(themeIcon("network-server"), "Set Side of "+label)
	for _, s := range []model.Side{model.SideBoth, model.SideClient, model.SideServer, model.SideUnknown} {
		s := s
		ac := sm.AddActionWithText(s.Label())
		ac.SetCheckable(true)
		ac.SetChecked(len(mods) == 1 && mods[0].Side == s)
		ac.OnTriggered(func() {
			for _, m := range mods {
				m.Side, m.SideLocked = s, true
			}
			a.markDirty()
			a.refresh()
		})
	}
	menu.AddSeparator()
	menu.AddAction2(themeIcon("edit-delete", "list-remove"), "Remove "+label).OnTriggered(func() {
		if len(mods) > 1 && qt.QMessageBox_Question(a.win.QWidget, "Remove mods", fmt.Sprintf("Remove %d mods from the pack?", len(mods))) != qt.QMessageBox__Yes {
			return
		}
		for _, m := range mods {
			a.doc.Pack.RemoveMod(m.ID)
		}
		a.markDirty()
		a.refresh()
		a.status("Removed %s", label)
	})
	menu.Popup(global)
}

func (a *App) moveTo(mods []*model.Mod, gid string) {
	for _, m := range mods {
		m.GroupID = gid
	}
	a.markDirty()
	a.refresh()
	a.status("Moved %d mod%s to %s", len(mods), plural(len(mods)), a.doc.Pack.GroupName(gid))
}

func (a *App) newGroup() {
	var ok bool
	name := qt.QInputDialog_GetText4(a.win.QWidget, "New Group", "Group name:", qt.QLineEdit__Normal, "", &ok)
	if !ok || strings.TrimSpace(name) == "" {
		return
	}
	g := a.doc.Pack.AddGroup(name)
	// Put the current selection into the new group: the quickest way to group.
	if mods := a.selectedMods(); len(mods) > 0 {
		a.moveTo(mods, g.ID)
		return
	}
	a.markDirty()
	a.refresh()
}

func (a *App) renameGroup(g *model.Group) {
	var ok bool
	name := qt.QInputDialog_GetText4(a.win.QWidget, "Rename Group", "Group name:", qt.QLineEdit__Normal, g.Name, &ok)
	if ok && strings.TrimSpace(name) != "" {
		g.Name = strings.TrimSpace(name)
		a.markDirty()
		a.refresh()
	}
}
