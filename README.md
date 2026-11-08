# prot

> **Your development servers, under control.**  
> Local-first · Cross-platform ambition · Agent-native · Open API

Prot is a local-first developer utility that discovers, organizes, routes, and manages development services, whether they run locally or through remote SSH connections.

It provides stable service identities and addresses (`http://<alias>.localhost`) independent of underlying process IDs, TCP ports, and infrastructure locations.

---

## Architecture

- **`protd`**: Long-running background daemon managing process discovery, reverse proxy routing, SSH tunnels, and SQLite registry.
- **`prot`**: Fast, lightweight CLI for terminal control, diagnostics, and MCP stdio adapter.
- **Prot Desktop**: Tauri 2 + React + TypeScript first-party dashboard.
- **REST & MCP API**: Loopback management API with token authentication and DNS-rebinding protection.

---

## Getting Started

### Prerequisites

- Go 1.22+
- OpenSSH client

### Build CLI & Daemon

```bash
# Build prot CLI and protd daemon
go build -o bin/prot ./cmd/prot
go build -o bin/protd ./cmd/protd
```

### Inspect Your System

```bash
# Run environment diagnostics and support matrix check
./bin/prot doctor

# Discover running local TCP and HTTP servers
./bin/prot discover
```

### Start the Daemon & Map Routes

```bash
# Start background daemon
./bin/protd &

# Check daemon status
./bin/prot status

# Register an alias to an upstream service
./bin/prot alias frontend http://127.0.0.1:3000

# Access your service through the stable URL
curl -H "Host: frontend.localhost" http://127.0.0.1:8080/
```

### Running Tests

```bash
go test -v ./...
```
