// Package qtui is the Qt 6 frontend (KDE Plasma, Windows, macOS). It uses
// Qt's native style on each platform, so it looks like Breeze on Plasma.
package qtui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"

	"modpackmanager/internal/config"
	"modpackmanager/internal/core"
	"modpackmanager/internal/imgutil"
	"modpackmanager/internal/model"
)

// App is the main window and its state.
type App struct {
	win     *qt.QMainWindow
	stack   *qt.QStackedWidget
	welcome *qt.QWidget
	editorW *qt.QWidget
	cfg     *config.Config
	svc     *core.Service
	doc     *core.Document
	images  *imgutil.Loader
	icons   map[string]*qt.QIcon // url -> loaded icon (cache)

	// editor widgets
	toolbar  *qt.QToolBar
	tree     *qt.QTreeWidget
	filter   *qt.QLineEdit
	summary  *qt.QLabel
	gen      int // bumped on every refresh; async callbacks check it
	recentLW *qt.QListWidget
	editActs []*qt.QAction // disabled while no pack is open
}

// ui runs f on the Qt main thread (asynchronously).
func ui(f func()) { mainthread.Start(f) }

// Run starts the Qt event loop.
func Run(args []string) int {
	qt.NewQApplication(append([]string{os.Args[0]}, args...))
	qt.QCoreApplication_SetApplicationName("Modpack Manager")
	qt.QCoreApplication_SetOrganizationName("modpackmanager")
	qt.QGuiApplication_SetDesktopFileName("io.github.blakka.ModpackManager")

	a := &App{cfg: config.Load(), images: imgutil.NewLoader(), icons: map[string]*qt.QIcon{}}
	a.svc = core.New(a.cfg.APIKey())
	a.win = qt.NewQMainWindow2()
	a.win.SetWindowTitle("Modpack Manager")
	w, h := a.cfg.WindowWidth, a.cfg.WindowHeight
	if w < 600 || h < 400 {
		w, h = 1100, 720
	}
	a.win.Resize(w, h)
	a.win.SetWindowIcon(themeIcon("package-x-generic", "application-x-archive"))

	a.buildToolbar()
	a.stack = qt.NewQStackedWidget2()
	a.welcome = a.buildWelcome()
	a.editorW = a.buildEditor()
	a.stack.AddWidget(a.welcome)
	a.stack.AddWidget(a.editorW)
	a.win.SetCentralWidget(a.stack.QWidget)
	a.showWelcome()

	a.win.OnCloseEvent(func(super func(*qt.QCloseEvent), ev *qt.QCloseEvent) {
		a.cfg.WindowWidth, a.cfg.WindowHeight = a.win.Width(), a.win.Height()
		_ = a.cfg.Save()
		if !a.maybeSave() {
			ev.Ignore()
			return
		}
		super(ev)
	})
	a.win.Show()
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			a.openPath(arg)
			break
		}
	}
	return qt.QApplication_Exec()
}

// themeIcon returns the first available freedesktop icon (Breeze on KDE).
func themeIcon(names ...string) *qt.QIcon {
	for _, n := range names {
		if qt.QIcon_HasThemeIcon(n) {
			return qt.QIcon_FromTheme(n)
		}
	}
	return qt.NewQIcon()
}

func (a *App) status(format string, args ...any) {
	a.win.StatusBar().ShowMessage2(fmt.Sprintf(format, args...), 6000)
}

func (a *App) errorBox(title, text string) {
	qt.QMessageBox_Warning(a.win.QWidget, title, text)
}

