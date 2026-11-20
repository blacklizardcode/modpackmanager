#!/usr/bin/env bash
# Recompute nix/vendor-hash.txt, the hash of the vendored Go modules.
# Run it once before the first `nix build`, and again whenever go.mod or go.sum
# change.
#   bash nix/update-vendor-hash.sh
set -euo pipefail
cd "$(dirname "$0")/.."

if [ ! -s go.sum ]; then
	echo "go.sum is missing: run 'go mod tidy' first." >&2
	exit 1
fi
if command -v go >/dev/null 2>&1 && ! go mod tidy -diff >/dev/null 2>&1; then
	echo "go.mod/go.sum are incomplete or not tidy: run 'go mod tidy' and commit the result." >&2
	exit 1
fi

# Flakes in a git repository only see files git knows about.
git add -N go.sum 2>/dev/null || true
: >nix/vendor-hash.txt
git add -N nix/vendor-hash.txt 2>/dev/null || true

# With the hash file empty the package uses lib.fakeHash, so the build fails
# and prints the real value.
log=$(nix build .#modpackmanager.goModules --no-link 2>&1 || true)
hash=$(printf '%s\n' "$log" | sed -n 's|^[[:space:]]*got:[[:space:]]*\(sha256-[A-Za-z0-9+/=]*\).*$|\1|p' | head -n 1)
if [ -z "$hash" ]; then
	printf '%s\n' "$log" >&2
	echo "Could not read a hash from the nix output above." >&2
	exit 1
fi

printf '%s\n' "$hash" >nix/vendor-hash.txt
echo "nix/vendor-hash.txt: $hash"
echo "Commit it together with go.mod and go.sum."
