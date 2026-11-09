# SSH tunnels

A tunnel forwards a port on a remote machine to your laptop through `ssh`, and gives it a stable
`http://<alias>.localhost` address like any other prot service. Tunnels are saved, reconnect on their own and
survive restarts of the daemon.

```bash
# Reach Postgres on a bastion as http://staging-db.localhost:8080 (and 127.0.0.1:15432)
prot tunnel add staging-db --host deploy@bastion.example.com --remote-port 5432 --local-port 15432 --auto-connect
```

## Before you add one

`protd` runs `ssh` in the background with no terminal, so `ssh` can never ask you anything. Two things must
already work without a prompt:

1. **Authentication.** Use a key held by `ssh-agent`, or an unencrypted key file (`--identity`). A password or a
   key passphrase prompt is not possible and will fail.
2. **The host key.** Connect to the host once yourself (`ssh deploy@bastion.example.com`) and accept its key, so it
   is in `~/.ssh/known_hosts`. Otherwise the tunnel reports `Host key verification failed.`

Whatever `~/.ssh/config` says for the host (user, port, `ProxyJump`, identity) applies too.

## Commands

| Command | What it does |
| :--- | :--- |
| `prot tunnel add <alias> --host [user@]host --remote-port N` | Save a tunnel |
| `prot tunnel ls [--json]` | List tunnels, their state, local port and address |
| `prot tunnel start <id_or_alias> [--no-wait]` | Connect, and wait until it is reachable |
| `prot tunnel stop <id_or_alias>` | Disconnect (returns once `ssh` has exited) |
| `prot tunnel rm <id_or_alias>` | Disconnect and delete |

`add` options: `--user`, `--remote-host` (default `127.0.0.1`), `--local-port` (default: a free port each time),
`--identity`, `--ssh-port`, repeatable `--ssh-arg` for any other `ssh` argument, and `--auto-connect`.

The desktop app's Tunnels tab does the same with a form. Agents get `list_tunnels`, `start_tunnel` and
`stop_tunnel` over MCP. There is deliberately no MCP tool to create one: that would let an agent open an SSH
connection to an arbitrary host.

## How it behaves

- **Address.** The alias becomes `<alias>.localhost`, routed to the tunnel's local port. If you did not pick a local
  port the route follows whichever port each connection gets. When the tunnel is down the address shows prot's
  "upstream is not responding" page rather than a 404. Aliases are shared with services, so they must be unique
  across both.
- **Connected means reachable.** A tunnel is `connected` only once its local port accepts connections.
- **Retries.** If the connection drops or fails, prot retries with a growing delay (0.5s up to 15s). A tunnel that
  has been up for 30 seconds starts over at 0.5s. The state is `retrying` while it does.
- **Why it failed.** The last lines `ssh` printed are kept as the tunnel's last error, so you see
  `Permission denied (publickey)` or `Host key verification failed` instead of `exit status 255`. They show in
  `prot tunnel ls`, the desktop app and `list_tunnels`.
- **Restarts.** Tunnels are stored in `~/.prot/prot.db`. When `protd` starts it restores them and reconnects the
  ones marked `--auto-connect`. When `protd` stops it stops every tunnel, so no `ssh` process outlives it.
- **Always non-interactive.** prot adds `-o BatchMode=yes -o ConnectTimeout=10` ahead of your own arguments.
  `ssh` keeps the first value it sees for an option, so `--ssh-arg` cannot turn prompting back on.

## If `protd` is killed

A clean stop (`systemctl --user stop protd`, Ctrl+C) stops the tunnels. If `protd` is killed outright (SIGKILL or
a crash) its `ssh -N -L` processes can be left running. The next daemon's tunnel on the same local port will then
fail with `address already in use`; stop the leftover with `pkill -f 'ssh -N -L'`.

## REST API

`GET/POST /v1/tunnels`, `DELETE /v1/tunnels/{id}`, `POST /v1/tunnels/{id}/start`, `POST /v1/tunnels/{id}/stop`.
`{id}` is the tunnel's id or its alias. Errors use the usual statuses: 400 invalid alias or field, 404 unknown
tunnel, 409 alias already used, 501 when `ssh` is not installed. See [openapi.yaml](../schemas/openapi.yaml).
