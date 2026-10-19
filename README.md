# Masterdale

Masterdale is a local-first AI operations layer for private device fleets. It lets one controller inspect trusted machines, search safe file roots, audit Git workspaces, sample system and energy metrics, and ask a local Ollama model for operator help.

The project is designed for small teams, labs, homelabs, and edge environments that want useful AI automation without sending operational context to a cloud model by default.

## What It Does

- Runs local AI through Ollama.
- Serves a private dashboard and chat workspace.
- Keeps append-only `masterdale.event.v1` event logs.
- Exposes MCP-style local resources and prompts.
- Discovers private-network devices when Tailscale is available.
- Lists, reads, and searches only configured safe roots.
- Audits Git workspaces across trusted machines.
- Samples CPU, memory, disk, battery, and estimated energy usage.
- Produces local AI operator reports with deterministic fallbacks.
- Drafts commerce/content material through explicit approval gates.
- Keeps remote command execution disabled unless explicitly enabled.
- Shows LinuxMice fleet views at `/fleet` when `lmice` is installed: repositories and
  folders across devices, per-location diffs, integrations and Plugroot-hosted services.
- Vault tab (`lmice vault`): personal, business and work vaults with their own
  keys; add logins in a form, copy without displaying, rotate a token everywhere
  it is copied, and answer agents' requests to use a secret (also from the phone).

## Commands

This repository builds three Go binaries:

- `dale`: node agent, dashboard, event log, local search, Ollama bridge, fleet APIs, and remote-device access.
- `autodale`: monitoring, scheduled tasks, QA checks, SSH checks, and local AI operator reports.
- `comdale`: approval-gated drafts, campaign plans, and profile-based repo scanning.

Masterdale builds with the Go standard library only: `go.mod` declares no
third-party dependencies, so builds work fully offline. Optional OIDC browser
sign-in is implemented directly on `net/http` and `crypto`.

## Quick Start

Prerequisites:

- Go 1.24+
- Git
- Optional: Ollama with a local model
- Optional: Tailscale for easy private-fleet discovery

```bash
make check
go run ./cmd/dale env init
go run ./cmd/dale up
```

`dale up` runs in the foreground. Leave that terminal open while using the
agent; press Ctrl-C to stop it. A new `.env` keeps remote command execution
off (`DALE_REMOTE_EXEC=0`); `dale up --exec` turns it on, and an existing
`.env` keeps whatever it says.

In another terminal:

```bash
go run ./cmd/dale ask "What is Masterdale?"
```

Then visit:

```text
http://127.0.0.1:7345/dashboard
```

With [LinuxMice](https://github.com/mmdmcy/linuxmice) installed, `http://127.0.0.1:7345/fleet`
shows its read-only views. The page is a thin wrapper: each view runs a fixed
`lmice ... --format json` command (shown on the page) and passes its JSON
through `GET /v1/linuxmice/{map,devices,integrations,selfhost,rescue,diff}`, so
anything the page shows can be reproduced from the CLI. Plans (sync, retire,
carry) and changes run as background jobs under `/v1/linuxmice/jobs`; changes
(`sync`/`carry --execute`, integration opt-outs) are refused unless the server
sets `DALE_LINUXMICE_ACTIONS=1`, ask for confirmation showing the exact command,
and run one at a time. Joining a new device asks for its password, so it stays
a terminal command. Set `DALE_LMICE` to use a specific `lmice` binary.


Useful commands:

```bash
go run ./cmd/dale git audit
go run ./cmd/dale fleet devices
go run ./cmd/dale remote list --device <device>
go run ./cmd/dale remote search --device <device> --query TODO
go run ./cmd/dale fleet ssh                              # SSH bootstrap status
go run ./cmd/dale fleet ssh setup --device <device>      # token -> passwordless ssh

go run ./cmd/autodale monitor sample
go run ./cmd/autodale monitor daily --kwh-cost 0.40
go run ./cmd/autodale agent fleet-report --ai-timeout 300 --max-tokens 700

go run ./cmd/comdale draft --type post --topic "local AI automation"
go run ./cmd/comdale --profile profiles/example-business.json campaign "Masterdale demo"
```

If you intentionally want a detached background agent, use:

```bash
go run ./cmd/dale up --background
go run ./cmd/dale status
go run ./cmd/dale down
```

## Configuration

Runtime state is stored outside the repository by default:

- `dale`: `~/.local/share/learndale`
- `autodale`: `~/.local/share/autodale`
- `comdale`: `~/.local/share/comdale`

Use a repo-local `.env` for private settings. `.env` is ignored by Git.

```env
DALE_TOKEN=<shared private-network token>
DALE_REMOTE_SCOPE=private
DALE_REMOTE_EXEC=0
DALE_SAFE_ROOTS=/path/to/safe/root
DALE_MODEL=gemma4:e2b
DALE_MODEL_STRATEGY=primary
COMDALE_PROFILE=profiles/example-business.json
```

Optional LinuxMice browser SSO uses the five `DALE_OIDC_*` settings documented
in `.env.example`. It uses authorization code + S256 PKCE, nonce/state and a
short-lived browser-bound flow, requires an explicit owner-subject allowlist,
then discards provider tokens and issues a bounded opaque Masterdale session.
`DALE_TOKEN` remains the independent CLI/device automation path.

Optional private dashboard context can be added without committing it:

```env
DALE_DASHBOARD_CONTEXT_DOCS=/path/to/private-notes.md:/path/to/team-runbook.md
```

## Security Defaults

Masterdale is built for private networks, not direct public exposure.

- Public-internet clients are blocked unless `DALE_REMOTE_SCOPE=public` is explicitly set.
- Non-localhost API requests require `Authorization: Bearer <DALE_TOKEN>` or,
  when configured for a browser, an allowlisted LinuxMice OIDC session.
- File APIs stay inside configured safe roots and refuse known secret/runtime filenames.
- Remote command execution requires `DALE_REMOTE_EXEC=1`, safe roots, timeouts, and clear operator intent.
- Loopback is trusted by design: a direct loopback JSON request from any
  local process on the device passes without a token, `/v1/exec` included,
  as that process could run the command itself. Browsers are kept out of
  that path (loopback Origin, JSON only, no proxied requests), and
  state-changing JSON endpoints refuse a cross-site browser post even with
  the session cookie.
- `comdale` drafts and plans only; it does not publish or contact customers automatically.
- Vault actions need a signed-in OIDC session: the device token and local
  requests are refused. The phone's approval endpoint is authorised by a
  one-time code only the notification carries. `DALE_TOKEN` is re-read from
  `.env` when it changes (`DALE_ENV_FILE` names another file), so a rotated
  token applies without a restart.
- When `daled` runs as a sandboxed systemd service, LinuxMice change jobs run
  through the user's systemd manager (`DALE_LINUXMICE_RUNNER=direct` turns that off).

See [SECURITY.md](SECURITY.md) and [docs/security.md](docs/security.md) before using this outside a local/private environment.

## Documentation

- [Vision](docs/vision.md)
- [Architecture](docs/architecture.md)
- [Operations](docs/operations.md)
- [Security Model](docs/security.md)
- [Local Models](docs/local-models.md)
- [Event Contract](docs/event-contract.md)

## Status

Masterdale is an early public release. The core is working, but production deployments should add stronger device identity, scoped permissions, TLS or a trusted reverse proxy, and more precise policy controls before exposing it beyond a private network.