func (a *App) buildToolbar() {
	tb := qt.NewQToolBar3()
	tb.SetMovable(false)
	tb.SetToolButtonStyle(qt.ToolButtonTextBesideIcon)
	a.toolbar = tb

	act := func(text, tip string, icon []string, key *qt.QKeySequence, f func(), editOnly bool) *qt.QAction {
		ac := qt.NewQAction3(themeIcon(icon...), text)
		ac.SetToolTip(tip)
		if key != nil {
			ac.SetShortcut(key)
		}
		ac.OnTriggered(f)
		tb.AddAction(ac)
		if editOnly {
			a.editActs = append(a.editActs, ac)
		}
		return ac
	}
	act("Open", "Open a .mrpack, CurseForge .zip or .mpm file", []string{"document-open"}, qt.NewQKeySequence6(qt.QKeySequence__Open), func() {
		if a.maybeSave() {
			a.chooseOpen()
		}
	}, false)
	act("Save", "Save as .mpm", []string{"document-save"}, qt.NewQKeySequence6(qt.QKeySequence__Save), func() { a.save() }, true)
	tb.AddSeparator()
	act("Add Mods", "Search Modrinth and CurseForge", []string{"list-add"}, qt.NewQKeySequence2("Ctrl+Shift+A"), a.showSearch, true)
	act("New Group", "Create a group", []string{"folder-new"}, nil, a.newGroup, true)
	act("Pack Settings", "Name, version, Minecraft version and loader", []string{"configure", "document-properties"}, nil, a.showPackSettings, true)
	tb.AddSeparator()
	lbl := qt.NewQLabel3(" Export: ")
	tb.AddWidget(lbl.QWidget)
	for _, t := range []core.Target{core.TargetClient, core.TargetCommon, core.TargetServer} {
		t := t
		tips := map[core.Target]string{
			core.TargetClient: "Export the client-only mods",
			core.TargetCommon: "Export the mods needed on both client and server",
			core.TargetServer: "Export a server pack (server-only + common mods by default)",
		}
		icons := map[core.Target][]string{
			core.TargetClient: {"computer", "video-display"},
			core.TargetCommon: {"emblem-shared", "folder-publicshare"},
			core.TargetServer: {"network-server", "server-database"},
		}
		act(t.Label(), tips[t], icons[t], nil, func() { a.showExport(t) }, true)
	}
	spacer := qt.NewQWidget2()
	spacer.SetSizePolicy2(qt.QSizePolicy__Expanding, qt.QSizePolicy__Preferred)
	tb.AddWidget(spacer)
	act("Preferences", "CurseForge API key", []string{"preferences-system", "configure"}, nil, a.showPreferences, false)
	a.win.AddToolBar(qt.TopToolBarArea, tb)

	find := qt.NewQAction5("Find", a.win.QObject)
	find.SetShortcut(qt.NewQKeySequence6(qt.QKeySequence__Find))
	find.OnTriggered(func() {
		if a.doc != nil {
			a.filter.SetFocus()
			a.filter.SelectAll()
		}
	})
	a.win.AddAction(find)
	saveAs := qt.NewQAction5("Save As", a.win.QObject)
	saveAs.SetShortcut(qt.NewQKeySequence6(qt.QKeySequence__SaveAs))
	saveAs.OnTriggered(func() {
		if a.doc != nil {
			a.saveAs()
		}
	})
	a.win.AddAction(saveAs)
}

func (a *App) buildWelcome() *qt.QWidget {
	w := qt.NewQWidget2()
	outer := qt.NewQVBoxLayout2()
	outer.AddStretch()

	title := qt.NewQLabel3("<h1>Modpack Manager</h1>")
	title.SetAlignment(qt.AlignCenter)
	outer.AddWidget(title.QWidget)
	sub := qt.NewQLabel3("Open a Modrinth (.mrpack), CurseForge (.zip) or Modpack Manager (.mpm) file, or start a new pack.")
	sub.SetAlignment(qt.AlignCenter)
	sub.SetWordWrap(true)
	outer.AddWidget(sub.QWidget)
	outer.AddSpacing(12)

	row := qt.NewQHBoxLayout2()
	row.AddStretch()
	open := qt.NewQPushButton3("Open Modpack…")
	open.SetIcon(themeIcon("document-open"))
	open.SetDefault(true)
	open.OnClicked(a.chooseOpen)
	row.AddWidget(open.QWidget)
	nb := qt.NewQPushButton3("New Modpack…")
	nb.SetIcon(themeIcon("document-new"))
	nb.OnClicked(a.newPack)
	row.AddWidget(nb.QWidget)
	row.AddStretch()
	outer.AddLayout(row.QLayout)
	outer.AddSpacing(18)

	rec := qt.NewQLabel3("<b>Recent</b>")
	rec.SetAlignment(qt.AlignCenter)
	outer.AddWidget(rec.QWidget)
	a.recentLW = qt.NewQListWidget2()
	a.recentLW.SetMaximumWidth(560)
	a.recentLW.SetMinimumWidth(420)
	a.recentLW.SetMaximumHeight(220)
	a.recentLW.OnItemActivated(func(it *qt.QListWidgetItem) {
		p := it.Data(int(qt.UserRole)).ToString()
		if p != "" {
			a.openPath(p)
		}
	})
	hl := qt.NewQHBoxLayout2()
	hl.AddStretch()
	hl.AddWidget(a.recentLW.QWidget)
	hl.AddStretch()
	outer.AddLayout(hl.QLayout)
	outer.AddStretch()
	w.SetLayout(outer.QLayout)
	return w
}

func (a *App) showWelcome() {
	a.doc = nil
	a.recentLW.Clear()
	for _, p := range a.cfg.Recent {
		it := qt.NewQListWidgetItem3(themeIcon("application-x-archive", "package-x-generic"), filepath.Base(p)+"   —   "+filepath.Dir(p))
		it.SetData(int(qt.UserRole), qt.NewQVariant14(p))
		it.SetToolTip(p)
		a.recentLW.AddItemWithItem(it)
	}
	a.recentLW.SetVisible(len(a.cfg.Recent) > 0)
	for _, ac := range a.editActs {
		ac.SetEnabled(false)
	}
	a.stack.SetCurrentWidget(a.welcome)
	a.win.SetWindowTitle("Modpack Manager")
}

const openFilter = "Modpacks (*.mrpack *.zip *.mpm);;Modrinth (*.mrpack);;CurseForge (*.zip);;Modpack Manager (*.mpm)"

