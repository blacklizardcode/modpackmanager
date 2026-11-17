#!/bin/sh
# Modpack Manager build helper.
#   ./build.sh            launcher + GTK app (GNOME and most Linux desktops)
#   ./build.sh qt         Qt app (KDE Plasma, Windows, macOS)
#   ./build.sh all        launcher + both apps
#   ./build.sh test       run the tests
#   ./build.sh install    install bin/* to ~/.local (PREFIX=/usr/local for system-wide)
set -eu
cd "$(dirname "$0")"
PREFIX=${PREFIX:-$HOME/.local}
cli() { go build -o bin/modpackmanager ./cmd/modpackmanager; }
gtk() { go build -o bin/modpackmanager-gtk ./cmd/modpackmanager-gtk; }
qt()  { go build -o bin/modpackmanager-qt ./cmd/modpackmanager-qt; }
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
