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

## Commands

This repository builds three Go binaries:

- `dale`: node agent, dashboard, event log, local search, Ollama bridge, fleet APIs, and remote-device access.
- `autodale`: monitoring, scheduled tasks, QA checks, SSH checks, and local AI operator reports.
- `comdale`: approval-gated drafts, campaign plans, and profile-based repo scanning.

## Quick Start

Prerequisites:

- Go 1.23+
- Git
- Optional: Ollama with a local model
- Optional: Tailscale for easy private-fleet discovery

```bash
make check
go run ./cmd/dale env init
go run ./cmd/dale up
go run ./cmd/dale ask "What is Masterdale?"
```

Then visit:

```text
http://127.0.0.1:7345/dashboard
```

Useful commands:

```bash
go run ./cmd/dale git audit
go run ./cmd/dale fleet devices
go run ./cmd/dale remote list --device <device>
go run ./cmd/dale remote search --device <device> --query TODO

go run ./cmd/autodale monitor sample
go run ./cmd/autodale monitor daily --kwh-cost 0.40
go run ./cmd/autodale agent fleet-report --ai-timeout 300 --max-tokens 700

go run ./cmd/comdale draft --type post --topic "local AI automation"
go run ./cmd/comdale --profile profiles/example-business.json campaign "Masterdale demo"
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

Optional private dashboard context can be added without committing it:

```env
DALE_DASHBOARD_CONTEXT_DOCS=/path/to/private-notes.md:/path/to/team-runbook.md
```

## Security Defaults

Masterdale is built for private networks, not direct public exposure.

- Public-internet clients are blocked unless `DALE_REMOTE_SCOPE=public` is explicitly set.
- Non-localhost API requests require `Authorization: Bearer <DALE_TOKEN>`.
- File APIs stay inside configured safe roots and refuse known secret/runtime filenames.
- Remote command execution requires `DALE_REMOTE_EXEC=1`, safe roots, timeouts, and clear operator intent.
- `comdale` drafts and plans only; it does not publish or contact customers automatically.

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
