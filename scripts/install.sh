#!/usr/bin/env bash
#
# prot installer script
# Installs prot CLI and protd background daemon.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/terastal-hq/prot/main/scripts/install.sh | bash
#
set -euo pipefail

REPO="terastal-hq/prot"
INSTALL_DIR="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
APPLICATIONS_DIR="${HOME}/.local/share/applications"
ICONS_DIR="${HOME}/.local/share/icons/hicolor/128x128/apps"

BOLD='\033[1m'
CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${CYAN}${BOLD}"
cat << 'EOF'
                 _   
  _ __  _ __ ___| |_ 
 | '_ \| '__/ _ \ __|
 | |_) | | | (_) | |_ 
 | .__/|_|  \___/ \__|
 |_|  Your development servers, under control.
EOF
echo -e "${NC}"

# 1. Detect OS and Architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64|amd64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        echo -e "${RED}Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

case "$OS" in
    linux)
        ;;
    darwin)
        ;;
    *)
        echo -e "${RED}Unsupported operating system: $OS${NC}"
        exit 1
        ;;
esac

echo -e "Detected platform: ${BOLD}${OS}/${ARCH}${NC}"

# 2. Prepare directories
mkdir -p "$INSTALL_DIR"

# 3. Determine if building from local repo or downloading release
TMP_DIR=$(mktemp -d)
cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

if [ -f "./cmd/prot/main.go" ] && [ -f "./cmd/protd/main.go" ] && command -v go >/dev/null 2>&1; then
    echo -e "\n${CYAN}Building from local repository...${NC}"
    go build -ldflags="-s -w" -o "$INSTALL_DIR/prot" ./cmd/prot
    go build -ldflags="-s -w" -o "$INSTALL_DIR/protd" ./cmd/protd
    chmod +x "$INSTALL_DIR/prot" "$INSTALL_DIR/protd"
    echo -e "${GREEN}✓ Compiled and installed prot and protd to $INSTALL_DIR${NC}"
