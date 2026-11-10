# Installing prot

> **Your development servers, under control.**  
> Local-first · Cross-platform ambition · Agent-native · Open API

---

## 1. Quick Install (Linux & macOS)

The fastest way to install the `prot` CLI, background daemon (`protd`), and register the background service is using the one-line installer:

```bash
curl -fsSL https://prot.adityagupta.dev/install | bash
```

On Arch Linux, prefer the [AUR packages](#arch-linux-aur) below, which your package manager can update.

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

- **Arch Linux:** `yay -S prot-desktop` (see [Package Managers](#arch-linux-aur)).
- **Linux (other):** Download the `.AppImage` or `.deb` from [Releases](https://github.com/terastal-hq/prot/releases).
- **macOS:** Download the `.dmg` from [Releases](https://github.com/terastal-hq/prot/releases).

*Note: The `.AppImage`, `.deb` and `.dmg` bundle `prot` and `protd` and connect to (or start) the background daemon on launch. The AUR `prot-desktop` package instead depends on the `prot` and `protd` packages.*

---

## 3. Package Managers

### Arch Linux (AUR)

prot is three packages from two AUR bases. `protd` and `prot` are built together from Go alone, so `yay -S prot` needs only
`go` and compiles in seconds. The desktop app (Rust, Node and WebKit) is its own base, `prot-desktop`, and is only built
when you ask for it:

| Package | Contents | Install it when |
|---|---|---|
| [`protd`](https://aur.archlinux.org/packages/protd) | the daemon and its `systemd --user` unit | you only want the background service |
| [`prot`](https://aur.archlinux.org/packages/prot) | the CLI and MCP server (depends on `protd`) | you want the command line |
| [`prot-desktop`](https://aur.archlinux.org/packages/prot-desktop) | the desktop app (depends on `prot`) | you want the GUI |

```bash
# CLI + daemon
yay -S prot

# Desktop app (also pulls in prot and protd; compiles the app, which takes a few minutes)
yay -S prot-desktop

# Start the daemon now and at every login
systemctl --user enable --now protd
```

Any AUR helper works (`paru -S prot`, or `git clone` the package and run `makepkg -si`).

Packages never enable services themselves, so run the `systemctl --user enable --now protd` line once after installing.
Updates come through your package manager (`yay -Syu`); after an upgrade, restart the daemon with
`systemctl --user restart protd`. Running `prot update` on a package-managed install does nothing except tell you this.

### Debian / Ubuntu
Download the `.deb` for your architecture from [Releases](https://github.com/terastal-hq/prot/releases/latest)
(the file names include the version, for example `prot_0.1.0_linux_amd64.deb`), then:

```bash
sudo dpkg -i prot_<version>_linux_amd64.deb
```

### Fedora / RHEL (RPM)
Download the `.rpm` for your architecture from [Releases](https://github.com/terastal-hq/prot/releases/latest), then:

```bash
sudo rpm -i prot_<version>_linux_amd64.rpm
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

## 7. Shell Completion

`prot` completes in **bash**, **zsh** and **fish**: commands, subcommands and options, and, from the running daemon, the
aliases of your services and tunnels, the ids of discovered processes, and the machines in your `~/.ssh/config`
(for `--host`).

```text
$ prot start <Tab>
api     web     worker
$ prot tunnel add db --host <Tab>
bastion   prod-db   prod-web
```

The `.deb`, `.rpm` and AUR packages, and the curl installer, set it up for you: open a new shell. The installer puts the
files in the user directories (`~/.local/share/bash-completion/completions`, `~/.config/fish/completions` and
`~/.local/share/zsh/site-functions`), so it needs no change to your shell's configuration, with two exceptions:

- **bash** loads files from there only when the `bash-completion` package is installed, which most Linux distributions
  have by default and macOS does not.
- **zsh** only reads directories on `$fpath`. If Tab does nothing, add this to `~/.zshrc` before `compinit`:

  ```zsh
  fpath=(~/.local/share/zsh/site-functions $fpath)
  ```

To set it up by hand, or to try it in one session, `prot completion <shell>` prints the script:

```bash
# bash: add to ~/.bashrc
source <(prot completion bash)

# zsh: add to ~/.zshrc, after compinit
source <(prot completion zsh)

# fish: keep it
prot completion fish > ~/.config/fish/completions/prot.fish
```

The scripts contain no list of commands; each Tab runs `prot __complete`, so the suggestions always match the `prot`
you have installed and are the same in all three shells. A lookup that needs the daemon waits at most 1.5 seconds and
says nothing when the daemon is not running, so Tab never prints an error onto your prompt.

---

## 8. Updating prot

### Installed from a package manager (AUR, `.deb`, `.rpm`)
Update through the package manager (`yay -Syu` on Arch). `prot update` detects that a package owns the binary and exits
with a pointer to your package manager instead of replacing files it does not own. `prot update --check` still reports
whether a newer release exists.

### In-Place CLI Self-Updater (curl installer and manual installs)
You can update `prot` and `protd` to the latest release directly from your terminal:

```bash
# Check if a new release is available
prot update --check

# Download, swap binaries atomically, and restart the background daemon
prot update
```

The updater:
1. Queries the latest GitHub release assets for your operating system and architecture.
2. Verifies the archive against the release's `checksums.txt` and refuses to install anything that does not match.
3. Performs an atomic in-place binary swap using OS file renames (avoiding `text file busy` errors with running binaries).
4. Automatically restarts the background daemon via `systemctl --user restart protd` (Linux) or `launchd` (macOS), maintaining your registered services and aliases without downtime.

### Script Re-run
You can also re-run the one-line installer at any time:

```bash
curl -fsSL https://prot.adityagupta.dev/install | bash
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
