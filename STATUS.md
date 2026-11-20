# Status

## Tested
- Core (formats, Modrinth/CurseForge APIs, search, add-with-dependencies, exports): `sh build.sh test`, against local mocks.
- Launcher + command line (`cmd/modpackmanager`): picks Qt on KDE/LXQt, GTK elsewhere on Linux, Qt on Windows/macOS.
- GTK4/libadwaita app and Qt 6 app: built and checked on screen on Linux (Ubuntu 24.04).
- Linux tarballs (`packaging/linux-tarball.sh`, `packaging/smoke-linux.sh`): built, installed into a scratch prefix and started headless (GTK under Xvfb, Qt offscreen) on Ubuntu 24.04. The Qt tarball was built without `-trimpath` (that compile needs about 6 GB of RAM and didn't fit next to other work); the flag is verified for the launcher and GTK only.
- `flake.nix` / `nix/package.nix`: evaluate cleanly for x86_64 and aarch64 (`nix flake check --no-build`); the Go tests pass in a sandbox-like environment (no network, no home, `-trimpath`).
- `.github/workflows/ci.yml`: action pins match their tags, inputs match each action's manifest, every `run:` block passes shellcheck.

## Never run for real
- Live Modrinth/CurseForge APIs (the build machine couldn't reach them).
- **An actual `nix build`** (the machine that wrote the flake couldn't reach the Nix cache), so `nix/vendor-hash.txt` doesn't exist yet.
- **The GitHub workflow** (no way to run Actions from there), including the draft-release step.
- **Windows** (`packaging/windows-zip.sh`, the `windows` job) and **macOS** (not included at all).

## Before the first release
0. Two 10 MB binaries are tracked in git (`modpackmanager` and `cmd/modpackmanager/modpackmanager`). Untrack them (the `.gitignore` here already ignores them): `git rm --cached modpackmanager cmd/modpackmanager/modpackmanager`.
1. `go mod tidy` and commit `go.mod` + a complete `go.sum` (the one in the repo only has two modules in it; the workflow's tidy check fails until this is done).
2. `git add` the new files, run `bash nix/update-vendor-hash.sh`, `nix flake lock`, then `nix build .#gtk .#qt` and try both apps.
3. Commit `nix/vendor-hash.txt` and `flake.lock`, push, and look at the first CI run (Actions → CI → Run workflow is a dry run of the whole pipeline).
4. Release: edit `internal/version/VERSION`, tag `v<VERSION>`, push the tag, review the draft release, publish.

You can delete the old `_wip/` folder (an early snapshot, ignored by `go build`).
