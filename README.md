# Modpack Manager

A native desktop editor for Minecraft modpacks, written in Go.

- Opens **Modrinth `.mrpack`**, **CurseForge `.zip`** and its own **`.mpm`** files
- Group mods, set per-mod client/server side, keep notes
- Search and add mods from **Modrinth** and **CurseForge** (required dependencies are added automatically)
- Descriptions render natively: Markdown (Modrinth) and HTML (CurseForge), including images, lists, tables and code
- Three export buttons: **Client**, **Common** (client + server) and **Server**, each as `.mrpack`, CurseForge `.zip`, or a ready-to-run zip with the jars downloaded and hash-checked
- Native look per platform: **GTK4 + libadwaita** on GNOME and most Linux desktops, **Qt 6** on KDE Plasma, Windows and macOS. The `modpackmanager` launcher picks the right one (`MPM_UI=gtk|qt` to override).

## Building

Requires Go 1.24+ and the toolkit headers below.

```sh
go mod tidy        # first time: fetches deps and verifies them against the Go checksum DB
./build.sh         # launcher + GTK app   (GNOME and most Linux desktops)
./build.sh qt      # Qt app               (KDE Plasma, Windows, macOS)
./build.sh test    # tests (no network needed; the APIs are mocked)
./build.sh install # into ~/.local, with a menu entry and .mrpack/.mpm file associations
```

Then run `modpackmanager` (or `modpackmanager pack.mrpack`). It opens the GTK app on GNOME and similar desktops and the Qt app on KDE, picking whichever is installed if only one is. `MPM_UI=gtk|qt` forces one.

| Frontend | Packages needed |
|---|---|
| GTK | Debian/Ubuntu: `libgtk-4-dev libadwaita-1-dev libgirepository1.0-dev` · Fedora: `gtk4-devel libadwaita-devel gobject-introspection-devel` · Arch: `gtk4 libadwaita gobject-introspection` |
| Qt | Debian/Ubuntu: `qt6-base-dev` · Fedora: `qt6-qtbase-devel` · Arch: `qt6-base` · macOS: `brew install qt@6` · Windows: Qt 6 + MinGW (see the miqt README) |

The first build of each frontend is slow (the GTK and Qt bindings are huge; 10–30 minutes and a few GB of RAM). Later builds are fast. Build them one at a time on machines with 8 GB of RAM or less.

The GTK app targets libadwaita **1.5** (Ubuntu 24.04, Debian 13, Fedora 40+, Arch), so it builds on older distros too. For SVG badges in descriptions, have `librsvg` installed (it normally is on GNOME); the Qt app needs the `qt6-svg` package for the same.

## Command line

```sh
modpackmanager [file]                         # open the GUI
modpackmanager info pack.mrpack               # list mods by group and side
modpackmanager convert pack.zip pack.mpm      # import into the .mpm format
modpackmanager export pack.mpm server zip --with-common   # server pack with jars
modpackmanager export pack.mpm client mrpack              # client-only mods
modpackmanager export pack.mpm common curseforge          # mods needed on both sides
```

Export targets: `client` (client-only mods), `common` (needed on both), `server` (server-only mods), `full`.
`--with-common` adds the common mods to a client/server export so it is installable on its own (the GUI does this by default for server exports, because a server can't start without them).
`--embed` puts jars that aren't hosted on Modrinth inside the `.mrpack` instead of linking to them.

## CurseForge API key

CurseForge's API refuses requests without a key. Get a free one at <https://console.curseforge.com/> and enter it in Preferences, or set `MPM_CURSEFORGE_API_KEY` (also `CF_API_KEY`). It's stored with owner-only permissions in `~/.config/modpackmanager/config.json`.

Without a key, CurseForge packs still open and save, but their mods only show as project IDs, and CurseForge search is disabled.

## How sides are decided

1. A side you set by hand always wins (stored as `sideLocked`).
2. A one-sided `env` in an imported `.mrpack` is kept (the pack author chose it).
3. Modrinth's version `environment` field, then the project's `client_side`/`server_side`.
4. CurseForge's `Client`/`Server` file tags; CurseForge files are also matched on Modrinth by SHA-1 to get better side data.
5. Otherwise *Unknown*, which exports as common (the safe choice). Server exports warn about every unknown mod.

## The `.mpm` format

A zip archive:

```
modpackmanager.json    {"format":"modpackmanager","formatVersion":1, name, version, minecraft, loader, loaderVersion, groups[], mods[]}
overrides/             files for client and server
client-overrides/      client-only files
server-overrides/      server-only files
```

Each mod stores its name, side (+ whether you locked it), group, notes, file path/hashes/download URLs, and its Modrinth and/or CurseForge IDs. Saves are atomic (temp file + rename).

## Known limitations

- **Modrinth's website only accepts `.mrpack` files whose downloads come from Modrinth, GitHub or GitLab.** Mods only on CurseForge are linked to CurseForge's CDN, which launchers like Prism accept but modrinth.com rejects. Use `--embed` (or the Embed option) to bundle those jars, but note that redistributing jars may break a mod's license or the author's CurseForge distribution setting.
- CurseForge sometimes hides a file's download URL (author opted out of third-party downloads); the app falls back to the CDN URL, which can still fail for those files. Failed downloads are listed after an export.
- Modrinth's side metadata is set by mod authors and is sometimes wrong; check the server export warnings and lock sides by hand where needed.
