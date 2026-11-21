# Status

## Tested
- Core (formats, Modrinth/CurseForge APIs, search, add-with-dependencies, adding jar files, exports): `sh build.sh test`, against local mocks. The jar-metadata and TOML readers are also fuzz-tested.
- Launcher and command line (`cmd/modpackmanager`), including `modpackmanager add`.
- GTK4/libadwaita app and Qt 6 app: built and driven on X11 (Xvfb, Ubuntu 24.04). Add JAR Files was checked end to end in both: the menu and toolbar entries, Ctrl+Shift+J, picking several files, drag and drop from another app, warnings for wrong-loader jars and non-jar files, the details dialog, saving, and a server export.
- MCP server (`modpackmanager mcp`): tests for both protocol eras and every tool against mocks, also under the race detector. The official MCP Go SDK client (v1.7.0, built with Go 1.24 for the test) connected with each of the five protocol revisions and created, edited and exported a pack. Both apps were checked under Xvfb reloading a pack the server changed, asking when there were unsaved changes (keep or reload), waiting while a dialog was open, and asking before saving over the server's changes.
- CI (GitHub Actions) for c4f3356: tests, both Linux tarballs (built and smoke-tested headless) and `nix flake check` (builds and tests the GTK and Qt Nix packages) passed.

## Not tried
- Live Modrinth/CurseForge APIs from the machine that writes the code (it can't reach them), so those calls are only tested against mocks.
- Wayland sessions (drag and drop there goes through GTK's and Qt's own code).
- Windows (the experimental CI job) and macOS (not built at all).
- The MCP server with Claude Code or Claude Desktop themselves, or other MCP clients (only the Go SDK's client was tried).

## Releasing
See "Releases and CI" in the README: bump `internal/version/VERSION`, push a matching `v<VERSION>` tag, then publish the draft release the workflow creates.

You can delete the old `_wip/` folder (an early snapshot, ignored by `go build`).
