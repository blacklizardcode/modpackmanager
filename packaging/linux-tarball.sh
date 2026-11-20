#!/bin/sh
# Build a Linux release tarball.
#   sh packaging/linux-tarball.sh <gtk|qt> [outdir]
# Builds with build.sh (stripped), then bundles the launcher, the chosen
# frontend, the desktop/MIME files and install.sh. The version comes from
# internal/version/VERSION. Output: <outdir>/modpackmanager-<version>-linux-<arch>-<ui>.tar.gz
set -eu
cd "$(dirname "$0")/.."

ui=${1:-}
out=${2:-dist}
case "$ui" in
gtk | qt) ;;
*)
	echo "usage: $0 <gtk|qt> [outdir]" >&2
	exit 2
	;;
esac

version=$(tr -d '[:space:]' <internal/version/VERSION)
arch=$(uname -m)
name="modpackmanager-$version-linux-$arch-$ui"

export LDFLAGS="${LDFLAGS:--s -w}"
# Keep the build machine's paths out of the binaries.
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath"
if [ "$ui" = qt ]; then
	sh ./build.sh qt
	needs="Qt 6.4 or newer (Widgets)"
	pkgs="libqt6widgets6t64 libqt6svg6 qt6-wayland"
else
	sh ./build.sh default
	needs="GTK 4.14+ and libadwaita 1.5+ (Ubuntu 24.04, Fedora 40, Debian 13, Arch)"
	pkgs="libgtk-4-1 libadwaita-1-0 librsvg2-common"
fi

stage="$out/$name"
rm -rf "$stage"
mkdir -p "$stage/bin" "$stage/share/applications" "$stage/share/mime/packages"
install -m755 bin/modpackmanager "bin/modpackmanager-$ui" "$stage/bin/"
install -m644 data/io.github.blakka.ModpackManager.desktop "$stage/share/applications/"
install -m644 data/modpackmanager-mime.xml "$stage/share/mime/packages/modpackmanager.xml"
install -m755 packaging/install.sh "$stage/install.sh"

# Oldest glibc these binaries can run on: the newest symbol version they use.
glibc=
if command -v objdump >/dev/null 2>&1; then
	floor=$(objdump -T "$stage"/bin/* 2>/dev/null | grep -o 'GLIBC_[0-9][0-9.]*' | sort -Vu | tail -n 1 | sed 's/^GLIBC_//')
	[ -z "$floor" ] || glibc="
       glibc $floor or newer (the binaries are built on Ubuntu 24.04)."
fi

cat >"$stage/README.txt" <<EOF
Modpack Manager $version ($ui frontend, Linux $arch)

Install:   ./install.sh                       (to ~/.local)
           PREFIX=/usr/local ./install.sh     (system-wide, as root)
Run:       modpackmanager [pack.mrpack]
Remove:    delete bin/modpackmanager*, share/applications/io.github.blakka.ModpackManager.desktop
           and share/mime/packages/modpackmanager.xml from the install prefix.

Needs: $needs$glibc
Debian/Ubuntu: sudo apt install $pkgs
       (other distros: the equivalent packages; the SVG ones are for badges in mod descriptions)
These are dynamically linked and use your system's toolkit libraries.
On NixOS use the flake instead (see the project README).
EOF

mkdir -p "$out"
tar --sort=name --owner=0 --group=0 --numeric-owner \
	--mtime="@${SOURCE_DATE_EPOCH:-$(date +%s)}" \
	-C "$out" -czf "$out/$name.tar.gz" "$name"
rm -rf "$stage"
echo "$out/$name.tar.gz"