func (a *App) chooseOpen() {
	dir := a.cfg.LastDir
	p := qt.QFileDialog_GetOpenFileName4(a.win.QWidget, "Open Modpack", dir, openFilter)
	if p != "" {
		a.cfg.LastDir = filepath.Dir(p)
		a.openPath(p)
	}
}

func (a *App) openPath(path string) {
	a.status("Opening %s…", filepath.Base(path))
	go func() {
		doc, err := core.Open(path)
		ui(func() {
			if err != nil {
				a.cfg.RemoveRecent(path)
				_ = a.cfg.Save()
				a.errorBox("Couldn't open modpack", err.Error())
				return
			}
			a.cfg.AddRecent(path)
			_ = a.cfg.Save()
			a.showEditor(doc)
			if doc.NeedsEnrichment() {
				a.enrich()
			}
		})
	}()
}

func (a *App) enrich() {
	doc := a.doc
	a.status("Fetching mod info…")
	go func() {
		rep := a.svc.Enrich(context.Background(), doc.Pack, nil)
		ui(func() {
			if a.doc != doc {
				return
			}
			a.refresh()
			a.status("Mod info updated")
			if len(rep.Warnings) > 0 {
				qt.QMessageBox_Information(a.win.QWidget, "Some mod info is missing", strings.Join(rep.Warnings, "\n\n"))
			}
		})
	}()
}

// maybeSave asks about unsaved changes; false means "cancel".
func (a *App) maybeSave() bool {
	if a.doc == nil || !a.doc.Dirty {
		return true
	}
	r := qt.QMessageBox_Question6(a.win.QWidget, "Save changes?",
		fmt.Sprintf("“%s” has unsaved changes.", a.doc.Pack.Name),
		qt.QMessageBox__Save|qt.QMessageBox__Discard|qt.QMessageBox__Cancel, qt.QMessageBox__Save)
	switch r {
	case qt.QMessageBox__Save:
		return a.save()
	case qt.QMessageBox__Discard:
		return true
	}
	return false
}

func (a *App) save() bool {
	if a.doc == nil {
		return false
	}
	if a.doc.Path == "" {
		return a.saveAs()
	}
	return a.saveTo(a.doc.Path)
}

func (a *App) saveAs() bool {
	p := qt.QFileDialog_GetSaveFileName4(a.win.QWidget, "Save Modpack", a.doc.SuggestedSavePath(), "Modpack Manager (*.mpm)")
	if p == "" {
		return false
	}
	return a.saveTo(p)
}

func (a *App) saveTo(p string) bool {
	if err := a.doc.Save(p); err != nil {
		a.errorBox("Couldn't save", err.Error())
		return false
	}
	a.cfg.AddRecent(a.doc.Path)
	_ = a.cfg.Save()
	a.updateTitle()
	a.status("Saved %s", filepath.Base(a.doc.Path))
	return true
}

func (a *App) markDirty() {
	a.doc.Dirty = true
	a.updateTitle()
}

func (a *App) updateTitle() {
	if a.doc == nil {
		return
	}
	t := a.doc.Pack.Name
	if a.doc.Dirty {
		t += " *"
	}
	a.win.SetWindowTitle(t + " — Modpack Manager")
}

// newPack asks for a name, Minecraft version and loader.
func (a *App) newPack() {
	if !a.maybeSave() {
		return
	}
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("New Modpack")
	form := qt.NewQFormLayout2()
	name := qt.NewQLineEdit3("My Modpack")
	form.AddRow3("Name:", name.QWidget)
	mc := qt.NewQComboBox2()
	mc.SetEditable(true)
	mc.AddItem("1.21.1")
	form.AddRow3("Minecraft version:", mc.QWidget)
	loader := qt.NewQComboBox2()
	loader.AddItems([]string{"Fabric", "Forge", "NeoForge", "Quilt"})
	form.AddRow3("Mod loader:", loader.QWidget)
	bb := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	bb.OnAccepted(d.Accept)
	bb.OnRejected(d.Reject)
	form.AddRowWithWidget(bb.QWidget)
	d.SetLayout(form.QLayout)
	gen := 0
	go func() {
		vs, err := a.svc.MinecraftVersions(context.Background(), false)
		ui(func() {
			if err != nil || len(vs) == 0 || gen != 0 {
				return
			}
			cur := mc.CurrentText()
			mc.Clear()
			mc.AddItems(vs)
			for i, v := range vs {
				if v == cur {
					mc.SetCurrentIndex(i)
				}
			}
		})
	}()
	ok := d.Exec() == int(qt.QDialog__Accepted)
	gen = 1
	if !ok {
		return
	}
	l := model.Loaders[loader.CurrentIndex()]
	mcv := strings.TrimSpace(mc.CurrentText())
	doc := core.NewDocument(strings.TrimSpace(name.Text()), mcv, l, "")
	a.showEditor(doc)
	go func() {
		vs, err := core.LoaderVersions(context.Background(), l, mcv)
		ui(func() {
			if err == nil && len(vs) > 0 && a.doc == doc && doc.Pack.LoaderVersion == "" {
				doc.Pack.LoaderVersion = vs[0]
				a.refresh()
			}
		})
	}()
}
