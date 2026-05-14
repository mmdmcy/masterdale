# Architecture

Masterdale is one repository with three command surfaces and shared internal packages.

```text
cmd/
  dale/       memory, fleet access, file/search APIs, MCP-style endpoint
  autodale/   monitoring, tasks, QA, local AI fleet reports
  comdale/    approval-gated commerce drafts and campaigns

internal/
  dale/       event store, HTTP server, fleet, files, exec, Git audit, Ollama, MCP
  autodale/   metrics, tasks, checks, Codex QA
  comdale/    business profile, drafts, adapters, events
  envfile/    repo-local .env loading
```

## Core Runtime

`daled` is the central node agent. It runs an HTTP server with:

- `/dashboard`
- `/v1/dashboard`
- `/v1/dashboard/ask`
- `/healthz`
- `/v1/events`
- `/v1/resources/search`
- `/v1/node/report`
- `/v1/fs/list`
- `/v1/fs/read`
- `/v1/fs/search`
- `/v1/git/audit`
- `/v1/exec`
- `/v1/llm/complete`
- `/mcp`

The endpoint set is intentionally small. The goal is not to expose the whole machine; it is to expose enough safe state for an AI operator to reason about the device.

Dashboard reads are lightweight snapshots. `daled` caches dashboard components independently: chat events refresh quickly, while Git, metrics, and fleet discovery use longer TTLs so the browser can stay live without repeatedly running expensive local inspections.

## Data Model

The base data primitive is `masterdale.event.v1`, stored as signed JSONL.

Events contain:

- stable id
- timestamp
- device id
- actor
- channel
- kind
- body
- refs
- hash

This keeps v1 simple while leaving room for SQLite projections, replication, or external indexing later.

## Trust Boundaries

The important boundary is not "inside the process." It is the network and action boundary:

- Localhost requests are trusted for developer ergonomics.
- Remote requests are limited to private-network source IPs by default.
- Remote API requests require a bearer token.
- File operations are limited to configured safe roots.
- Secret/runtime filenames are blocked from file reads/searches.
- Remote command execution requires `DALE_REMOTE_EXEC=1`.
- Commerce actions are drafts only; no automatic publishing.

## Fleet Flow

1. Each device runs `go run ./cmd/dale up`.
2. `dale up` creates or updates `.env`, starts `daled`, and uses `DALE_REMOTE_SCOPE=private`.
3. The controller discovers devices through Tailscale when available.
4. The controller probes `/healthz`, then uses token-authenticated endpoints for deeper checks.
5. `autodale agent fleet-report` combines local metrics, local Git audit, remote Git audit, and local Ollama summarization.

The same HTTP APIs can work on a normal private LAN when devices are known by hostname/IP. Tailscale is convenient, not a hard dependency of the server security model.

## Local AI Flow

Local model access is routed through Ollama:

- `dale` provides `/v1/llm/complete`.
- `dale ask` lets a local model answer questions about the repository using bounded docs context.
- `comdale` uses local models for drafts when available and deterministic templates otherwise.
- `autodale` uses local models for fleet reports and deterministic fallback summaries when model output fails or times out.

Model selection has two layers. The default `primary` strategy keeps one all-round model warm for efficiency. The `role` strategy can still route fast, text, context, structured, reasoning, and vision work to different Ollama models when specialization matters. This lets the system keep moving even on small laptops where local models are slow or inconsistent.

## Extension Points

Good next adapters fit one of these shapes:

- new read-only check in `internal/autodale`
- new event-producing command under `cmd/autodale`
- new approval-gated draft in `internal/comdale`
- new `dale` endpoint that exposes safe device state
- new MCP prompt/tool backed by the event store
- new projection/index over `masterdale.event.v1`

Avoid adding broad remote mutation. Prefer narrow actions with explicit policy, logging, and approval.

## Deployment Profiles

- Local-only: `DALE_REMOTE_SCOPE=loopback`, use only on one machine.
- Office/private LAN: `DALE_REMOTE_SCOPE=private`, shared token, firewall limited to trusted networks.
- Tailnet: `DALE_REMOTE_SCOPE=tailnet` or default `private`, shared token, Tailscale discovery.
- Public/VPS: `DALE_REMOTE_SCOPE=public` only behind TLS, firewall, reverse proxy, and stronger identity controls.

The default profile is private LAN/Tailscale because it is useful without being accidentally public.
