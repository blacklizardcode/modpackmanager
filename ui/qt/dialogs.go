package qtui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"modpackmanager/internal/core"
	"modpackmanager/internal/model"
)

func (a *App) showExport(t core.Target) {
	if a.doc == nil {
		return
	}
	p := a.doc.Pack
	opt := core.DefaultExportOptions(t)
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("Export " + t.Label())
	d.SetMinimumWidth(480)
	v := qt.NewQVBoxLayout2()
	summary := qt.NewQLabel2()
	summary.SetWordWrap(true)
	v.AddWidget(summary.QWidget)

	form := qt.NewQFormLayout2()
	formats := []core.Format{core.FormatMrpack, core.FormatCurseForge, core.FormatZip}
	fmtBox := qt.NewQComboBox2()
	for i, f := range formats {
		fmtBox.AddItem(f.Label())
		if f == opt.Format {
			fmtBox.SetCurrentIndex(i)
		}
	}
	form.AddRow3("Format:", fmtBox.QWidget)
	common := qt.NewQCheckBox3("Include common (client + server) mods")
	common.SetChecked(opt.IncludeCommon)
	common.SetToolTip("Makes the export installable on its own")
	if t == core.TargetClient || t == core.TargetServer {
		form.AddRowWithWidget(common.QWidget)
	}
	embed := qt.NewQCheckBox3("Bundle jars that aren't hosted on Modrinth")
	embed.SetToolTip("Needed for modrinth.com uploads. Check the mods' licenses before sharing.")
	form.AddRowWithWidget(embed.QWidget)
	v.AddLayout(form.QLayout)

	progress := qt.NewQProgressBar2()
	progress.SetVisible(false)
	v.AddWidget(progress.QWidget)
	bb := qt.NewQDialogButtonBox2()
	exportBtn := bb.AddButton2("Export…", qt.QDialogButtonBox__AcceptRole)
	exportBtn.SetIcon(themeIcon("document-export", "document-save-as"))
	bb.AddButtonWithButton(qt.QDialogButtonBox__Cancel)
	bb.OnRejected(d.Reject)
	v.AddWidget(bb.QWidget)
	d.SetLayout(v.QLayout)

	update := func() {
		opt.Format = formats[fmtBox.CurrentIndex()]
		opt.IncludeCommon = common.IsChecked()
		opt.EmbedNonModrinth = embed.IsChecked()
		embed.SetVisible(opt.Format == core.FormatMrpack)
		ms := core.SelectMods(p, t, opt.IncludeCommon)
		ovs := core.SelectOverrides(p, t, opt.IncludeCommon)
		unknown := 0
		for _, m := range ms {
			if m.Side == model.SideUnknown || m.Side == "" {
				unknown++
			}
		}
		txt := fmt.Sprintf("<b>%d mods</b> and %d override files will be exported.", len(ms), len(ovs))
		if n := core.CountStored(ms); n > 0 {
			txt += fmt.Sprintf(" %d JAR file%s stored in this pack will be bundled.", n, plural(n))
		}
		if opt.Format == core.FormatZip && core.CountStored(ms) < len(ms) {
			txt += " Jars are downloaded and checked against their hashes."
		}
		if unknown > 0 {
			txt += fmt.Sprintf("<br/>%d of them have no side information and are treated as common.", unknown)
		}
		summary.SetText(txt)
	}
	fmtBox.OnCurrentIndexChanged(func(int) { update() })
	common.OnToggled(func(bool) { update() })
	embed.OnToggled(func(bool) { update() })
	update()

	var cancel context.CancelFunc
	d.OnFinished(func(int) {
		if cancel != nil {
			cancel()
		}
	})
	exportBtn.OnClicked(func() {
		dir := a.cfg.LastDir
		out := qt.QFileDialog_GetSaveFileName4(d.QWidget, "Export "+t.Label(), filepath.Join(dir, core.SuggestedFileName(p, opt)), "*"+opt.Format.Ext())
		if out == "" {
			return
		}
		a.cfg.LastDir = filepath.Dir(out)
		_ = a.cfg.Save()
		exportBtn.SetEnabled(false)
		progress.SetVisible(true)
		progress.SetRange(0, 0)
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		o := opt
		go func() {
			data, rep, err := a.svc.Export(ctx, p, o, nil, func(stage string, done, total int) {
				ui(func() {
					if total > 0 {
						progress.SetRange(0, total)
						progress.SetValue(done)
					}
					progress.SetFormat(stage)
				})
			})
			if err == nil {
				err = os.WriteFile(out, data, 0o644)
			}
			ui(func() {
				if ctx.Err() != nil {
					return
				}
				exportBtn.SetEnabled(true)
				progress.SetVisible(false)
				if err != nil {
					a.errorBox("Export failed", err.Error())
					return
				}
				d.Accept()
				msg := fmt.Sprintf("Exported %d mods to %s.", rep.Mods, filepath.Base(out))
				var details []string
				if len(rep.Skipped) > 0 {
					details = append(details, fmt.Sprintf("%d mods couldn't be downloaded and were left out:\n• %s", len(rep.Skipped), strings.Join(rep.Skipped, "\n• ")))
				}
				if len(rep.Warnings) > 0 {
					details = append(details, "• "+strings.Join(rep.Warnings, "\n• "))
				}
				if len(details) == 0 {
					a.status("%s", msg)
					return
				}
				qt.QMessageBox_Information(a.win.QWidget, "Export finished", msg+"\n\n"+strings.Join(details, "\n\n"))
			})
		}()
	})
	d.Open()
}

