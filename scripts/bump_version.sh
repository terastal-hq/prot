#!/usr/bin/env bash
#
# Bump version across all project files:
# - internal/daemon/daemon.go
# - apps/desktop/package.json
# - apps/desktop/package-lock.json
# - apps/desktop/src-tauri/Cargo.toml
# - apps/desktop/src-tauri/tauri.conf.json
# - schemas/openapi.yaml
#
# Usage:
#   scripts/bump_version.sh <patch|minor|major|x.y.z>
#
set -euo pipefail

if [ $# -lt 1 ]; then
    echo "Usage: $0 <patch|minor|major|x.y.z>"
    exit 1
fi

BUMP_TYPE="$1"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Read current version from internal/daemon/daemon.go
CURRENT_VERSION=$(grep -E 'const Version = ' "$ROOT_DIR/internal/daemon/daemon.go" | sed -E 's/.*"([^"]+)".*/\1/')
echo "Current version: $CURRENT_VERSION"

# Compute new version
IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

case "$BUMP_TYPE" in
    patch)
        PATCH=$((PATCH + 1))
        NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
        ;;
    minor)
        MINOR=$((MINOR + 1))
        PATCH=0
        NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
        ;;
    major)
        MAJOR=$((MAJOR + 1))
        MINOR=0
        PATCH=0
        NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
        ;;
    *)
        # Custom version string e.g. 0.2.1
        NEW_VERSION="${BUMP_TYPE#v}"
        if ! [[ "$NEW_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo "error: '$BUMP_TYPE' is not patch, minor, major or a version like 0.2.1" >&2
            exit 1
        fi
        ;;
esac

echo "Target version:  $NEW_VERSION"

# 1. Update internal/daemon/daemon.go
sed -i -E "s/const Version = \"[^\"]+\"/const Version = \"$NEW_VERSION\"/" "$ROOT_DIR/internal/daemon/daemon.go"

# 2. Update apps/desktop/package.json
sed -i -E "s/\"version\": \"[^\"]+\"/\"version\": \"$NEW_VERSION\"/" "$ROOT_DIR/apps/desktop/package.json"

# 3. Update apps/desktop/package-lock.json (if present)
if [ -f "$ROOT_DIR/apps/desktop/package-lock.json" ]; then
    sed -i -E "0,/\"version\": \"[^\"]+\"/s/\"version\": \"[^\"]+\"/\"version\": \"$NEW_VERSION\"/" "$ROOT_DIR/apps/desktop/package-lock.json"
fi

# 4. Update apps/desktop/src-tauri/Cargo.toml
sed -i -E "s/^version = \"[^\"]+\"/version = \"$NEW_VERSION\"/" "$ROOT_DIR/apps/desktop/src-tauri/Cargo.toml"

# 5. Update apps/desktop/src-tauri/tauri.conf.json
sed -i -E "s/\"version\": \"[^\"]+\"/\"version\": \"$NEW_VERSION\"/" "$ROOT_DIR/apps/desktop/src-tauri/tauri.conf.json"

# 6. Update schemas/openapi.yaml
sed -i -E "s/version: [0-9]+\.[0-9]+\.[0-9]+/version: $NEW_VERSION/" "$ROOT_DIR/schemas/openapi.yaml"

echo "✓ Updated version to $NEW_VERSION across all project manifests:"
echo "  • internal/daemon/daemon.go"
echo "  • apps/desktop/package.json"
echo "  • apps/desktop/src-tauri/Cargo.toml"
echo "  • apps/desktop/src-tauri/tauri.conf.json"
echo "  • schemas/openapi.yaml"
