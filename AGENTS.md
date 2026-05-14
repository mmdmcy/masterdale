# Masterdale Agent Notes

- This is one git repo with three commands: `dale`, `autodale`, and `comdale`.
- Keep the code centralized under root-level `cmd/`, `internal/`, `docs/`, and `profiles/`.
- Keep runtime state out of git; default data dirs should remain under `~/.local/share/*`.
- Local Ollama is the default model provider. Do not add cloud model calls unless explicitly requested.
- Prefer the primary-model strategy for normal work so one model can stay warm; use role-specific models only when the task benefits from it.
- `autodale` checks must remain read-only by default.
- `comdale` must draft and plan only; never publish, send, or contact customers automatically in v1.
- Preserve `masterdale.event.v1` compatibility across all commands.
- Fleet mode must stay inspection-first by default. Remote command execution exists for private-network operations, but must remain explicitly gated by `DALE_REMOTE_EXEC=1`, safe roots, timeouts, and clear operator intent.
