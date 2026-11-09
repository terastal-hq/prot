#!/usr/bin/env bash
#
# Regenerate every app icon from the single source of truth: design/icon.png
#
# Outputs:
#   apps/desktop/src-tauri/icons/       Tauri bundle + window icons (png, icns)
#   packaging/linux/icons/<N>x<N>/      hicolor sizes for the Linux .desktop entry
#
# Requires: python3 with Pillow, and npm dependencies installed in apps/desktop.
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE="$ROOT_DIR/design/icon.png"
TAURI_ICONS="$ROOT_DIR/apps/desktop/src-tauri/icons"
LINUX_ICONS="$ROOT_DIR/packaging/linux/icons"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

if [ ! -f "$SOURCE" ]; then
    echo "missing $SOURCE" >&2
    exit 1
fi

# 1. Pad the source to a transparent square (icons must be square).
python3 - "$SOURCE" "$TMP_DIR/square.png" << 'EOF'
import sys
from PIL import Image

src = Image.open(sys.argv[1]).convert("RGBA")
w, h = src.size
side = int(max(w, h) * 1.06)
canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
canvas.alpha_composite(src, ((side - w) // 2, (side - h) // 2))
canvas.resize((1024, 1024), Image.LANCZOS).save(sys.argv[2])
EOF

# 2. Tauri icon set.
(cd "$ROOT_DIR/apps/desktop" && npx --no-install tauri icon "$TMP_DIR/square.png" --output "$TMP_DIR/tauri" >/dev/null 2>&1)
mkdir -p "$TAURI_ICONS"
for f in 32x32.png 128x128.png "128x128@2x.png" icon.png icon.icns; do
    cp -f "$TMP_DIR/tauri/$f" "$TAURI_ICONS/$f"
done

# 3. Linux hicolor sizes.
python3 - "$TMP_DIR/square.png" "$LINUX_ICONS" << 'EOF'
import os
import sys
from PIL import Image

src = Image.open(sys.argv[1])
for size in (16, 24, 32, 48, 64, 128, 256, 512):
    out = os.path.join(sys.argv[2], f"{size}x{size}")
    os.makedirs(out, exist_ok=True)
    src.resize((size, size), Image.LANCZOS).save(os.path.join(out, "prot-desktop.png"))
EOF

echo "icons regenerated from design/icon.png"
