#!/bin/sh
# Install Modpack Manager from an unpacked release tarball.
#   ./install.sh                      install to ~/.local
#   PREFIX=/usr/local ./install.sh    system-wide (run as root)
set -eu
here=$(cd "$(dirname "$0")" && pwd)
PREFIX=${PREFIX:-$HOME/.local}

for f in "$here"/bin/*; do
	install -Dm755 "$f" "$PREFIX/bin/$(basename "$f")"
done
for f in "$here"/share/applications/*.desktop; do
	install -Dm644 "$f" "$PREFIX/share/applications/$(basename "$f")"
done
for f in "$here"/share/mime/packages/*.xml; do
	install -Dm644 "$f" "$PREFIX/share/mime/packages/$(basename "$f")"
done
update-mime-database "$PREFIX/share/mime" >/dev/null 2>&1 || true
update-desktop-database "$PREFIX/share/applications" >/dev/null 2>&1 || true

echo "Installed to $PREFIX"
case ":$PATH:" in
*":$PREFIX/bin:"*) ;;
*) echo "Note: $PREFIX/bin is not on your PATH." ;;
esac
