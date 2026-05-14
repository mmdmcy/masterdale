# Local Model Routing

Masterdale supports two model strategies:

- `primary`: default; keep one useful all-round model warm for normal work.
- `role`: route each job to the model that tested best for that role.

The default primary model is `gemma4:e2b`. It is heavier than the tiny models, but it tested best for repository Q&A and can stay resident so repeated answers are faster.

## Current Roles

| Role | Model | Use |
| --- | --- | --- |
| `primary` | `gemma4:e2b` | default all-rounder for normal work |
| `fast` | `qwen3.5:0.8b` | quick status, short chat, simple first pass |
| `text` | `ministral-3:3b` | normal summaries, business drafts, operator prose |
| `context` | `gemma4:e2b` | repo navigation and docs-grounded Q&A |
| `structured` | `nemotron-3-nano:4b` | concise JSON-style reports and low-fluff facts |
| `reasoning` | `qwen3.5:4b` | deeper analysis when waiting several minutes is acceptable |
| `vision` | `gemma4:e2b` | image/vision tasks and possible text fallback |

`gemma4:e2b` was surprisingly strong for repository Q&A. It is still the largest installed model here, so it is the default primary model, while smaller models remain available for explicit fast or role-specific runs.

With the default `primary` strategy, normal text/context/structured work starts with `gemma4:e2b` to avoid constant model swapping. Explicit `--fast`, `--deep`, `--model`, or `DALE_MODEL_STRATEGY=role` opt into other models.

## May 14, 2026 Test

Prompt: summarize Masterdale battery/electricity monitoring from actual `autodale` metrics.

| Model | Chat time | Result |
| --- | ---: | --- |
| `qwen3.5:0.8b` | 43s | usable simple summary |
| `qwen3.5:2b` | 79s | usable, but misread free memory/disk as utilization |
| `qwen3.5:4b` | 196s | best cautious summary, slow |
| `ministral-3:3b` | 110s | good practical summary |
| `nemotron-3-nano:4b` | 109s | concise and factual |
| `gemma4:e2b` | 78s | good short summary |

Ollama `/api/chat` worked much better than `/api/generate` for this set. Some `/api/generate` calls returned blank visible text even after long waits.

Follow-up repo-QA test through `dale ask`:

| Model / role | Context | Time | Result |
| --- | --- | ---: | --- |
| `qwen3.5:0.8b` / `fast` | reduced keyword docs | 41-60s | usable but needs tight context to avoid overclaiming |
| `ministral-3:3b` / `text` | architecture docs | 135s | accurate, slower |
| `gemma4:e2b` / `context` | architecture docs | 59s cold-ish, 23s warm | accurate and faster for repo navigation |

Fleet report test through `autodale agent fleet-report`:

| Mode | Model | Time | Result |
| --- | --- | ---: | --- |
| `--fast` | `qwen3.5:0.8b` | 63s | failed structured JSON, deterministic fallback worked |
| default primary | `gemma4:e2b` | 73s warm | valid JSON operator report |
| role structured | `nemotron-3-nano:4b` | 137s | valid JSON operator report |

Warm-ish JSON micro-bench through `dale models bench` after models had already been exercised. Times still vary depending on what Ollama has resident:

| Model | Observed time | Result |
| --- | ---: | --- |
| `gemma4:e2b` | 5-25s | ok |
| `qwen3.5:0.8b` | 4-11s | ok |
| `ministral-3:3b` | 7-88s | ok, inconsistent |
| `nemotron-3-nano:4b` | 17-18s | ok |
| `qwen3.5:4b` | 24-27s | ok |
| `qwen3.5:2b` | 17-22s | ok |

## Keep-Alive Behavior

Masterdale requests use `keep_alive` where useful. A model that is already loaded usually answers faster than a cold model, but only one or two large models should stay loaded on this laptop at once. Use `ollama ps` to see what is resident.

## Useful Commands

```bash
go run ./cmd/dale models bench
go run ./cmd/dale ask --fast "What is Masterdale?"
go run ./cmd/dale ask "How does Masterdale secure remote device access?"
go run ./cmd/dale ask --role text "Draft a short explanation of Masterdale."
go run ./cmd/dale ask --deep --timeout 600 "What should the architecture improve next?"
go run ./cmd/autodale agent fleet-report --fast --kwh-cost 0.40
go run ./cmd/autodale agent fleet-report --deep --ai-timeout 600 --kwh-cost 0.40
```

Useful environment overrides:

```env
DALE_MODEL=gemma4:e2b
DALE_MODEL_STRATEGY=primary
```

Use `DALE_MODEL_STRATEGY=role` when you want the specialized router instead of the one-warm-model default.
