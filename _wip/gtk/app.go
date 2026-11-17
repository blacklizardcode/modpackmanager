// WORK IN PROGRESS — not compiled yet. Lives under _wip/ (ignored by the Go
// tool) until it builds; it will move to ui/gtk + cmd/modpackmanager-gtk.
//
// GTK4 + libadwaita frontend: welcome screen, then the pack editor.
package gtkui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"modpackmanager/internal/config"
	"modpackmanager/internal/core"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

const appID = "io.github.blakka.ModpackManager"

// App holds the window-wide state.
type App struct {
	app    *adw.Application
	win    *adw.ApplicationWindow
	toasts *adw.ToastOverlay
	cfg    *config.Config
	svc    *core.Service
	doc    *core.Document
}

// Run starts the GTK main loop.
func Run(args []string) int {
	a := &App{cfg: config.Load()}
	a.svc = core.New(a.cfg.APIKey())
	a.app = adw.NewApplication(appID, gio.ApplicationHandlesOpen)
	a.app.ConnectActivate(func() { a.activate(""); a.showWelcome() })
	a.app.ConnectOpen(func(files []gio.Filer, hint string) {
		a.activate("")
		if len(files) > 0 {
			a.openPath(files[0].Path())
		}
	})
	return a.app.Run(append([]string{os.Args[0]}, args...))
}

func (a *App) activate(_ string) {
	if a.win != nil {
		a.win.Present()
		return
	}
	a.win = adw.NewApplicationWindow(&a.app.Application)
	a.win.SetTitle("Modpack Manager")
	w, h := a.cfg.WindowWidth, a.cfg.WindowHeight
	if w < 600 || h < 400 {
		w, h = 1100, 720
	}
	a.win.SetDefaultSize(w, h)
	a.toasts = adw.NewToastOverlay()
	a.win.SetContent(a.toasts)
	a.win.Present()
}

// toast shows a short, non-blocking message.
func (a *App) toast(format string, args ...any) {
	t := adw.NewToast(fmt.Sprintf(format, args...))
	t.SetTimeout(4)
	a.toasts.AddToast(t)
}

// ui runs f on the GTK main thread.
func ui(f func()) { glib.IdleAdd(f) }

// showWelcome is the main screen: open / new / recent packs.
func (a *App) showWelcome() {
	tv := adw.NewToolbarView()
	hb := adw.NewHeaderBar()
	tv.AddTopBar(hb)

	page := adw.NewStatusPage()
	page.SetIconName("package-x-generic-symbolic")
	page.SetTitle("Modpack Manager")
	page.SetDescription("Open a Modrinth (.mrpack), CurseForge (.zip) or Modpack Manager (.mpm) file, or start a new pack.")

	box := gtk.NewBox(gtk.OrientationVertical, 18)
	box.SetHAlign(gtk.AlignCenter)
	btns := gtk.NewBox(gtk.OrientationHorizontal, 12)
	btns.SetHAlign(gtk.AlignCenter)
	open := gtk.NewButtonWithLabel("Open Modpack…")
	open.AddCSSClass("suggested-action")
	open.AddCSSClass("pill")
	open.ConnectClicked(a.chooseOpen)
	newBtn := gtk.NewButtonWithLabel("New Modpack")
	newBtn.AddCSSClass("pill")
	newBtn.ConnectClicked(func() { a.showEditor(core.NewDocument("", "1.21.1", "fabric", "")) })
	btns.Append(open)
	btns.Append(newBtn)
	box.Append(btns)

	if len(a.cfg.Recent) > 0 {
		grp := adw.NewPreferencesGroup()
		grp.SetTitle("Recent")
		for _, p := range a.cfg.Recent {
			p := p
			row := adw.NewActionRow()
			row.SetTitle(markup.Escape(markup.Pango, filepath.Base(p)))
			row.SetSubtitle(markup.Escape(markup.Pango, filepath.Dir(p)))
			row.SetActivatable(true)
			row.AddSuffix(gtk.NewImageFromIconName("go-next-symbolic"))
			row.ConnectActivated(func() { a.openPath(p) })
			grp.Add(row)
		}
		clamp := adw.NewClamp()
		clamp.SetMaximumSize(520)
		clamp.SetChild(grp)
		box.Append(clamp)
	}
	page.SetChild(box)
	tv.SetContent(page)
	a.toasts.SetChild(tv)
}

func (a *App) chooseOpen() {
	d := gtk.NewFileDialog()
	d.SetTitle("Open Modpack")
	f := gtk.NewFileFilter()
	f.SetName("Modpacks (.mrpack, .zip, .mpm)")
	for _, pat := range []string{"*.mrpack", "*.zip", "*.mpm"} {
		f.AddPattern(pat)
	}
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(f.Object)
	d.SetFilters(filters)
	d.Open(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		file, err := d.OpenFinish(res)
		if err != nil || file == nil {
			return // cancelled
		}
		a.openPath(file.Path())
	})
}

// openPath loads a pack off the main thread, then enriches metadata.
func (a *App) openPath(path string) {
	a.toast("Opening %s…", filepath.Base(path))
	go func() {
		doc, err := core.Open(path)
		ui(func() {
			if err != nil {
				a.error("Couldn't open modpack", err.Error())
				a.cfg.RemoveRecent(path)
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
	go func() {
		rep := a.svc.Enrich(context.Background(), doc.Pack, nil)
		ui(func() {
			if a.doc != doc {
				return
			}
			a.refreshList()
			for _, w := range rep.Warnings {
				a.toast("%s", w)
			}
		})
	}()
}

func (a *App) error(title, body string) {
	d := adw.NewAlertDialog(title, body)
	d.AddResponse("ok", "OK")
	d.Present(a.win)
}

// showEditor and refreshList are implemented in editor.go (next step).
var _ = model.SideBoth
