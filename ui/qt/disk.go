package qtui

import (
	"fmt"
	"path/filepath"

	qt "github.com/mappu/miqt/qt6"
)

// diskCheckMS is how often the open pack's file is checked for changes made
// by other programs: an AI assistant using "modpackmanager mcp", the command
// line, or another window.
const diskCheckMS = 2000

func (a *App) watchDisk() {
	t := qt.NewQTimer2(a.win.QObject)
	t.OnTimeout(a.checkDisk)
	t.Start(diskCheckMS)
}

// checkDisk reloads the pack when its file changed, or asks first when there
// are unsaved changes here.
func (a *App) checkDisk() {
	doc := a.doc
	if doc == nil || a.asking || qt.QApplication_ActiveModalWidget() != nil {
		return // nothing open, or a dialog may be working on the pack
	}
	if !doc.ChangedOnDisk() {
		return
	}
	if !doc.Dirty {
		a.reloadFromDisk()
		return
	}
	// Ask once per change; a later change asks again.
	doc.AcknowledgeDiskChange()
	a.asking = true
	defer func() { a.asking = false }()
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, "Pack changed on disk", fmt.Sprintf(
		"Another program, such as an AI assistant using the MCP server, changed “%s”, and you have unsaved changes.\n\n"+
			"Reload to see its version and lose yours, or keep yours: saving then replaces its changes.", filepath.Base(doc.Path)),
		qt.QMessageBox__NoButton, a.win.QWidget)
	box.SetTextFormat(qt.PlainText)
	keep := box.AddButton2("Keep Mine", qt.QMessageBox__RejectRole)
	reload := box.AddButton2("Reload", qt.QMessageBox__DestructiveRole)
	box.SetDefaultButton(keep)
	box.SetEscapeButton(keep.QAbstractButton)
	box.Exec()
	if clicked(box, reload) && a.doc == doc {
		a.reloadFromDisk()
	}
}

func clicked(box *qt.QMessageBox, b *qt.QPushButton) bool {
	c := box.ClickedButton()
	return c != nil && c.UnsafePointer() == b.QAbstractButton.UnsafePointer()
}

// reloadFromDisk replaces the open pack with what its file holds now.
func (a *App) reloadFromDisk() bool {
	doc := a.doc
	name := filepath.Base(doc.Path)
	if err := doc.Reload(); err != nil {
		// Perhaps half-written by a program that doesn't save safely; its
		// next change tries again.
		doc.AcknowledgeDiskChange()
		a.status("“%s” changed on disk but couldn't be read: %v", name, err)
		return false
	}
	a.refresh()
	a.status("Reloaded “%s”: another program changed it", name)
	return true
}

// confirmOverwrite asks before saving over changes another program made.
// It returns whether to save; after "Reload Instead" the pack is reloaded
// and nothing needs saving.
func (a *App) confirmOverwrite(path string) (save, reloaded bool) {
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, "Replace changes made elsewhere?", fmt.Sprintf(
		"Another program, such as an AI assistant using the MCP server, changed “%s” after this window last loaded or saved it.\n\n"+
			"Saving replaces its changes with yours.", filepath.Base(path)),
		qt.QMessageBox__NoButton, a.win.QWidget)
	box.SetTextFormat(qt.PlainText)
	cancel := box.AddButton2("Cancel", qt.QMessageBox__RejectRole)
	reload := box.AddButton2("Reload Instead", qt.QMessageBox__ResetRole)
	saveBtn := box.AddButton2("Save Anyway", qt.QMessageBox__DestructiveRole)
	box.SetDefaultButton(cancel)
	box.SetEscapeButton(cancel.QAbstractButton)
	a.asking = true
	box.Exec()
	a.asking = false
	switch {
	case clicked(box, saveBtn):
		return true, false
	case clicked(box, reload):
		return false, a.reloadFromDisk()
	}
	return false, false
}
