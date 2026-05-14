# Masterdale Vision

AI work is becoming distributed across laptops, servers, local models, shells, repositories, dashboards, and agent sessions. Most personal and small-team infrastructure still assumes a human manually switches context.

Masterdale is a local-first operations layer for that gap. It gives local agents enough trusted context, safe tools, and durable memory to help operators understand a private fleet without turning the fleet into an unsafe remote-control surface.

## Product Thesis

Local AI becomes useful when it has three things:

- trusted context
- safe tools
- a durable memory of what happened

Masterdale provides those primitives as a small Go control plane:

- `dale` exposes local memory, search, file inspection, Git audits, Ollama access, and private-network fleet APIs.
- `autodale` watches systems over time and produces operator reports.
- `comdale` turns business intent into approval-gated drafts and campaign plans.

The key design choice is local-first autonomy. Small local models can be useful when the system around them provides schemas, bounded context, deterministic checks, templates, validation, and explicit approvals.

## Design Principles

- Local first: Ollama and local files are the default.
- Private by default: public internet exposure is blocked unless explicitly configured.
- Small binaries: Go commands are easy to build, ship, and inspect.
- Event contracts over tight coupling: commands cooperate through signed JSONL events and HTTP APIs.
- Human approval for external impact: drafting is allowed; publishing and destructive actions are not automatic.
- Useful on weak hardware: deterministic checks and templates reduce load on small local models.
- Reusable transport: direct private LAN works, and Tailscale is a convenient optional discovery layer.

## North Star

Masterdale should become an AI operations backbone for small autonomous systems: private, explainable, cross-platform, secure enough to run in real environments, and simple enough that a single builder or small team can understand every moving part.
