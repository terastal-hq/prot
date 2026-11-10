# Roadmap

Two features are planned next. **Neither is implemented.** Today the gateway serves plain HTTP on
`127.0.0.1:8080`, and the only tunnels Prot makes are outgoing SSH tunnels (see [SSH tunnels](tunnels.md)).

Everything below is a proposal: names, flags and defaults can change, the order is a priority and not a schedule, and
no dates are promised. Where a sketch shows a command or output, it is not something Prot prints today.

| Feature | What it gives you | Status |
| --- | --- | --- |
| [Local HTTPS](#1-local-https) | `https://web.localhost` with a certificate your machine trusts | Planned |
| [Public exposure](#2-public-exposure) | A temporary public URL for one service: previews and webhooks | Planned |

The two are independent. Public exposure does not need local HTTPS, because the tunnel provider terminates TLS for
public visitors.

## 1. Local HTTPS

Serve every alias over HTTPS from the same gateway, using a certificate authority (CA) that Prot creates on your
machine and that you explicitly choose to trust.

### Why

Be clear about what this does and does not buy. A browser already treats `http://web.localhost` as a secure context
(checked in Chromium 158: `isSecureContext`, `crypto.subtle` and service workers are all available), so HTTPS is not
needed for those APIs. What it adds:

- Apps and OAuth providers that insist on an `https://` redirect or origin.
- Cookie behavior that matches production (`Secure`, and `SameSite=None; Secure`).
- Mixed-content problems showing up early, for pages that call HTTPS APIs.
- HTTP/2. HTTP/1.1 browsers open about six connections per host, which slows dev servers that serve many small
  modules ([Portless documents the same benefit](https://deepwiki.com/vercel-labs/portless/3.2-https-and-tls)).

### Proposed design

| Part | Plan |
| --- | --- |
| Local CA | ECDSA P-256, created by `prot tls enable`, stored under `~/.prot/tls/` (directory `0700`, key `0600`) |
| Name constraint | The CA certificate permits only `localhost` and its subdomains (`PermittedDNSDomains`, marked critical), so a leaked key cannot sign for other sites in clients that enforce the extension. Support is good in current browsers and operating systems but not guaranteed everywhere, so this is defense in depth, not a promise |
| Leaf certificates | One per hostname, created on demand from the TLS SNI name by `tls.Config.GetCertificate`, with that exact name in the Subject Alternative Name, cached in memory. Portless reports that a `*.localhost` wildcard is not reliable in browsers, and per-host certificates avoid the question |
| Which names | Only registered aliases get a certificate. An unknown SNI name gets none |
| Listener | A second listener beside the HTTP one, reusing the existing registry and reverse proxy. HTTP/2 comes with `net/http` over TLS |
| Upstream | Plain HTTP over loopback, as today. `X-Forwarded-Proto` must report `https` to the app (to verify against the current proxy) |
| Bring your own | `--cert` and `--key`, or an existing mkcert CA, for people who already manage one |

Proposed commands:

```
prot tls enable      create the CA and start the HTTPS listener
prot tls trust       add the CA to the trust stores (shows what it will change first)
prot tls status      fingerprint, expiry, listener address, which stores trust it
prot tls untrust     remove it from every store it was added to
prot tls disable     stop the listener
```

### Trust is the hard part

Installing a root CA is a significant action, and "trusted" means different things per program.

| Where | Mechanism | Notes |
| --- | --- | --- |
| macOS | `security add-trusted-cert` | Authorization prompt |
| Debian, Ubuntu, openSUSE | Copy to `/usr/local/share/ca-certificates`, run `update-ca-certificates` | Needs `sudo` |
| Fedora, RHEL | Copy to `/etc/pki/ca-trust/source/anchors`, run `update-ca-trust` | Needs `sudo` |
| Arch | `trust anchor` | Needs `sudo` |
| Firefox, Chrome and Chromium on Linux | Their own NSS database | Needs `certutil` (`libnss3-tools`, `nss-tools` or `nss`) |
| Node.js | Does not read the system store | Set `NODE_EXTRA_CA_CERTS` to the CA file |

The mechanisms are the ones [mkcert](https://github.com/FiloSottile/mkcert) and
[Portless](https://deepwiki.com/vercel-labs/portless/3.2-https-and-tls) use. Rules for Prot:

- **Explicit consent.** `prot tls trust` is run by you, prints exactly what it will change, and supports a dry run.
  The daemon never modifies a trust store on its own. The desktop app can offer the same flow behind a confirmation that
  does not time out.
- **Exact removal.** Prot records every store it added the CA to, so `prot tls untrust` undoes precisely that and then
  deletes the key.
- **Key protection.** The CA key never leaves the machine and is not readable by other users. Whether it should live in
  the OS keyring, as tunnel passwords do, instead of a file is open (see below).
- **Agents.** No MCP tool touches TLS or trust. `get_service_url` would simply return the `https://` URL once it is
  enabled.

### Done when

- Chrome, Firefox (Linux) and Safari (macOS) open `https://<alias>.localhost` with no warning after `prot tls trust`.
- `curl` works against it with the system store, and a documented one-liner covers Node.
- A leaf for a name outside `localhost` fails verification against the CA (a test, so the constraint is proven).
- WebSockets and hot module replacement keep working through the TLS listener (tested with a real dev server).
- `prot tls untrust` leaves nothing trusted and no key on disk.

### Open questions

- **Port.** Binding `443` needs privileges on Linux. The likely default is a high port such as `8443`, which puts a port
  in the URL, with `443` as an opt-in through a capability or sysctl.
- **CA lifetime.** Long enough not to nag, short enough to limit a leak. Portless uses 10 years; Prot's value is open.
- **Key storage.** File with strict permissions, or the OS keyring.
- **HTTP to HTTPS redirect**, and whether to send HSTS (probably not for `.localhost`, as it sticks in the browser).

## 2. Public exposure

Let someone on the internet reach one service running on your machine, for a limited time.

```
prot expose web
```

This is the opposite direction to the SSH tunnels Prot already has. Those reach out to a service on another machine;
this lets the outside reach in to yours. No router port forwarding is needed: the machine opens an outbound connection to
a relay, and public requests come back over it.

Proposed shape, not implemented (the URL is a placeholder):

```
$ prot expose web --for 1h
web is running on :5173
exposed: https://<random>.trycloudflare.com  (preview, expires in 1h)
```

### The design rule that matters most

Prot's gateway routes by the `Host` header. Pointing a tunnel at the gateway would expose **every** alias. Instead:

- Each exposure gets its **own listener on a random loopback port** that serves exactly one service.
- The provider tunnels to that listener, **never** to the gateway, the admin API or the MCP endpoint.
- Because every public request passes through that listener, Prot can enforce access, limits and logging itself. The
  same rules then apply whichever provider carries the traffic.

### Providers

Prot would not run a public relay. That brings hosting cost, abuse handling, bandwidth and operational security, so it
starts by driving tools that exist. Like the SSH tunnels, which run `ssh`, an exposure runs the provider's own program as
a supervised child, reads the public URL it reports, and restarts or stops it. Prot does not download these programs.

| Provider | What it needs | Limits and notes (from their documentation) | Likely use |
| --- | --- | --- | --- |
| [Cloudflare Quick Tunnel](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/) | `cloudflared`; no account or domain | New hostname each time. No uptime guarantee. 200 in-flight requests, then `429`. No Server-Sent Events. Optional email one-time-PIN gate (`--allowed-mail`) that works only in an interactive browser | First provider; previews |
| [Tailscale Funnel](https://tailscale.com/docs/features/tailscale-funnel) | Tailscale, MagicDNS, HTTPS certificates, a `funnel` attribute in the tailnet policy | Only your `ts.net` name, only ports 443, 8443 and 10000, non-configurable bandwidth limits, beta | Stable URL for previews. Tailscale Serve is the tailnet-only counterpart for private mode |
| [ngrok](https://ngrok.com/docs/share-localhost/inspection) | An account and auth token | Traffic Inspector with request replay, and a [Verify Webhook](https://ngrok.com/docs/gateway/traffic-policy/actions/verify-webhook) policy action | Webhook development |
| Self-hosted relay | A server you run | Full control, and the cost of operating it | Later, if there is demand |

The email gate on a Quick Tunnel is the reason Prot enforces access in its own listener: a gate that needs a person in a
browser would block automated webhook deliveries.

### Modes

Browser login is right for a preview and wrong for a webhook, so there are three modes with different defaults.

| Mode | For | Protection |
| --- | --- | --- |
| Preview | Showing an app to a teammate or client | An access code or link token checked by Prot, plus an expiry |
| Webhook | GitHub, Stripe and similar callbacks | No login. A path allowlist, a method allowlist, a body size limit and signature verification |
| Private | Your own devices | The provider's private network (Tailscale Serve); no public URL |

Webhook mode verifies signatures and does not rely on the URL being secret:

- GitHub sends `X-Hub-Signature-256: sha256=<hex>`, an HMAC-SHA256 of the raw body
  ([docs](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)).
- Stripe sends `Stripe-Signature: t=<timestamp>,v1=<hex>`, where the signed string is the timestamp, a dot and the body.
  The timestamp must be checked against a tolerance to stop replays.
- Secrets are kept in the OS keyring, like tunnel passwords, and are never printed. A rejected request is logged with
  the reason.

### Safety rules

These are requirements, not options:

1. **Off by default.** Exposure is disabled until you turn it on, and every exposure is an explicit command.
2. **One service only.** Nothing but the chosen service is reachable. The gateway, admin API and MCP endpoint are never
   exposed.
3. **Always expires.** There is a default lifetime (a proposed 1 hour) and a configurable maximum. An exposure also ends
   when its service is deleted or the daemon stops, and any provider process left over is cleaned up at daemon start.
4. **Say what is public.** Creating an exposure prints what will be reachable and asks for confirmation. HTTPS only
   encrypts traffic. A dev server can still have debug endpoints, unauthenticated admin routes or secrets, and that
   warning is part of the flow.
5. **Agents do not publish.** The MCP rule for SSH tunnels carries over: agents may list exposures and read their status
   and requests, but cannot create or stop one. A possible later step is an agent *requesting* an exposure that the
   desktop app shows as a pending confirmation. That is an open question, not a plan.

### Request inspector

Since requests pass through Prot's listener, Prot can record them without depending on a provider: method, path, status,
duration and headers, with `Authorization`, `Cookie`, `Set-Cookie` and signature headers redacted, in a bounded buffer.
They would show next to the service's logs in the desktop app and the CLI. Replay would default to safe methods (`GET`,
`HEAD`), and replaying a `POST` would need an explicit flag.

Request inspection and replay are not new: ngrok has both. What Prot can add is tying a request to the supervised
process that answered it, by service and time, so a failing request sits next to the log lines it caused. That is
correlation by time and service, not tracing, unless the app logs a request ID.

### Phases

| Phase | Scope |
| --- | --- |
| A | Local HTTPS: `prot tls enable`, `trust`, `status`, `untrust` |
| B | Exposure core: per-exposure listener, expiry, the Cloudflare Quick Tunnel provider, preview mode with an access code, `prot expose`, `unexpose`, `exposures`, a desktop panel |
| C | Webhook mode: allowlists, signature verification, the request inspector |
| D | More providers (Tailscale Serve and Funnel, ngrok) and private mode |
| E | Self-hosted relay; agent-requested exposure; previews of a whole isolated workspace, which depends on workspaces that Prot does not have yet |

### Not planned

- A hosted public relay run by Prot.
- Exposing the admin API or MCP endpoint, under any mode.
- Downloading or updating third-party tunnel programs for you.
- Anything meant for production traffic. This is for development.

## Sources

- [Portless: HTTPS and TLS](https://deepwiki.com/vercel-labs/portless/3.2-https-and-tls) and its
  [README](https://github.com/vercel-labs/portless)
- [mkcert](https://github.com/FiloSottile/mkcert): trust stores and the key warning
- [Cloudflare Quick Tunnels](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)
- [Tailscale Funnel](https://tailscale.com/docs/features/tailscale-funnel)
- [ngrok Traffic Inspector](https://ngrok.com/docs/share-localhost/inspection)
- [GitHub: validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)
- [W3C Secure Contexts](https://www.w3.org/TR/secure-contexts/)
