#!/bin/bash
#
# Regenerates the raster branding assets from assets/logo.svg.
#
# Run this after changing the logo. The PNGs are committed, so CI never needs a renderer.
#
# WARNING: do not use ImageMagick alone for this. `magick logo.svg logo.png` appears to succeed
# but silently produces a blank square unless librsvg is installed — ImageMagick's built-in MSVG
# renderer draws neither strokes nor circles. This script uses headless Chrome, which is a real
# SVG engine and is present on every machine that has a browser.
#
set -euo pipefail

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[ -x "$CHROME" ] || {
    echo "render-assets: no Chrome at '$CHROME'. Set CHROME=/path/to/a/chromium binary." >&2
    exit 1
}

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/assets"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

render() {
    local size="$1" out="$2"
    # Chrome renders at the SVG's own width/height, so restamp them for each target size.
    sed -E "s/width=\"[0-9]+\" height=\"[0-9]+\"/width=\"$size\" height=\"$size\"/" logo.svg \
        > "$work/in.svg"
    "$CHROME" --headless --disable-gpu --hide-scrollbars \
        --default-background-color=00000000 \
        --screenshot="$work/out.png" --window-size="$size,$size" \
        "file://$work/in.svg" >/dev/null 2>&1
    [ -s "$work/out.png" ] || { echo "render-assets: Chrome produced nothing for $out" >&2; exit 1; }
    mv "$work/out.png" "$out"
    echo "  $out (${size}x${size})"
}

cp logo.svg favicon.svg
echo "  favicon.svg (copy of logo.svg)"
render 512 logo.png
render 128 favicon.png
render 180 apple-touch-icon.png

# A blank render is the failure mode this script exists to prevent, so check for it.
for f in logo.png favicon.png apple-touch-icon.png; do
    colours=$(magick "$f" -format "%k" info: 2>/dev/null || echo 0)
    [ "$colours" -gt 2 ] || {
        echo "render-assets: $f has $colours distinct colours — the icon did not render" >&2
        exit 1
    }
done
echo "  all rasters contain more than a flat fill"
