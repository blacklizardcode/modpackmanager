# Modpack Manager

A native desktop editor for Minecraft modpacks, written in Go.

- Opens **Modrinth `.mrpack`**, **CurseForge `.zip`** and its own **`.mpm`** files
- Group mods, set per-mod client/server side, keep notes
- Search and add mods from **Modrinth** and **CurseForge** (required dependencies are added automatically)
- Add your own **`.jar` files** (Add Mods ▾ → Add JAR Files…, Ctrl+Shift+J, or drag them onto the window; a dropped folder adds the jars in it). A jar that is on Modrinth is linked to it; any other jar is stored in the pack and bundled into exports. Name, version, side and icon come from the jar's `fabric.mod.json`, `quilt.mod.json` or `mods.toml`, and adding a newer build of a stored jar replaces the old one
- Descriptions render natively: Markdown (Modrinth) and HTML (CurseForge), including images, lists, tables and code
- Three export buttons: **Client**, **Common** (client + server) and **Server**, each as `.mrpack`, CurseForge `.zip`, or a ready-to-run zip with the jars downloaded and hash-checked
- **AI assistants** (Claude Code, Claude Desktop, other MCP clients) can read, edit and export packs through the built-in MCP server, `modpackmanager mcp`
- Native look per platform: **GTK4 + libadwaita** on GNOME and most Linux desktops, **Qt 6** on KDE Plasma, Windows and macOS. The `modpackmanager` launcher picks the right one (`MPM_UI=gtk|qt` to override).

## Install

**Linux:** download `modpackmanager-<version>-linux-x86_64-gtk.tar.gz` (GNOME and most desktops) or `…-qt.tar.gz` (KDE) from the Releases page, unpack it and run `./install.sh` (installs to `~/.local`; `PREFIX=/usr/local ./install.sh` for system-wide). The binaries are built on Ubuntu 24.04 and use your system's toolkit, so you need a distro at least that new (GTK 4.14 + libadwaita 1.5, or Qt 6.4). On Debian/Ubuntu the runtime packages are `libgtk-4-1 libadwaita-1-0 librsvg2-common` (GTK) or `libqt6widgets6t64 libqt6svg6 qt6-wayland` (Qt). Check the download against `SHA256SUMS`.

