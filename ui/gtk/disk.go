package gtkui

import (
	"fmt"
	"path/filepath"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
)

// diskCheckMS is how often the open pack's file is checked for changes made
// by other programs: an AI assistant using "modpackmanager mcp", the command
// line, or another window.
const diskCheckMS = 2000

// checkDisk reloads the pack when its file changed, or asks first when there
// are unsaved changes here.
func (a *App) checkDisk() {
	doc := a.doc
	if doc == nil || a.win == nil || a.ed.list == nil || a.win.VisibleDialog() != nil {
		return // nothing open, or a dialog may be working on the pack
	}
	if !doc.ChangedOnDisk() {
		return
	}
	if !doc.Dirty {
		a.reloadFromDisk(nil)
		return
	}
	// Ask once per change; a later change asks again.
	doc.AcknowledgeDiskChange()
	d := adw.NewAlertDialog("Pack Changed on Disk", fmt.Sprintf(
		"Another program, such as an AI assistant using the MCP server, changed “%s”, and you have unsaved changes. "+
			"Reload to see its version and lose yours, or keep yours: saving then replaces its changes.", filepath.Base(doc.Path)))
	d.AddResponse("keep", "Keep Mine")
	d.AddResponse("reload", "Reload")
	d.SetResponseAppearance("reload", adw.ResponseDestructive)
	d.SetDefaultResponse("keep")
	d.SetCloseResponse("keep")
	d.ConnectResponse(func(r string) {
		if r == "reload" && a.doc == doc {
			a.reloadFromDisk(nil)
		}
	})
	d.Present(a.win)
}

// reloadFromDisk replaces the open pack with what its file holds now.
func (a *App) reloadFromDisk(after func()) {
	doc := a.doc
	name := filepath.Base(doc.Path)
	if err := doc.Reload(); err != nil {
		// Perhaps half-written by a program that doesn't save safely; its
		// next change tries again.
		doc.AcknowledgeDiskChange()
		a.toast("“%s” changed on disk but couldn't be read: %v", name, err)
		return
	}
	for id := range a.ed.selected {
		if doc.Pack.ModByID(id) == nil {
			delete(a.ed.selected, id)
		}
	}
	a.refreshList()
	a.toast("Reloaded “%s”: another program changed it", name)
	if after != nil {
		after()
	}
}

// confirmOverwrite asks before saving over changes another program made.
func (a *App) confirmOverwrite(path string, after func()) {
	doc := a.doc
	d := adw.NewAlertDialog("Replace Changes Made Elsewhere?", fmt.Sprintf(
		"Another program, such as an AI assistant using the MCP server, changed “%s” after this window last loaded or saved it. "+
			"Saving replaces its changes with yours.", filepath.Base(path)))
	d.AddResponse("cancel", "Cancel")
	d.AddResponse("reload", "Reload Instead")
	d.AddResponse("save", "Save Anyway")
	d.SetResponseAppearance("save", adw.ResponseDestructive)
	d.SetDefaultResponse("cancel")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		if a.doc != doc {
			return
		}
		switch r {
		case "save":
			a.writeDoc(path, after)
		case "reload":
			a.reloadFromDisk(after)
		}
	})
	d.Present(a.win)
}
