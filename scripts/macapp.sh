#!/bin/sh
# Build qslotter.app (universal binary) from the darwin builds in dist/.
# Run scripts/release.sh first. Ad-hoc signed only: on another Mac the first
# start needs System Settings > Privacy & Security > "Open Anyway".
set -eu
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT=dist
APP="$OUT/qslotter.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

lipo -create -output "$APP/Contents/MacOS/qslotter" \
	"$OUT/qslotter-darwin-arm64" "$OUT/qslotter-darwin-amd64"

# Icon: drawn at 1024 px (scripts/iconpng), scaled with sips, packed with
# iconutil (both ship with macOS).
ICONSET="$(mktemp -d)/qslotter.iconset"
mkdir -p "$ICONSET"
go run ./scripts/iconpng -size 1024 -o "$ICONSET/base.png"
for s in 16 32 128 256 512; do
	sips -z $s $s "$ICONSET/base.png" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
	d=$((s * 2))
	sips -z $d $d "$ICONSET/base.png" --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
rm "$ICONSET/base.png"
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/qslotter.icns"

sed "s/@VERSION@/$VERSION/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"
codesign --force --deep --sign - "$APP"
ditto -c -k --keepParent "$APP" "$OUT/qslotter-$VERSION-macos.zip"
echo "done: $APP and $OUT/qslotter-$VERSION-macos.zip"
