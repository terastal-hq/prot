#!/usr/bin/env bash
#
# prot installer script
# Installs prot CLI and protd background daemon.
#
# Usage:
#   curl -fsSL https://prot.adityagupta.dev/install | bash
#
set -euo pipefail

REPO="terastal-hq/prot"
INSTALL_DIR="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
APPLICATIONS_DIR="${HOME}/.local/share/applications"
ICONS_BASE="${HOME}/.local/share/icons/hicolor"

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
if [ ! -f "apps/desktop/src-tauri/target/release/prot-desktop" ] && [ -d "apps/desktop" ] && command -v cargo >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
    echo -e "\n${CYAN}Building standalone desktop app (release mode)...${NC}"
    (cd apps/desktop && npm run build && cd src-tauri && cargo build --release)
fi

if [ -f "apps/desktop/src-tauri/target/release/prot-desktop" ]; then
    cp -f "apps/desktop/src-tauri/target/release/prot-desktop" "$INSTALL_DIR/prot-desktop"
    chmod +x "$INSTALL_DIR/prot-desktop"
    echo -e "${GREEN}✓ Desktop binary installed: $INSTALL_DIR/prot-desktop${NC}"
fi

if [ -f "$INSTALL_DIR/prot-desktop" ] && [ "$OS" = "linux" ]; then
    mkdir -p "$APPLICATIONS_DIR"

    # Icon: every size generated from design/icon.png (scripts/gen_icons.sh).
    if [ -d "packaging/linux/icons" ]; then
        for size_dir in packaging/linux/icons/*/; do
            size="$(basename "$size_dir")"
            mkdir -p "$ICONS_BASE/$size/apps"
            cp -f "$size_dir/prot-desktop.png" "$ICONS_BASE/$size/apps/prot-desktop.png"
        done
    else
        mkdir -p "$ICONS_BASE/512x512/apps"
        curl -fsSL -o "$ICONS_BASE/512x512/apps/prot-desktop.png" \
            "https://raw.githubusercontent.com/${REPO}/main/apps/desktop/src-tauri/icons/icon.png" || true
    fi

    # Desktop entry. The file name and StartupWMClass match the window's app id
    # ("prot-desktop") so taskbars and docks on Wayland and X11 show the right icon.
    cat > "$APPLICATIONS_DIR/prot-desktop.desktop" << EOF
[Desktop Entry]
Type=Application
Name=prot
GenericName=Development Control Plane
Comment=Your development servers, under control
Exec=$INSTALL_DIR/prot-desktop
Icon=prot-desktop
Terminal=false
Categories=Development;
StartupWMClass=prot-desktop
EOF

    # Remove the legacy entry and icon from earlier installs.
    rm -f "$APPLICATIONS_DIR/prot.desktop" "$ICONS_BASE/128x128/apps/prot.png"

    command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPLICATIONS_DIR" >/dev/null 2>&1 || true
    command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f "$ICONS_BASE" >/dev/null 2>&1 || true
    echo -e "${GREEN}✓ Application menu shortcut and icons registered${NC}"
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

# 6. Shell completion, in the directories each shell reads without any configuration. A prot that does not have the
# 'completion' command yet (an older release) is skipped without a word.
install_completion() {
    local shell="$1" dest="$2"
    mkdir -p "$(dirname "$dest")"
    if "$INSTALL_DIR/prot" completion "$shell" >"$dest.tmp" 2>/dev/null; then
        mv -f "$dest.tmp" "$dest"
        return 0
    fi
    rm -f "$dest.tmp"
    return 1
}

DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
COMPLETION_SHELLS=""
ZSH_FPATH_HINT=0
if install_completion bash "$DATA_HOME/bash-completion/completions/prot"; then
    COMPLETION_SHELLS="bash"
    if command -v fish >/dev/null 2>&1 &&
        install_completion fish "${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions/prot.fish"; then
        COMPLETION_SHELLS="$COMPLETION_SHELLS fish"
    fi
    if command -v zsh >/dev/null 2>&1 && install_completion zsh "$DATA_HOME/zsh/site-functions/_prot"; then
        COMPLETION_SHELLS="$COMPLETION_SHELLS zsh"
        ZSH_FPATH_HINT=1
    fi
    echo -e "${GREEN}✓ Shell completion installed for: ${COMPLETION_SHELLS}${NC}"
    if [ "$ZSH_FPATH_HINT" = 1 ]; then
        echo "  zsh only looks in directories on \$fpath. If Tab does not complete prot, add this to ~/.zshrc"
        echo -e "  before the 'compinit' line: ${BOLD}fpath=(\"$DATA_HOME/zsh/site-functions\" \$fpath)${NC}"
    fi
fi

# 7. Check PATH
case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        ;;
    *)
        echo -e "\n${YELLOW}Notice:${NC} $INSTALL_DIR is not in your current PATH."
        echo "Add the following line to your ~/.bashrc or ~/.zshrc:"
        echo -e "  ${BOLD}export PATH=\"\$HOME/.local/bin:\$PATH\"${NC}"
        ;;
esac

# 8. Verification check
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
