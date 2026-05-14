# Operations

This guide is the practical runbook for demos and private-network use.

## Prerequisites

- Go 1.23+
- Git
- Optional: Ollama with a local model
- Optional: Tailscale for easy device discovery

## First Run

```bash
make check
go run ./cmd/dale env init
go run ./cmd/dale up
```

Copy the generated `DALE_TOKEN` into `.env` on every device that should join the same private fleet.

## Device Agent

Start or refresh the agent:

```bash
go run ./cmd/dale up
go run ./cmd/dale up --restart
```

Stop it:

```bash
go run ./cmd/dale down
```

Check local status:

```bash
go run ./cmd/dale status
```

## Dashboard

Start the node agent and open the built-in dashboard:

```bash
go run ./cmd/dale up
```

Then visit `http://127.0.0.1:7345/dashboard`.

The dashboard shows node status, Git workspaces, local energy metrics, fleet discovery, recent events, model routing, and an agent chat surface. The UI stays live through lightweight polling; `daled` serves cached component snapshots so Git and fleet inspection are not recomputed on every browser update. On another private-network device, open `http://<device-ip>:7345/dashboard`; the page will prompt for `DALE_TOKEN` before loading protected dashboard data.

## Private-Network Modes

Default:

```env
DALE_REMOTE_SCOPE=private
```

Use stricter modes when needed:

```env
DALE_REMOTE_SCOPE=loopback
DALE_REMOTE_SCOPE=tailnet
```

Use public mode only behind real network protection:

```env
DALE_REMOTE_SCOPE=public
```

## Fleet Checks

```bash
go run ./cmd/dale fleet probe
go run ./cmd/dale fleet devices
go run ./cmd/dale fleet doctor --device <device>
go run ./cmd/dale fleet git-audit --device <device> --fetch
```

If a device is visible but unreachable:

- check that `daled` is running on the target
- check Windows/macOS/Linux firewall rules
- check that the port matches
- check that `DALE_REMOTE_SCOPE` allows the source network
- check that both devices use the same `DALE_TOKEN`

## Monitoring

Take one sample:

```bash
go run ./cmd/autodale monitor sample
```

Compute local-day energy:

```bash
go run ./cmd/autodale monitor daily --kwh-cost 0.40
```

Run a long watch:

```bash
go run ./cmd/autodale monitor watch --interval 1m
```

Energy is estimated unless the platform exposes battery power or a real external meter is integrated.

## Local AI Operator Report

```bash
go run ./cmd/autodale agent fleet-report --device <device> --fetch --kwh-cost 0.40 --ai-timeout 300 --max-tokens 700
```

On slow laptops, the patient default is intentional. Use `--fast` only for lower-quality quick checks.
Use `--deep` when you want the larger reasoning model and are willing to wait.

Talk to the local model about this repo:

```bash
go run ./cmd/dale ask --fast "What is Masterdale?"
go run ./cmd/dale ask "How is Masterdale organized?"
go run ./cmd/dale ask --deep --timeout 600 "What should I improve before showing this portfolio?"
```

Masterdale defaults to a primary all-round model, currently `gemma4:e2b`, so one useful model can stay warm. Use `--fast` for cheap checks, `--deep` for explicit reasoning, or set `DALE_MODEL_STRATEGY=role` when you want task-specific model routing.

## Generic SSH Check

```bash
go run ./cmd/autodale check ssh <host> --port 22
```

## Demo Script

```bash
make check
go run ./cmd/dale git audit --fetch
go run ./cmd/dale ask "How is Masterdale organized?"
go run ./cmd/dale fleet doctor --device <device>
go run ./cmd/dale fleet git-audit --device <device> --fetch
go run ./cmd/autodale monitor sample
go run ./cmd/autodale monitor daily --kwh-cost 0.40
go run ./cmd/autodale agent fleet-report --device <device> --fetch --kwh-cost 0.40 --ai-timeout 300 --max-tokens 700
go run ./cmd/comdale --profile profiles/example-business.json campaign "Masterdale private AI operations"
```

## Release Hygiene

Before showing the repo:

```bash
make check
make hygiene
```

Do not commit runtime state, `.env`, model files, build artifacts, logs, or local credentials.