func (a *App) showPackSettings() {
	if a.doc == nil {
		return
	}
	p := a.doc.Pack
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("Pack Settings")
	d.SetMinimumWidth(460)
	form := qt.NewQFormLayout2()
	name := qt.NewQLineEdit3(p.Name)
	version := qt.NewQLineEdit3(p.Version)
	author := qt.NewQLineEdit3(p.Author)
	summary := qt.NewQLineEdit3(p.Summary)
	mc := qt.NewQLineEdit3(p.Minecraft)
	loader := qt.NewQComboBox2()
	loader.AddItems([]string{"Fabric", "Forge", "NeoForge", "Quilt"})
	for i, l := range model.Loaders {
		if l == p.Loader {
			loader.SetCurrentIndex(i)
		}
	}
	lv := qt.NewQLineEdit3(p.LoaderVersion)
	latest := qt.NewQPushButton3("Latest")
	latest.SetToolTip("Fetch the newest stable loader version for this Minecraft version")
	lvRow := qt.NewQHBoxLayout2()
	lvRow.AddWidget2(lv.QWidget, 1)
	lvRow.AddWidget(latest.QWidget)
	latest.OnClicked(func() {
		latest.SetEnabled(false)
		l, ver := model.Loaders[loader.CurrentIndex()], strings.TrimSpace(mc.Text())
		go func() {
			vs, err := core.LoaderVersions(context.Background(), l, ver)
			ui(func() {
				latest.SetEnabled(true)
				if err != nil || len(vs) == 0 {
					a.status("No %s version found for Minecraft %s", model.LoaderLabel(l), ver)
					return
				}
				lv.SetText(vs[0])
			})
		}()
	})
	form.AddRow3("Name:", name.QWidget)
	form.AddRow3("Version:", version.QWidget)
	form.AddRow3("Author:", author.QWidget)
	form.AddRow3("Summary:", summary.QWidget)
	form.AddRow3("Minecraft version:", mc.QWidget)
	form.AddRow3("Mod loader:", loader.QWidget)
	form.AddRow4("Loader version:", lvRow.QLayout)
	note := qt.NewQLabel3("Search results and new mods are filtered to this Minecraft version and loader.")
	note.SetWordWrap(true)
	note.SetStyleSheet("color: palette(placeholder-text)")
	form.AddRowWithWidget(note.QWidget)
	bb := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	bb.OnAccepted(d.Accept)
	bb.OnRejected(d.Reject)
	form.AddRowWithWidget(bb.QWidget)
	d.SetLayout(form.QLayout)
	d.OnFinished(func(r int) {
		if r != int(qt.QDialog__Accepted) {
			return
		}
		p.Name = strings.TrimSpace(name.Text())
		p.Version = strings.TrimSpace(version.Text())
		p.Author = strings.TrimSpace(author.Text())
		p.Summary = strings.TrimSpace(summary.Text())
		p.Minecraft = strings.TrimSpace(mc.Text())
		p.Loader = model.Loaders[loader.CurrentIndex()]
		p.LoaderVersion = strings.TrimSpace(lv.Text())
		a.markDirty()
		a.refresh()
	})
	d.Open()
}

