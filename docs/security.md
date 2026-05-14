# Security Model

Masterdale is powerful because it makes machines inspectable. That means the default posture must be conservative enough for reuse outside a personal Tailnet.

## Default Network Policy

`DALE_REMOTE_SCOPE=private` is the default.

It allows:

- loopback
- RFC1918 IPv4 private LANs
- IPv4 link-local
- IPv6 unique local addresses
- IPv6 link-local
- Tailscale IPv4 and IPv6 ranges

It blocks:

- public internet source IPs

Available scopes:

| Scope | Allowed clients |
| --- | --- |
| `loopback` | local machine only |
| `tailnet` | loopback + Tailscale |
| `private` | loopback + private LAN + Tailscale |
| `public` | any source IP |

Use `public` only behind a firewall, TLS reverse proxy, VPN, or another trusted perimeter. Do not port-forward `daled` directly to the internet.

## Authentication

Non-localhost API requests require:

```http
Authorization: Bearer <DALE_TOKEN>
```

The bearer token is shared by devices for the current prototype. It is stored in `.env`, which is ignored by git. Token comparison uses constant-time comparison.

Future hardening should replace shared-token-only trust with per-device identity, mTLS, signed requests, or short-lived session tokens.

## Endpoint Risk

| Surface | Risk | Current control |
| --- | --- | --- |
| `/healthz` | service discovery | non-sensitive response only |
| `/dashboard` | service discovery | shell only; protected data still uses API auth remotely |
| `/v1/dashboard*` | aggregated local status disclosure | bearer token required remotely |
| file list/read/search | data disclosure | safe roots and blocked secret/runtime filenames |
| Git audit | repo metadata disclosure | bearer token required remotely |
| remote exec | arbitrary command execution | `DALE_REMOTE_EXEC=1`, safe cwd roots, timeout, output limits |
| Ollama complete | prompt/data disclosure | local model endpoint only |
| events/MCP | context leakage | bearer token required remotely |

## Remote Exec Policy

Remote command execution is the sharpest tool in the project.

Rules:

- Disabled unless `DALE_REMOTE_EXEC=1`.
- CWD must resolve inside configured safe roots.
- Commands time out.
- Output is truncated.
- No package install, publishing, or destructive workflow should be automated without a stronger approval/policy layer.

For demos where inspection is enough, set:

```env
DALE_REMOTE_EXEC=0
```

## Data At Rest

Runtime files stay outside git by default:

- `.env`
- event logs
- metrics logs
- tasks logs
- config files
- local databases
- build artifacts

The default data directories are under `~/.local/share/*`.

## Known Gaps

- Shared bearer token instead of per-device identity.
- No TLS inside `daled`; rely on private networks, Tailscale, VPN, or reverse proxy.
- No role-based permissions yet.
- Remote exec is command-level, not policy-scoped.
- File safe roots are config-based, not OS sandboxed.

These are acceptable for a prototype and local/private demo, but they should be addressed before production use in a company environment.
