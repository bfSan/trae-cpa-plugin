#!/usr/bin/env bash
# Build only this fork's Trae provider. A target C compiler is required for cross builds.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGET_OS="${1:-$(go env GOOS)}"
TARGET_ARCH="${2:-$(go env GOARCH)}"
case "$TARGET_OS" in linux) EXT=so ;; darwin) EXT=dylib ;; windows) EXT=dll ;; *) echo "Unsupported target OS" >&2; exit 2 ;; esac
case "$TARGET_ARCH" in amd64|arm64) ;; *) echo "Unsupported target architecture" >&2; exit 2 ;; esac
OUT_DIR="$ROOT_DIR/dist/$TARGET_OS-$TARGET_ARCH"
mkdir -p "$OUT_DIR"
VERSION="$(tr -d '[:space:]' < "$ROOT_DIR/plugins/trae/VERSION")"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "Invalid VERSION" >&2; exit 2; }
export CGO_ENABLED=1 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH"
# Respect an explicitly supplied cross compiler; never silently overwrite CC.
if [[ -z "${CC:-}" ]]; then
 case "$TARGET_OS" in darwin) export CC=clang ;; windows) export CC=x86_64-w64-mingw32-gcc ;; *) export CC=gcc ;; esac
fi
(cd "$ROOT_DIR/plugins/trae" && go build -buildmode=c-shared -trimpath -ldflags="-s -w -X main.version=$VERSION" -o "$OUT_DIR/trae.$EXT" .)
printf 'Built trae-cpa-plugin %s: %s/trae.%s\n' "$VERSION" "$OUT_DIR" "$EXT"
# Store asset: exactly one library at archive root, matching CPA's resolver.
python3 "$ROOT_DIR/scripts/package.py" "$VERSION" "$TARGET_OS" "$TARGET_ARCH"