**NixOS / Nix:** see [NixOS and Nix](#nixos-and-nix).

**Windows:** the release has a `…-windows-x86_64-qt.zip` when its build succeeded (this build is experimental). Unzip it anywhere and run `modpackmanager-qt.exe`.

**macOS:** no release build yet. Build from source (`brew install qt@6`, then `sh build.sh qt`); this is untested.

## NixOS and Nix

The repository is a flake that follows `nixos-unstable`. It builds the launcher plus both frontends, wrapped so that GTK schemas and Qt plugins are found:

```sh
nix run github:blacklizardcode/modpackmanager          # launcher + GTK + Qt, picks one per desktop
nix run github:blacklizardcode/modpackmanager#gtk      # GTK only (smaller)
nix run github:blacklizardcode/modpackmanager#qt       # Qt only
```

In a NixOS or home-manager flake configuration:

```nix
inputs.modpackmanager.url = "github:blacklizardcode/modpackmanager";
inputs.modpackmanager.inputs.nixpkgs.follows = "nixpkgs";   # optional: build against your nixpkgs

# then, either the package directly:
environment.systemPackages = [ inputs.modpackmanager.packages.${pkgs.stdenv.hostPlatform.system}.default ];
# or through the overlay:
nixpkgs.overlays = [ inputs.modpackmanager.overlays.default ];   # gives you pkgs.modpackmanager
```

`nix develop` gives a shell with Go, gopls, the GTK and Qt libraries, shellcheck and actionlint, and `nix flake check` builds and tests both variants.

Maintaining the flake: Nix builds the Go modules from a hash, so after any change to `go.mod` or `go.sum` run `bash nix/update-vendor-hash.sh` and commit `nix/vendor-hash.txt` with them. Flakes only see files that git tracks, so `git add` new files before `nix build`. Commit `flake.lock` too (`nix flake update` bumps nixpkgs).

## Building

Requires Go 1.24+ and the toolkit headers below.

```sh
go mod tidy        # first time: fetches deps and verifies them against the Go checksum DB
sh build.sh         # launcher + GTK app   (GNOME and most Linux desktops)
sh build.sh qt      # Qt app               (KDE Plasma, Windows, macOS)
sh build.sh test    # tests (no network needed; the APIs are mocked)
sh build.sh install # into ~/.local, with a menu entry and .mrpack/.mpm file associations
```

Then run `modpackmanager` (or `modpackmanager pack.mrpack`). It opens the GTK app on GNOME and similar desktops and the Qt app on KDE, picking whichever is installed if only one is. `MPM_UI=gtk|qt` forces one.

| Frontend | Packages needed |
|---|---|
| GTK | Debian/Ubuntu: `libgtk-4-dev libadwaita-1-dev libgirepository1.0-dev` · Fedora: `gtk4-devel libadwaita-devel gobject-introspection-devel` · Arch: `gtk4 libadwaita gobject-introspection` |
| Qt | Debian/Ubuntu: `qt6-base-dev` · Fedora: `qt6-qtbase-devel` · Arch: `qt6-base` · macOS: `brew install qt@6` · Windows: Qt 6 + MinGW (see the miqt README) |

The first build of each frontend is slow (the GTK and Qt bindings are huge): about 20 minutes on two cores for GTK, and the Qt one needs far more memory. The Go compiler peaked at about 2 GB for the GTK bindings and about 6 GB for the Qt bindings, and a 7.5 GB machine ran out of memory when two builds overlapped. Later builds are fast. Build one frontend at a time on machines with 8 GB of RAM or less (`./build.sh` and `sh build.sh qt` are separate for that reason).

The GTK app targets libadwaita **1.5** (Ubuntu 24.04, Debian 13, Fedora 40+, Arch), so it builds on older distros too. For SVG badges in descriptions, have `librsvg` installed (it normally is on GNOME); the Qt app needs the `qt6-svg` package for the same.

## Command line

```sh
modpackmanager [file]                         # open the GUI
modpackmanager info pack.mrpack               # list mods by group and side
modpackmanager convert pack.zip pack.mpm      # import into the .mpm format
modpackmanager export pack.mpm server zip --with-common   # server pack with jars
modpackmanager export pack.mpm client mrpack              # client-only mods
modpackmanager export pack.mpm common curseforge          # mods needed on both sides
modpackmanager add pack.mpm mymod-1.2.jar --side client   # add jar files (or a folder of them)
modpackmanager mcp [--read-only]                          # MCP server for AI assistants (below)
```

Export targets: `client` (client-only mods), `common` (needed on both), `server` (server-only mods), `full`.
`--with-common` adds the common mods to a client/server export so it is installable on its own (the GUI does this by default for server exports, because a server can't start without them).
`--embed` puts jars that aren't hosted on Modrinth inside the `.mrpack` instead of linking to them.
`add` works on `.mpm` files; `--side` sets (and locks) the side, `--offline` skips the Modrinth lookup.

## AI assistants (MCP)

`modpackmanager mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server: an AI assistant starts it and talks to it over stdin/stdout, so you can ask things like "make a Fabric 1.21.1 pack with Sodium and Lithium and export a server zip".

**Claude Code:**

```sh
claude mcp add --scope user modpackmanager -- modpackmanager mcp
```

**Claude Desktop** and most other clients take JSON. In Claude Desktop, open Settings → Developer → Edit Config (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS, `%APPDATA%\Claude\claude_desktop_config.json` on Windows, `~/.config/Claude/claude_desktop_config.json` on the Linux beta), add the server and restart the app:

```json
{
  "mcpServers": {
    "modpackmanager": {
      "command": "/full/path/to/modpackmanager",
      "args": ["mcp"]
    }
  }
}
```

Use the full path (`command -v modpackmanager`): Claude Desktop starts servers with a minimal `PATH`. On Nix, install the package (see above) instead of pointing the client at `nix run`, which can take long enough to build that the client gives up.

| Tool | Does |
| --- | --- |
| `pack_info`, `list_mods` | Summarise a pack or list its mods (ids, sides, groups); also reads `.mrpack` and CurseForge `.zip` |
| `search_mods` | Search Modrinth (and CurseForge with an API key), filtered by a pack's Minecraft version and loader |
| `create_pack`, `import_pack` | Make a new `.mpm`, or turn a `.mrpack`/`.zip` into one |
| `update_pack_info` | Change name, version, author, summary or loader version |
| `add_mod`, `add_jars` | Add Modrinth/CurseForge projects (with required dependencies) or local `.jar` files |
| `remove_mods`, `set_side`, `move_to_group` | Edit mods; sides set this way are locked |
| `export_pack` | Export client/common/server/full as `.mrpack`, CurseForge `.zip` or server `.zip` |

- Only `.mpm` files are edited, and every change is saved straight away. There is no undo.
- No existing file is replaced unless the assistant passes `overwrite: true`, and an export never replaces its own pack.
- The server can read and write wherever your user account can, at the paths the assistant gives it. `modpackmanager mcp --read-only` offers only `pack_info`, `list_mods` and `search_mods`.
- If the pack is open in the app, the app reloads it within about two seconds. With unsaved changes it asks first, and saving over the assistant's changes asks too.
- It speaks MCP 2026-07-28 and the older initialize-based revisions back to 2024-11-05, over stdio only.

## CurseForge API key

CurseForge's API refuses requests without a key. Get a free one at <https://console.curseforge.com/> and enter it in Preferences, or set `MPM_CURSEFORGE_API_KEY` (also `CF_API_KEY`). It's stored with owner-only permissions in `~/.config/modpackmanager/config.json`.

Without a key, CurseForge packs still open and save, but their mods only show as project IDs, and CurseForge search is disabled.

## How sides are decided

1. A side you set by hand always wins (stored as `sideLocked`).
2. A one-sided `env` in an imported `.mrpack` is kept (the pack author chose it).
3. Modrinth's version `environment` field, then the project's `client_side`/`server_side`.
4. CurseForge's `Client`/`Server` file tags; CurseForge files are also matched on Modrinth by SHA-1 to get better side data.
5. For a jar you add (or one an imported pack bundles in `overrides/mods/`): Fabric's and Quilt's `environment` field. Forge metadata has no such field, so Forge jars start as *Unknown*. A jar in `client-overrides/` or `server-overrides/` keeps that side.
6. Otherwise *Unknown*, which exports as common (the safe choice). Server exports warn about every unknown mod.

## The `.mpm` format

A zip archive:

```
modpackmanager.json    {"format":"modpackmanager","formatVersion":1, name, version, minecraft, loader, loaderVersion, groups[], mods[]}
files/<sha1>/<name>    jar files the pack stores itself
overrides/             files for client and server
client-overrides/      client-only files
server-overrides/      server-only files
```

Each mod stores its name, side (+ whether you locked it), group, notes, file path/hashes/download URLs, and its Modrinth and/or CurseForge IDs; a stored jar also records its mod id, version and loaders. Stored jars are checked against their SHA-1 when the pack is opened. A pack that stores jars is written as `formatVersion` 2, so older versions of the app refuse it instead of dropping the jars; other packs stay at 1. Jars that an imported pack keeps in an `overrides/mods/` folder are turned into mod entries the same way. Saves are atomic (temp file + rename).

## Releases and CI

`.github/workflows/ci.yml` tests on every push and pull request, builds the Linux tarballs (GTK and Qt, each smoke-tested headless), the experimental Windows zip, and runs `nix flake check`.

The version number lives in one place, `internal/version/VERSION` (it feeds `modpackmanager version`, the About dialog, the Nix package and the release file names). To release:

1. Edit `internal/version/VERSION` and commit.
2. `git tag v<VERSION>` (it must match the file, the workflow checks) and `git push origin main v<VERSION>`.
3. The workflow builds everything and creates a **draft** release with the files and `SHA256SUMS` attached. Review it on GitHub, then publish it. Nothing is public until you do.

Running the workflow by hand (Actions → CI → Run workflow) does everything except create the release, so it works as a dry run. If a release attempt goes wrong, delete the draft and the tag (`git push --delete origin v<VERSION>`) and start over. `.github/dependabot.yml` keeps the SHA-pinned actions current.

## Known limitations

- **Windows and macOS builds are not verified.** The Windows zip comes from a CI job that has never run on a real runner; if it fails it is left out of the release (with a warning) rather than blocking it. There is no macOS release at all.
- The Linux tarballs and the Nix package are built and tested in CI; on desktops they have only been tried on X11 so far.
- **Modrinth's website only accepts `.mrpack` files whose downloads come from Modrinth, GitHub or GitLab.** Mods only on CurseForge are linked to CurseForge's CDN, which launchers like Prism accept but modrinth.com rejects. Use `--embed` (or the Embed option) to bundle those jars, but note that redistributing jars may break a mod's license or the author's CurseForge distribution setting.
- CurseForge sometimes hides a file's download URL (author opted out of third-party downloads); the app falls back to the CDN URL, which can still fail for those files. Failed downloads are listed after an export.
- Modrinth's side metadata is set by mod authors and is sometimes wrong; check the server export warnings and lock sides by hand where needed.
- **Jars you add are bundled into every export.** That is fine for your own mods and private packs, but redistributing someone else's jar may break its license. Added jars are only matched against Modrinth (by SHA-1): a jar that is only on CurseForge is stored, not linked.