func (a *App) showPreferences() {
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("Preferences")
	d.SetMinimumWidth(460)
	form := qt.NewQFormLayout2()
	key := qt.NewQLineEdit3(a.cfg.CurseForgeAPIKey)
	key.SetEchoMode(qt.QLineEdit__Password)
	form.AddRow3("CurseForge API key:", key.QWidget)
	note := qt.NewQLabel3(`CurseForge's API needs a key. Get a free one at <a href="https://console.curseforge.com/">console.curseforge.com</a>. It's stored only on this computer.`)
	note.SetWordWrap(true)
	note.SetOpenExternalLinks(true)
	form.AddRowWithWidget(note.QWidget)
	bb := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	bb.OnAccepted(d.Accept)
	bb.OnRejected(d.Reject)
	form.AddRowWithWidget(bb.QWidget)
	d.SetLayout(form.QLayout)
	d.OnFinished(func(r int) {
		if r != int(qt.QDialog__Accepted) {
			return
		}
		a.cfg.CurseForgeAPIKey = strings.TrimSpace(key.Text())
		_ = a.cfg.Save()
		a.svc.SetCurseForgeKey(a.cfg.APIKey())
	})
	d.Open()
}

func (a *App) showModDetails(m *model.Mod) {
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle(m.DisplayName())
	d.Resize(720, 720)
	v := qt.NewQVBoxLayout2()
	form := qt.NewQFormLayout2()
	fileL := qt.NewQLabel3(m.File.Path)
	fileL.SetTextInteractionFlags(qt.TextSelectableByMouse)
	form.AddRow3("File:", fileL.QWidget)
	if m.File.Stored() {
		form.AddRow3("Stored in this pack:", qt.NewQLabel3(core.FormatSize(int64(len(m.File.Data)))).QWidget)
	}
	if l := m.Local; l != nil && m.PrimarySource() == model.SourceLocal {
		for _, row := range [][2]string{{"Version:", l.Version}, {"Mod ID:", l.ModID}, {"Made for:", core.LoaderLabels(l.Loaders)}} {
			if row[1] != "" {
				lbl := qt.NewQLabel3(row[1])
				lbl.SetTextFormat(qt.PlainText)
				lbl.SetTextInteractionFlags(qt.TextSelectableByMouse)
				form.AddRow3(row[0], lbl.QWidget)
			}
		}
	}
	if m.Modrinth != nil && m.Modrinth.VersionNumber != "" {
		form.AddRow3("Modrinth version:", qt.NewQLabel3(m.Modrinth.VersionNumber).QWidget)
	}
	if m.CurseForge != nil {
		form.AddRow3("CurseForge IDs:", qt.NewQLabel3(fmt.Sprintf("project %d, file %d", m.CurseForge.ProjectID, m.CurseForge.FileID)).QWidget)
	}
	side := m.Side.Label()
	if m.SideLocked {
		side += " (set by you)"
	}
	form.AddRow3("Side:", qt.NewQLabel3(side).QWidget)
	notes := qt.NewQLineEdit3(m.Notes)
	notes.SetPlaceholderText("Your notes about this mod")
	form.AddRow3("Notes:", notes.QWidget)
	v.AddLayout(form.QLayout)
	browser := qt.NewQTextBrowser2()
	browser.SetOpenLinks(false)
	browser.OnAnchorClicked(func(u *qt.QUrl) { qt.QDesktopServices_OpenUrl(u) })
	v.AddWidget2(browser.QWidget, 1)
	bb := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Close)
	bb.OnRejected(d.Reject)
	v.AddWidget(bb.QWidget)
	d.SetLayout(v.QLayout)
	gen := 0
	if h := core.HitFromMod(m); h != nil {
		a.renderDetails(browser, h, &gen)
	} else if m.Summary != "" {
		browser.SetPlainText(m.Summary) // a jar's own description
	} else {
		browser.SetHtml("<p>This file isn't linked to Modrinth or CurseForge.</p>")
	}
	d.OnFinished(func(int) {
		gen++
		if notes.Text() != m.Notes {
			m.Notes = notes.Text()
			a.markDirty()
		}
	})
	d.Open()
}
