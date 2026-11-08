#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TRIPLE=$(rustc -vV | sed -n 's|host: ||p')
DEST_DIR="$ROOT_DIR/apps/desktop/src-tauri/binaries"
mkdir -p "$DEST_DIR"

echo "Building Go sidecars for target triple: $TRIPLE"
(cd "$ROOT_DIR" && go build -ldflags="-s -w" -o "$DEST_DIR/prot-$TRIPLE" ./cmd/prot)
(cd "$ROOT_DIR" && go build -ldflags="-s -w" -o "$DEST_DIR/protd-$TRIPLE" ./cmd/protd)

chmod +x "$DEST_DIR/prot-$TRIPLE" "$DEST_DIR/protd-$TRIPLE"
echo "✓ Sidecar binaries ready in $DEST_DIR"
