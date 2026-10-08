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

BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BI=github.com/dl9et/qslotter/internal/buildinfo
LDFLAGS="-s -w -X $BI.Version=$VERSION -X $BI.Date=$BUILT"
build() { # goos goarch outname [extra ldflags]
	echo "build $1/$2"
	CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath \
		-ldflags "$LDFLAGS ${4:-}" -o "$OUT/$3" ./cmd/qslotter
}

# Windows icon, version info and manifest (DPI awareness). The .syso must sit
# next to the main package to be linked. Optional: needs network for
# go-winres once.
WINRES=github.com/tc-hib/go-winres@v0.3.3
rm -f cmd/qslotter/rsrc_windows_*.syso
if go run "$WINRES" make --in winres/winres.json --out cmd/qslotter/rsrc --arch amd64 \
	--product-version "$VERSION" --file-version "$VERSION"; then
	echo "winres: icon/version/manifest resource embedded"
else
	echo "winres: FAILED - the Windows exe gets no icon, version info or DPI manifest"
fi

build darwin  arm64 qslotter-darwin-arm64
build darwin  amd64 qslotter-darwin-amd64
build windows amd64 qslotter-windows-amd64.exe "-H windowsgui"
build linux   amd64 qslotter-linux-amd64
build linux   arm64 qslotter-linux-arm64
rm -f cmd/qslotter/rsrc_windows_*.syso

# Linux: binary + desktop entry + icon.
# One top-level folder in the archive (never a "./" entry: GNU tar would
# apply its mode to the directory the user extracts into).
for arch in amd64 arm64; do
	name="qslotter-$VERSION-linux-$arch"
	d="$OUT/$name"
	mkdir -p "$d"
	cp "$OUT/qslotter-linux-$arch" "$d/qslotter"
	cp packaging/linux/qslotter.desktop packaging/linux/qslotter.png "$d/"
	tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
	rm -rf "$d"
done

(cd "$OUT" && shasum -a 256 qslotter-* > SHA256SUMS)
echo "done: $OUT ($VERSION)"
