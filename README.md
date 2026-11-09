<h1 align="center">
  <img src="docs/assets/prot-logo.png" alt="prot" width="420" />
</h1>

> **Your development servers, under control.**  
> Local-first · Cross-platform ambition · Agent-native · Open API

Prot is a local-first developer utility and control plane that discovers, organizes, routes, and manages development services, whether they run locally or through remote SSH connections.

It provides stable service identities and addresses (`http://<alias>.localhost`) independent of underlying process IDs, TCP ports, and infrastructure locations.

<p align="center">
  <img src="docs/assets/prot-desktop-ui.png" alt="prot desktop app" width="340" />
</p>

<img src="docs/assets/headings/quick-install.svg" alt="Quick Install" />

Install the `prot` CLI, background daemon (`protd`), and systemd/launchd service with a single command:

```bash
curl -fsSL https://raw.githubusercontent.com/terastal-hq/prot/main/scripts/install.sh | bash
```

For package managers (`.deb`, `.rpm`), desktop installers (`.AppImage`, `.dmg`), and build instructions, see the **[Installation Guide](docs/installation.md)**.

<img src="docs/assets/headings/architecture.svg" alt="Architecture" />

<p align="center">
  <img src="docs/assets/architecture.svg" alt="Prot Desktop, the prot CLI and AI agents talk over REST, IPC and MCP to protd, which owns the service registry, reverse proxy, process discovery, supervisor, SSH tunnels and security layer." width="860" />
</p>

- **`protd`**: Long-running background daemon managing process discovery, reverse proxy routing, OpenSSH tunnels, and SQLite registry.
- **`prot`**: Fast, lightweight CLI for terminal control, diagnostics, and MCP stdio adapter.
- **Prot Desktop**: Tauri 2 + React + TypeScript first-party dashboard.
- **REST & MCP API**: Loopback management API with 256-bit token authentication and DNS-rebinding protection.

<img src="docs/assets/headings/quick-tour.svg" alt="Quick Tour" />

### 1. Inspect System & Diagnostics
```bash
prot doctor
```

### 2. Discover Listening Development Services
Find running Vite, Next.js, Python, or backend servers automatically:
```bash
prot discover
```

### 3. Register and Supervise a Managed Service
```bash
# Register a dev server with a stable alias
prot add "Frontend Web" --cmd "npm run dev" --alias "web" --port 3000

# Start the service
prot start web

# Reach your service via stable .localhost URL
open http://web.localhost:8080
```

### 4. View Real-Time Logs
```bash
prot logs web -f
```

### 5. Check Daemon Status & Routes
```bash
prot status
```

<img src="docs/assets/headings/ai-agent-integration-mcp.svg" alt="AI Agent Integration (MCP)" />

Prot natively implements the **Model Context Protocol (MCP)**. AI agents (such as Claude Desktop, Cursor, or pi) can discover your local services, verify health, and ensure required endpoints are running.

### Automatic Setup Prompt for AI Coding Agents

Paste this prompt directly into your AI coding assistant (Claude Code, Cursor, pi, Cline, Windsurf, etc.) to configure prot automatically:

> "Add prot as an MCP server in my agent configuration. Configure it with command `prot` and arguments `["mcp"]`. If writing to a config file like claude_desktop_config.json or mcp.json, register it under `mcpServers.prot`."

### Manual Configuration

Add to your MCP configuration file (e.g. `claude_desktop_config.json`):

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

`prot mcp` is a thin bridge: it relays each message to the running `protd` over the authenticated loopback API,
so agents see the same services, logs and process state as the CLI and the desktop app. Processes an agent starts
belong to the daemon and keep running when the agent disconnects. If `protd` is not running, tool calls fail with
a clear error until it is (`systemctl --user start protd`).

### Available MCP Tools

Tools that target a service take `id_or_alias`.

- `list_services`: Discover available local services.
- `get_service`: Read service state and launch configuration.
- `get_service_url`: Obtain the canonical `.localhost` URL.
- `get_service_logs`: Retrieve bounded recent stdout/stderr logs.
- `ensure_running`: Idempotently start a required service if not running.
- `start_service`, `stop_service`, `restart_service`: Process controls.
- `set_alias`: Change a service's `.localhost` alias (takes `id_or_alias` and `alias`).

<img src="docs/assets/headings/documentation.svg" alt="Documentation" />

- **[Installation Guide](docs/installation.md)** — One-line installer, binary packages, background service management.
- **[Window Managers](docs/window-managers.md)** — Floating rules for niri, sway, i3 and Hyprland.
- **[Platform Support Matrix](docs/support-matrix.md)** — Operating system tiers and hardware capabilities.
- **[OpenAPI Specification](schemas/openapi.yaml)** — Complete REST API contract.

<img src="docs/assets/headings/testing.svg" alt="Testing" />

Run the Go unit test suite:
```bash
go test -v ./...
```

Run the end-to-end integration acceptance test:
```bash
./scripts/test_e2e.sh
```

<img src="docs/assets/headings/license.svg" alt="License" />

MIT © [Terastal HQ](https://github.com/terastal-hq)
