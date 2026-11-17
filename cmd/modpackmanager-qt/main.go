// Command modpackmanager-qt is the Qt 6 frontend (KDE Plasma, Windows, macOS).
package main

import (
	"os"

	qtui "modpackmanager/ui/qt"
)

func main() { os.Exit(qtui.Run(os.Args[1:])) }
