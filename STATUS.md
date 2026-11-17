# Status

Done and tested:
- Core (formats, Modrinth/CurseForge APIs, search, add-with-dependencies, exports): `make test`
- Command-line launcher (`cmd/modpackmanager`)
- GTK4/libadwaita app (`cmd/modpackmanager-gtk`), run and checked on screen in light and dark mode

Qt 6 app (`cmd/modpackmanager-qt`): written; compile check still running at the time of this snapshot.

You can delete the old `_wip/` folder (an early snapshot, ignored by `go build`).
