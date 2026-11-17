// Command modpackmanager-gtk is the GTK4/libadwaita frontend.
package main

import (
	"os"

	gtkui "modpackmanager/ui/gtk"
)

func main() { os.Exit(gtkui.Run(os.Args[1:])) }
