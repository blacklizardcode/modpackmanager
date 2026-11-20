#!/bin/sh
# Modpack Manager build helper.
#   sh build.sh           launcher + GTK app (GNOME and most Linux desktops)
#   sh build.sh qt        Qt app (KDE Plasma, Windows, macOS)
#   sh build.sh all       launcher + both apps
#   sh build.sh test      run the tests
#   sh build.sh install   install bin/* to ~/.local (PREFIX=/usr/local for system-wide)
# LDFLAGS="-s -w" sh build.sh ...  strip the binaries (release builds do this)
set -eu
cd "$(dirname "$0")"
PREFIX=${PREFIX:-$HOME/.local}
gobuild() { go build ${LDFLAGS:+-ldflags "$LDFLAGS"} -o "bin/$1" "./cmd/$1"; }
cli() { gobuild modpackmanager; }
gtk() { gobuild modpackmanager-gtk; }
qt()  { gobuild modpackmanager-qt; }
case "${1:-default}" in
  default) cli; gtk ;;
  gtk) gtk ;;
  qt) cli; qt ;;
  all) cli; gtk; qt ;;
  test) go test ./internal/... ./cmd/modpackmanager/ ;;
  install)
    for b in modpackmanager modpackmanager-gtk modpackmanager-qt; do
      [ -f "bin/$b" ] && install -Dm755 "bin/$b" "$PREFIX/bin/$b"
    done
    install -Dm644 data/io.github.blakka.ModpackManager.desktop "$PREFIX/share/applications/io.github.blakka.ModpackManager.desktop"
    install -Dm644 data/modpackmanager-mime.xml "$PREFIX/share/mime/packages/modpackmanager.xml"
    update-mime-database "$PREFIX/share/mime" 2>/dev/null || true
    update-desktop-database "$PREFIX/share/applications" 2>/dev/null || true
    echo "Installed to $PREFIX" ;;
  *) sed -n '2,8p' "$0"; exit 1 ;;
esac
