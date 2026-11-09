# Installing prot

> **Your development servers, under control.**  
> Local-first · Cross-platform ambition · Agent-native · Open API

---

## 1. Quick Install (Linux & macOS)

The fastest way to install the `prot` CLI, background daemon (`protd`), and register the background service is using the one-line installer:

```bash
curl -fsSL https://raw.githubusercontent.com/terastal-hq/prot/main/scripts/install.sh | bash
```

### What the installer does:
1. Detects your operating system (`Linux`, `macOS`) and CPU architecture (`amd64`, `arm64`).
2. Installs the standalone `prot` and `protd` binaries into `~/.local/bin/`.
3. Registers and enables `protd` as a background user service:
   - **Linux:** `systemd --user` unit (`~/.config/systemd/user/protd.service`).
   - **macOS:** `launchd` user agent (`~/Library/LaunchAgents/dev.prot.protd.plist`).
4. Installs the application menu shortcut (`prot.desktop`) if the desktop binary is present.
5. Runs `prot doctor` to verify system port permissions and capabilities.

---

## 2. Desktop Application

The first-party **prot Desktop** application is built with Tauri 2 + React.

- **Linux:** Download the `.AppImage` or `.deb` from [Releases](https://github.com/terastal-hq/prot/releases).
- **macOS:** Download the `.dmg` from [Releases](https://github.com/terastal-hq/prot/releases).

*Note: The desktop app includes bundled `prot` and `protd` sidecar binaries and automatically connects to or installs the background daemon on launch.*

---

## 3. Package Managers

### Debian / Ubuntu
```bash
# Download and install the .deb package
curl -LO https://github.com/terastal-hq/prot/releases/latest/download/prot_linux_amd64.deb
sudo dpkg -i prot_linux_amd64.deb
```

### Fedora / RHEL / Arch (RPM)
```bash
# Download and install the .rpm package
curl -LO https://github.com/terastal-hq/prot/releases/latest/download/prot_linux_amd64.rpm
sudo rpm -i prot_linux_amd64.rpm
```

---

## 4. Building From Source

### Prerequisites
- **Go:** 1.22+
- **Rust/Cargo:** 1.75+ (only needed for desktop app)
- **Node.js:** 20+ (only needed for desktop app)
- **OpenSSH:** Standard system OpenSSH client (`ssh`)

### Step 1: Clone Repository
```bash
git clone https://github.com/terastal-hq/prot.git
cd prot
```

### Step 2: Build CLI & Daemon
```bash
# Build Go binaries
go build -ldflags="-s -w" -o bin/prot ./cmd/prot
go build -ldflags="-s -w" -o bin/protd ./cmd/protd

# Copy to user bin path
cp bin/prot bin/protd ~/.local/bin/
```

### Step 3: (Optional) Build Desktop App
```bash
cd apps/desktop
npm install
npm run build
cd src-tauri
cargo build --release

cp target/release/prot-desktop ~/.local/bin/
```

---

## 5. Background Daemon Management

`prot` is a daemon-first architecture. The daemon (`protd`) runs independently in the background so closing your terminal or desktop window does not terminate your development servers or routes.

### Linux (`systemd --user`)
```bash
# Start and enable daemon at login
systemctl --user enable --now protd

# View daemon status
systemctl --user status protd

# View live daemon logs
journalctl --user -u protd -f

# Restart daemon
systemctl --user restart protd
```

### macOS (`launchd`)
```bash
# Start daemon agent
launchctl load -w ~/Library/LaunchAgents/dev.prot.protd.plist

# Stop daemon agent
launchctl unload ~/Library/LaunchAgents/dev.prot.protd.plist
```

---

## 6. Verifying Installation

Run the built-in diagnostic suite to confirm your system capabilities:

```bash
prot doctor
```

Example output:
```text
prot doctor — Diagnostic and Support Matrix Check

[1/6] Operating System:
      OS:           linux
      Architecture: amd64
      Go Version:   go1.27.3

[2/6] Process Inspection:
      ✓ Socket inspection: OK (11 listening TCP sockets found)

[3/6] Port Binding Capabilities:
      • Port 80 (standard HTTP): privileged / restricted
        (prot will use unprivileged gateway port 8080 by default)
      ✓ Port 8080: available for unprivileged gateway

[4/6] SSH Tunnel Capability:
      ✓ OpenSSH client: found at /usr/bin/ssh

[5/6] Development Toolchain:
      ✓ Node.js: found (v24.22.2)

[6/6] Support Matrix Evaluation:
      Tier 1 (Full Support): All discovery, proxy, and tunnel features supported.
```

---

## 8. Updating prot

### In-Place CLI Self-Updater (Recommended)
You can update `prot` and `protd` to the latest release directly from your terminal:

```bash
# Check if a new release is available
prot update --check

# Download, swap binaries atomically, and restart the background daemon
prot update
```

The updater:
1. Queries the latest GitHub release assets for your operating system and architecture.
2. Performs an atomic in-place binary swap using OS file renames (avoiding `text file busy` errors with running binaries).
3. Automatically restarts the background daemon via `systemctl --user restart protd` (Linux) or `launchd` (macOS), maintaining your registered services and aliases without downtime.

### Script Re-run
You can also re-run the one-line installer at any time:

```bash
curl -fsSL https://raw.githubusercontent.com/terastal-hq/prot/main/scripts/install.sh | bash
```

---

## 9. AI Agent & MCP Client Setup

Prot exposes the **Model Context Protocol (MCP)** so AI coding agents (Claude Desktop, Cursor, pi, etc.) can discover, start, stop, and inspect your local services.

Add this to your MCP settings file (e.g. `claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "prot": {
      "command": "prot",
      "args": ["mcp"]
    }
  }
}
```

`prot mcp` forwards every request to the running daemon, so `protd` must be running
(`systemctl --user status protd`). Services an agent starts are owned by the daemon, not by the agent session.

Or connect via loopback HTTP at:
`http://127.0.0.1:4400/mcp` (Bearer token located at `~/.prot/auth.token`).
