#!/bin/sh
# Build a Windows release zip (Qt frontend + launcher + every DLL they need).
#   sh packaging/windows-zip.sh [outdir]
# Run it inside an MSYS2 UCRT64 shell with these packages installed:
#   mingw-w64-ucrt-x86_64-{go,gcc,pkgconf,qt6-base,qt6-svg}  and  zip
# Output: <outdir>/modpackmanager-<version>-windows-x86_64-qt.zip
#
# EXPERIMENTAL: nothing here has run on Windows yet. The deployment step is
# windeployqt (Qt's own tool) plus an ldd pass that copies the remaining
# non-Qt DLLs (ICU, harfbuzz, ...) and a final check that nothing is missing.
set -eu
cd "$(dirname "$0")/.."

out=${1:-dist}
version=$(tr -d '[:space:]' <internal/version/VERSION)
name="modpackmanager-$version-windows-x86_64-qt"
stage="$out/$name"
ucrt=${MSYSTEM_PREFIX:-/ucrt64}

export CGO_ENABLED=1
mkdir -p bin
# The launcher stays a console program so `modpackmanager.exe export ...` works
# in a terminal; the Qt frontend is a GUI program (no console window).
go build -trimpath -ldflags '-s -w' -o bin/modpackmanager.exe ./cmd/modpackmanager
go build -trimpath -ldflags '-s -w -H windowsgui' -o bin/modpackmanager-qt.exe ./cmd/modpackmanager-qt

rm -rf "$stage"
mkdir -p "$stage"
cp bin/modpackmanager.exe bin/modpackmanager-qt.exe "$stage/"

# Qt DLLs, plugins (platforms/qwindows.dll, styles, image formats) and the
# MinGW runtime. The app doesn't link QtSvg, but it needs the SVG image plugin
# at runtime for badges in mod descriptions, so add it by hand.
windeployqt6 --release --no-translations --no-system-d3d-compiler --no-opengl-sw \
	"$stage/modpackmanager-qt.exe"
cp "$ucrt/bin/Qt6Svg.dll" "$stage/"
mkdir -p "$stage/imageformats" "$stage/iconengines"
cp "$ucrt/share/qt6/plugins/imageformats/qsvg.dll" "$stage/imageformats/"
cp "$ucrt/share/qt6/plugins/iconengines/qsvgicon.dll" "$stage/iconengines/"

# Copy every remaining MSYS2 DLL that something in the stage still imports,
# until nothing new turns up (copied DLLs have dependencies of their own).
pass=0
while :; do
	pass=$((pass + 1))
	find "$stage" \( -name '*.exe' -o -name '*.dll' \) -print0 |
		xargs -0 -n1 ldd 2>/dev/null |
		sed -n "s|^.*=> \($ucrt/bin/[^ ]*\) .*\$|\1|p" |
		sort -u >"$out/.deps.txt"
	added=0
	while IFS= read -r dll; do
		if [ ! -e "$stage/$(basename "$dll")" ]; then
			cp "$dll" "$stage/"
			added=$((added + 1))
		fi
	done <"$out/.deps.txt"
	[ "$added" -gt 0 ] || break
	[ "$pass" -lt 8 ] || {
		echo "dependency copy did not converge" >&2
		exit 1
	}
done
rm -f "$out/.deps.txt"

# Everything must now resolve inside the stage or in Windows itself.
missing=$(find "$stage" \( -name '*.exe' -o -name '*.dll' \) -print0 |
	xargs -0 -n1 ldd 2>/dev/null | grep -i 'not found' || true)
if [ -n "$missing" ]; then
	echo "unresolved DLLs:" >&2
	echo "$missing" >&2
	exit 1
fi

cat >"$stage/README.txt" <<EOF
Modpack Manager $version (Windows, x86_64, Qt frontend)

Run modpackmanager-qt.exe. Command-line use: modpackmanager.exe help
No installer: unzip anywhere and run. To remove it, delete the folder.
EOF

(cd "$out" && rm -f "$name.zip" && zip -qr -X "$name.zip" "$name")
rm -rf "$stage"
echo "$out/$name.zip"