else
    echo -e "\n${CYAN}Downloading latest release from GitHub...${NC}"
    LATEST_TAG=$(curl -s "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || echo "")
    
    if [ -z "$LATEST_TAG" ]; then
        LATEST_TAG="v0.1.0"
    fi

    ARCHIVE_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/prot_${OS}_${ARCH}.tar.gz"
    echo "Fetching: $ARCHIVE_URL"

    if curl -s -f -L -o "$TMP_DIR/prot.tar.gz" "$ARCHIVE_URL"; then
        tar -xzf "$TMP_DIR/prot.tar.gz" -C "$TMP_DIR"
        mv "$TMP_DIR/prot" "$INSTALL_DIR/prot"
        mv "$TMP_DIR/protd" "$INSTALL_DIR/protd"
        chmod +x "$INSTALL_DIR/prot" "$INSTALL_DIR/protd"
        echo -e "${GREEN}✓ Downloaded and installed prot binaries to $INSTALL_DIR${NC}"
    elif command -v go >/dev/null 2>&1; then
        echo -e "${YELLOW}Pre-built binary not found for ${LATEST_TAG}; building via Go toolchain...${NC}"
        go install github.com/${REPO}/cmd/prot@latest
        go install github.com/${REPO}/cmd/protd@latest
        GOPATH_BIN="$(go env GOPATH)/bin"
        cp -f "$GOPATH_BIN/prot" "$INSTALL_DIR/prot"
        cp -f "$GOPATH_BIN/protd" "$INSTALL_DIR/protd"
        chmod +x "$INSTALL_DIR/prot" "$INSTALL_DIR/protd"
        echo -e "${GREEN}✓ Built and installed prot to $INSTALL_DIR${NC}"
    else
        echo -e "${RED}Failed to download binary and Go compiler is not installed.${NC}"
        echo "Please visit https://github.com/${REPO}/releases to download manually."
        exit 1
    fi
fi

# 4. Desktop binary and icons (if available)
if [ -f "apps/desktop/src-tauri/target/release/prot-desktop" ]; then
    cp -f "apps/desktop/src-tauri/target/release/prot-desktop" "$INSTALL_DIR/prot-desktop"
    chmod +x "$INSTALL_DIR/prot-desktop"
    echo -e "${GREEN}✓ Desktop binary installed: $INSTALL_DIR/prot-desktop${NC}"
elif [ -f "apps/desktop/src-tauri/target/debug/prot-desktop" ]; then
    cp -f "apps/desktop/src-tauri/target/debug/prot-desktop" "$INSTALL_DIR/prot-desktop"
    chmod +x "$INSTALL_DIR/prot-desktop"
    echo -e "${GREEN}✓ Desktop binary installed: $INSTALL_DIR/prot-desktop${NC}"
fi

if [ -f "$INSTALL_DIR/prot-desktop" ] && [ "$OS" = "linux" ]; then
    mkdir -p "$APPLICATIONS_DIR" "$ICONS_DIR"
    if [ -f "apps/desktop/src-tauri/icons/128x128.png" ]; then
        cp -f "apps/desktop/src-tauri/icons/128x128.png" "$ICONS_DIR/prot.png"
    fi
    cat > "$APPLICATIONS_DIR/prot.desktop" << EOF
[Desktop Entry]
Name=prot
Comment=Your development servers, under control
Exec=$INSTALL_DIR/prot-desktop
Icon=prot
Terminal=false
Type=Application
Categories=Development;
EOF
    echo -e "${GREEN}✓ Application menu shortcut registered${NC}"
fi

# 5. Background daemon setup (systemd for Linux, launchd for macOS)
if [ "$OS" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
    mkdir -p "$SYSTEMD_USER_DIR"
    cat > "$SYSTEMD_USER_DIR/protd.service" << EOF
[Unit]
Description=prot development control plane daemon
After=network.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/protd -gateway 127.0.0.1:8080 -admin 127.0.0.1:4400
Restart=always
RestartSec=2s
LimitNOFILE=65535

[Install]
WantedBy=default.target
EOF
    systemctl --user daemon-reload
    systemctl --user enable --now protd >/dev/null 2>&1 || true
    echo -e "${GREEN}✓ Background systemd user service enabled and started${NC}"
elif [ "$OS" = "darwin" ]; then
    LAUNCHD_DIR="${HOME}/Library/LaunchAgents"
    mkdir -p "$LAUNCHD_DIR"
    cat > "$LAUNCHD_DIR/dev.prot.protd.plist" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.prot.protd</string>
    <key>ProgramArguments</key>
    <array>
        <string>$INSTALL_DIR/protd</string>
        <string>-gateway</string>
        <string>127.0.0.1:8080</string>
        <string>-admin</string>
        <string>127.0.0.1:4400</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
EOF
    launchctl load -w "$LAUNCHD_DIR/dev.prot.protd.plist" 2>/dev/null || true
    echo -e "${GREEN}✓ Background launchd agent enabled and started${NC}"
fi

# 6. Check PATH
case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        ;;
    *)
        echo -e "\n${YELLOW}Notice:${NC} $INSTALL_DIR is not in your current PATH."
        echo "Add the following line to your ~/.bashrc or ~/.zshrc:"
        echo -e "  ${BOLD}export PATH=\"\$HOME/.local/bin:\$PATH\"${NC}"
        ;;
esac

# 7. Verification check
echo -e "\n${CYAN}Running diagnostics:${NC}"
"$INSTALL_DIR/prot" doctor || true

echo -e "\n${GREEN}${BOLD}prot installed successfully!${NC}"
echo -e "Quick start commands:"
echo -e "  • ${BOLD}prot status${NC}     - View daemon status and active routes"
echo -e "  • ${BOLD}prot discover${NC}   - Discover listening development servers"
echo -e "  • ${BOLD}prot add --help${NC} - Register a managed service"
if [ -f "$INSTALL_DIR/prot-desktop" ]; then
echo -e "  • ${BOLD}prot-desktop${NC}    - Launch desktop GUI"
fi
