#!/bin/sh
# Smoke-test a Linux release tarball the way a user would use it: unpack it,
# install into a scratch prefix, run the CLI, check the frontend's shared
# libraries resolve, then start the app headless for a few seconds.
#   sh packaging/smoke-linux.sh dist/modpackmanager-<version>-linux-<arch>-<gtk|qt>.tar.gz
# GTK needs xvfb-run (apt: xvfb); Qt runs on its offscreen platform plugin.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)

tarball=$(realpath "${1:?usage: $0 <tarball>}")
case "$tarball" in
*-gtk.tar.gz) ui=gtk ;;
*-qt.tar.gz) ui=qt ;;
*)
	echo "can't tell the frontend from the file name: $tarball" >&2
	exit 2
	;;
esac
want=$(tr -d '[:space:]' <"$root/internal/version/VERSION")

fail() {
	echo "SMOKE TEST FAILED: $*" >&2
	exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tar -xzf "$tarball" -C "$work"
dir=$(echo "$work"/modpackmanager-*)
[ -d "$dir" ] || fail "tarball does not contain a modpackmanager-* directory"

# Keep the app away from the real home directory.
export HOME="$work/home" XDG_CONFIG_HOME="$work/home/config" XDG_CACHE_HOME="$work/home/cache" XDG_DATA_HOME="$work/home/data"
mkdir -p "$HOME"

prefix="$work/prefix"
PREFIX="$prefix" "$dir/install.sh" >/dev/null
bin="$prefix/bin"

[ "$("$bin/modpackmanager" version)" = "modpackmanager $want" ] || fail "version output is not 'modpackmanager $want'"
"$bin/modpackmanager" help >/dev/null || fail "'modpackmanager help' failed"

# The MCP server answers a request, then exits when its input ends.
mcp=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}' |
	timeout 10 "$bin/modpackmanager" mcp) || fail "'modpackmanager mcp' failed"
case "$mcp" in
*'"supportedVersions"'*) ;;
*) fail "'modpackmanager mcp' did not answer server/discover: $mcp" ;;
esac

[ -f "$prefix/share/applications/io.github.blakka.ModpackManager.desktop" ] || fail "desktop file was not installed"
[ -f "$prefix/share/mime/packages/modpackmanager.xml" ] || fail "MIME file was not installed"
if command -v desktop-file-validate >/dev/null 2>&1; then
	desktop-file-validate "$prefix/share/applications/io.github.blakka.ModpackManager.desktop" || fail "desktop file does not validate"
fi

# Every shared library the frontend needs must resolve on this machine.
if ldd "$bin/modpackmanager-$ui" | grep -q 'not found'; then
	ldd "$bin/modpackmanager-$ui" | grep 'not found' >&2
	fail "modpackmanager-$ui has unresolved shared libraries"
fi

# Start it through the launcher; it must still be running when timeout stops it.
# (timeout signals the whole process group, which includes the frontend the
# launcher started.) 124 = stopped by TERM, 137 = needed KILL.
log="$work/app.log"
set +e
case "$ui" in
gtk)
	MPM_UI=gtk GDK_BACKEND=x11 GSK_RENDERER=cairo \
		xvfb-run -a -s '-screen 0 1280x800x24' timeout -k 3 8 "$bin/modpackmanager" >"$log" 2>&1
	;;
qt)
	MPM_UI=qt QT_QPA_PLATFORM=offscreen \
		timeout -k 3 8 "$bin/modpackmanager" >"$log" 2>&1
	;;
esac
rc=$?
set -e
case "$rc" in
124 | 137) ;;
*)
	cat "$log" >&2
	fail "the $ui app exited early with status $rc"
	;;
esac

echo "smoke test passed: $(basename "$tarball")"
