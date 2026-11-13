# SSH tunnels

A tunnel forwards a port on a remote machine to your laptop through `ssh`, and gives it a stable
`http://<alias>.localhost` address like any other prot service. Tunnels are saved, reconnect on their own and
survive restarts of the daemon.

```bash
# Reach Postgres on a bastion as http://staging-db.localhost:8080 (and 127.0.0.1:15432)
prot tunnel add staging-db --host deploy@bastion.example.com --remote-port 5432 --local-port 15432 --auto-connect
prot tunnel add staging-db --host prod-db --remote-port 5432   # or a machine name from ~/.ssh/config
```

## Before you add one

`protd` runs `ssh` in the background with no terminal, so `ssh` can never ask you anything. Two things must
already be settled:

1. **How it authenticates.** Pick one:
   - a key held by `ssh-agent` (best), or an unencrypted key file (`--identity`);
   - a **password or key passphrase stored in your OS keyring** (see [Passwords](#passwords)).
2. **The host key.** Connect to the host once yourself (`ssh deploy@bastion.example.com`) and accept its key, so it
   is in `~/.ssh/known_hosts`. Otherwise the tunnel reports `Host key verification failed.` prot never accepts an
   unknown host key on your behalf.

Whatever `~/.ssh/config` says for the host (user, port, `ProxyJump`, identity) applies too.

## Machines from `~/.ssh/config`

Instead of spelling out `user@host`, use a name you already defined in your ssh configuration:

```
# ~/.ssh/config
Host prod-db
  HostName db.internal.example.com
  User deploy
  Port 2222
  IdentityFile ~/.ssh/prod_key
  ProxyJump bastion
```

```bash
prot ssh ls                                              # the machines prot found, and what ssh resolves for each
prot tunnel add db --host prod-db --remote-port 5432     # user, port, key and jump host all come from the config
```

In the desktop app the SSH host box in the New Tunnel sheet is both a text box and a dropdown of these names (type
to filter, arrow keys and Enter to choose). Choosing one fills in the SSH user, SSH port and identity file ssh will use and shows
`deploy@db.internal.example.com:2222 via bastion` under the box. Those values are only passed on if you change them;
otherwise the tunnel stores just the name and ssh keeps reading your configuration, so editing `~/.ssh/config`
later changes the tunnel too. Typing any other host still works, and so does `user@host` in the host box (a user
typed into the SSH user box wins over it).

How prot reads it:

- **Listing** reads `~/.ssh/config` and follows `Include` (globs and `~` work). Only concrete `Host` names are
  offered; wildcard patterns such as `Host *.internal` are not machines. Nothing is executed.
- **Resolving** one name is done by `ssh -G`, so `Match`, `Include` and wildcard blocks mean exactly what they mean
  to ssh. Only names the configuration lists are resolved. Keys that ssh tries by default (`~/.ssh/id_ed25519` and
  friends) are not shown as the machine's key.
- A name you pick must be listed by the file `protd` reads. Start `protd -ssh-config <file>` to use another file;
  tunnels then use it too.

## Passwords

```bash
# typed without echo, or piped in; never a command-line flag, so it cannot show up in ps or shell history
prot tunnel add legacy --host admin@10.0.0.7 --remote-port 8080 --password --auto-connect
printf '%s\n' "$PASSWORD" | prot tunnel add legacy --host admin@10.0.0.7 --remote-port 8080 --password-stdin

prot tunnel password legacy            # change it (asks twice); --stdin to pipe it in
prot tunnel password legacy --clear    # back to keys and ssh-agent only
```

The desktop app has the same in the New Tunnel sheet and behind the key button on each tunnel.

The secret is a login password or the passphrase of an encrypted key; `ssh` asks for both the same way.

**Where it lives.** In the operating system's keyring: the Secret Service (GNOME Keyring, KWallet, KeePassXC...) on
Linux, the Keychain on macOS. prot's own files and database only record that a password exists (`has_password`).
It never appears in an API response, a log, the MCP tools, or on a command line. Deleting the tunnel removes it from
the keyring.

**No keyring, no password.** If no keyring service is running, adding a password fails with a clear error and saves
nothing, rather than storing it somewhere weaker. Keys and `ssh-agent` keep working. If the keyring is locked
(for example right after boot) the tunnel reports why and keeps retrying, and connects once you unlock it.

**How `ssh` gets it.** For a tunnel with a password `protd` runs `ssh` with `SSH_ASKPASS_REQUIRE=force`, so `ssh`
asks a helper instead of a terminal. The helper is `protd` itself in a one-shot mode (`PROT_ASKPASS=1`): it answers
password and passphrase prompts, and **refuses everything else**, including "Are you sure you want to continue
connecting (yes/no)?". A wrong password costs one attempt, not three (`NumberOfPasswordPrompts=1`), so it will not
trip lockout tools such as fail2ban by itself.

**What can read it.** The secret reaches `ssh` through that process's environment, readable only by your user (and
root), not on a command line. Anything running as you can also ask the keyring for it while it is unlocked, and a
`ProxyCommand` in your `ssh` config for that host would inherit the environment. A password is still weaker than a
key: prefer `ssh-copy-id` and a key when you can.

## Commands

| Command | What it does |
| :--- | :--- |
| `prot tunnel add <alias> --host [user@]host --remote-port N` | Save a tunnel |
| `prot tunnel ls [--json]` | List tunnels, their state, local port and address |
| `prot tunnel edit <id_or_alias> [options]` | Change a saved tunnel in place (see below) |
| `prot tunnel start <id_or_alias> [--no-wait]` | Connect, and wait until it is reachable |
| `prot tunnel stop <id_or_alias>` | Disconnect (returns once `ssh` has exited) |
| `prot tunnel rm <id_or_alias>` | Disconnect and delete |
| `prot tunnel password <id_or_alias> [--stdin \| --clear]` | Set, replace or remove the stored password |

`add` options: `--user`, `--remote-host` (default `127.0.0.1`), `--local-port` (default: a free port each time),
`--identity`, `--ssh-port`, repeatable `--ssh-arg` for any other `ssh` argument, `--password` /
`--password-stdin`, and `--auto-connect`.

### Editing a tunnel

```bash
prot tunnel edit db --remote-port 6000                 # change one thing; the rest stays as it is
prot tunnel edit db --alias warehouse --host prod-db   # rename it and point it at a machine from ~/.ssh/config
prot tunnel edit db --user "" --identity ""            # an empty value clears an optional field
```

`edit` takes the same options as `add` (`--alias`, `--host`, `--user`, `--remote-host`, `--remote-port`,
`--local-port`, `--identity`, `--ssh-port`, `--ssh-arg`, `--auto-connect[=false]`), and only what you give changes.
`--ssh-port` changes just the port; `--ssh-arg` replaces the other extra `ssh` arguments and keeps the port unless you
also change it. The id and the stored password stay (the password has its own command).

A tunnel that was connecting, connected or retrying is reconnected with the new settings, so the change takes effect
straight away; a stopped one stays stopped and uses them the next time it connects. An edit that changes nothing
does not touch a live connection. A refused edit (an alias that is taken, a missing remote port, ...) changes
nothing and leaves the connection up. Renaming moves the `.localhost` address: the old name stops resolving.

The desktop app's Tunnels tab does the same with a form, and the settings button on a tunnel's card edits it. Agents get `list_tunnels`, `start_tunnel`, `stop_tunnel`, `add_tunnel` and `list_ssh_hosts` (the machine names in `~/.ssh/config`, read-only) over MCP; they see `has_password` but can never read
or set a password.

`add_tunnel` (alias, host, remote_port, and optionally user, remote_host and local_port) only works for a machine that is
already set up: ssh must reach it with no prompt at all, using a key from `ssh-agent` or `~/.ssh` that the server accepts
(`ssh-copy-id`) and a host key you have already trusted (connect once from a terminal). The tool connects the tunnel and
waits for it. If it does not come up, the tunnel is deleted again, nothing is saved, and the agent is told to ask you to
set the host up. There is no way to pass a password, an identity file or extra ssh options, and ssh always runs with
`BatchMode`. Tunnels it creates are saved with `auto_connect`. The event log records the creation with `"source":"mcp"`.

This is a real grant of power: an agent can open a tunnel to any machine your keys already let you into. Editing or
deleting a tunnel is still left to you.

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
- **Always non-interactive.** prot adds `-o BatchMode=yes -o ConnectTimeout=10` ahead of your own arguments
  (tunnels with a password use the askpass helper instead of `BatchMode`, see above). `ssh` keeps the first value
  it sees for an option, so `--ssh-arg` cannot turn prompting back on.

## If `protd` is killed

A clean stop (`systemctl --user stop protd`, Ctrl+C) stops the tunnels. If `protd` is killed outright (SIGKILL or
a crash) its `ssh -N -L` processes can be left running. The next daemon's tunnel on the same local port will then
fail with `address already in use`; stop the leftover with `pkill -f 'ssh -N -L'`.

## REST API

`GET/POST /v1/tunnels`, `DELETE /v1/tunnels/{id}`, `POST /v1/tunnels/{id}/start`, `POST /v1/tunnels/{id}/stop`,
`PATCH /v1/tunnels/{id}` (edit; only the fields in the body change), `PUT/DELETE /v1/tunnels/{id}/password`, and for the machines in `~/.ssh/config` `GET /v1/ssh/hosts` and
`GET /v1/ssh/hosts/{alias}`. `{id}` is the tunnel's id or its alias. `POST` accepts an optional
write-only `password`. Errors use the usual statuses: 400 invalid alias, field or password, 404 unknown tunnel,
409 alias already used, 501 when `ssh` is not installed or a password was given without a keyring. See
[openapi.yaml](../schemas/openapi.yaml).
