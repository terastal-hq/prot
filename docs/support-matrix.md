# prot — Platform Support Matrix & Technical Baseline

**Target Version:** v1.0  
**Last Updated:** Milestone M0 Baseline

---

## 1. Operating System Support Matrix

| Platform | Architecture | Tier | Status | Process Discovery | Reverse Proxy | SSH Tunnels | Daemon Auto-Start |
| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| **Linux** | `amd64`, `arm64` | **Tier 1** | Primary target (Validated) | Native (`/proc`, `gopsutil`) | Go `net/http` (8080 unprivileged, 80 with auth) | OpenSSH child process | `systemd` user service |
| **macOS** | `arm64` (Apple Silicon), `amd64` | **Tier 1** | Primary target | Native (`sysctl`, `gopsutil`) | Go `net/http` (8080 unprivileged, 80 with pf/auth) | OpenSSH child process | `launchd` user agent |
| **Windows** | `x86_64` | **Not supported yet** | The supervisor and tunnels need a Unix-like OS, and no Windows binaries are released | | | | |

---

## 2. Core Capabilities & Architectural Invariants

### A. Automatic Process Discovery (`internal/discovery`)
- **Observation Only:** Listening TCP sockets are discovered via OS networking APIs and matched to process inodes.
- **Process Identity:** Fingerprint combines `PID + CreateTime + BinaryPath + Port + IP` to prevent process identity confusion from OS PID recycling.
- **Protocol Inspection:** Conservative, bounded HTTP probe (timeout ≤ 400ms, max 4KB body inspection, non-destructive `GET /` with `User-Agent: prot-discovery/1.0`).
- **Framework Detection:** Heuristics detect Next.js, Vite, React, Svelte, Nuxt, and backend servers (e.g. Uvicorn, Werkzeug, Node/Express).

### B. Local Reverse Proxy & WebSockets (`internal/proxy`)
- **Domain Namespace:** Default `.localhost` routing (e.g. `frontend.localhost`, `api.localhost`).
- **Dynamic Updates:** Upstream port changes are applied immediately in memory without restarting proxy listeners or interrupting active connections on unaffected routes.
- **WebSocket & HMR Support:** Streaming responses flush with zero buffer delay; WebSockets pass full-duplex frames.
- **Security Isolation:** The management REST API (`/v1/*`) and MCP (`/mcp`) are served only on a separate loopback listener (default `127.0.0.1:4400`) behind token authentication and DNS-rebinding checks. The dev gateway never serves them, so every path on `<alias>.localhost` belongs to your app, including ones like `/v1/users`.
- **LAN Sharing Policy:** Routes are private (loopback-only) by default. LAN exposure must be explicitly enabled per route.

### C. SSH Tunnel Supervision (`internal/tunnels`)
- **Credential Safety:** Prot never touches or stores raw SSH private keys, and never puts a password in its database or files. It invokes OpenSSH (`/usr/bin/ssh`) to leverage `~/.ssh/config` and the user's running `ssh-agent`. A tunnel stores only the path of an identity file. An optional password or key passphrase lives in the OS keyring (Secret Service on Linux, Keychain on macOS) and is handed to `ssh` through an askpass helper that answers only password prompts, never host-key questions.
- **Non-interactive:** `ssh` runs with `BatchMode=yes` and `ConnectTimeout=10` (before user arguments, so they cannot override them), or through the askpass helper when a password is stored. Keys must work unattended and the host key must already be trusted.
- **Readiness Verification:** Probes local forwarded port before transitioning state to `connected`.
- **Auto-Recovery:** Exponential backoff reconnection loop (0.5s to 15s) on failed attempts and unexpected disconnects.
- **Diagnostics:** The tail of `ssh`'s stderr is kept as the tunnel's last error.
- **Persistence:** Tunnels are stored in SQLite, restored at startup, and `auto_connect` ones reconnect. Stopping `protd` stops every tunnel.
- **Addressing:** Each tunnel's alias is a `.localhost` route to its local port. See [SSH tunnels](tunnels.md).

### D. Security & Management Isolation (`internal/security`)
- **Token Auth:** Cryptographically secure 256-bit token persisted to `~/.prot/auth.token` with strict `0600` permissions.
- **DNS Rebinding Protection:** Management endpoints validate loopback remote addresses and reject non-loopback Host headers.

---

## 3. Environment Prerequisites

- **Go:** 1.22+ (verified with Go 1.27)
- **Node.js:** 20+ (verified with Node 24.22)
- **OpenSSH:** Standard system OpenSSH client (`ssh`)
- **Rust/Cargo:** 1.75+ (for Tauri 2 desktop shell builds)
