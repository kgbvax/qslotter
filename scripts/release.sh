#!/bin/sh
# Build qslotter for every desktop target from one machine (no cgo needed:
# glaze/native bind the OS WebView and tray through purego).
#
#   scripts/release.sh [version]      -> dist/
#
# Windows gets -H windowsgui (no console window; logs go to
# %LOCALAPPDATA%\qslotter\qslotter.log) and, if go-winres is reachable, the
# exe icon/version resource. macOS: run scripts/macapp.sh afterwards for the
# .app bundle.
set -eu
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT=dist
rm -rf "$OUT"
mkdir -p "$OUT"

LDFLAGS="-s -w"
build() { # goos goarch outname [extra ldflags]
	echo "build $1/$2"
	CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath \
		-ldflags "$LDFLAGS ${4:-}" -o "$OUT/$3" ./cmd/qslotter
}

# Windows icon + version info (optional: needs network for go-winres once).
if go run github.com/tc-hib/go-winres@latest make --in winres/winres.json \
	--product-version "$VERSION" --file-version "$VERSION" >/dev/null 2>&1; then
	echo "winres: icon/version resource embedded"
else
	echo "winres: skipped (go-winres not reachable) - exe without icon"
fi

build darwin  arm64 qslotter-darwin-arm64
build darwin  amd64 qslotter-darwin-amd64
build windows amd64 qslotter-windows-amd64.exe "-H windowsgui"
build linux   amd64 qslotter-linux-amd64
build linux   arm64 qslotter-linux-arm64
rm -f rsrc_windows_*.syso

# Linux: binary + desktop entry + icon.
for arch in amd64 arm64; do
	d="$OUT/qslotter-linux-$arch-pkg"
	mkdir -p "$d"
	cp "$OUT/qslotter-linux-$arch" "$d/qslotter"
	cp packaging/linux/qslotter.desktop packaging/linux/qslotter.png "$d/"
	tar -C "$d" -czf "$OUT/qslotter-$VERSION-linux-$arch.tar.gz" .
	rm -rf "$d"
done

(cd "$OUT" && shasum -a 256 qslotter-* > SHA256SUMS)
echo "done: $OUT ($VERSION)"
