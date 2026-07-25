# Masterdale Handoff

Last updated: 2026-07-25

This file is the editable working handoff for future sessions.

## Current Position

- Optional LinuxMice OIDC browser SSO sits beside the existing `DALE_TOKEN`
  bearer path.
- OIDC is all-or-nothing env config: issuer, client id/secret, redirect URL, and
  comma-separated allowed subject UUIDs must be set together.
- Flow is authorization code + S256 PKCE, state/nonce, browser-bound flow
  cookie, subject allowlist, then an opaque in-memory Masterdale session (max
  12h). Provider tokens are discarded after verification.
- Dashboard shell 401 with `X-Masterdale-OIDC: enabled` redirects the browser to
  `/auth/oidc/start` instead of only prompting for a bearer token.
- Direct loopback remains bearer-free only when the request is not reverse-
  proxied (`Forwarded` / `X-Forwarded-*` absent).

## Recent Changes

- New `internal/dale/oidc.go` + `oidc_test.go`
- `server.go` routes/middleware, `config.go` load, dashboard OIDC redirect hook
- Docs/env: `.env.example`, `README.md`, `docs/security.md`
- `go.mod` / `go.sum` pull in `go-oidc` and `oauth2`

## How To Extend Safely

- Keep `DALE_TOKEN` as the independent CLI/device automation credential.
- Do not put private issuer hostnames, real subject UUIDs, or secrets in tracked
  docs; use local `.env` only.
- Pair relying-party clients with LinuxMice Identity admin registration, then
  copy only the five `DALE_OIDC_*` values into ignored env.

## Verification Baseline

```sh
go test ./...
git diff --check
```

Verified 2026-07-25: `go test ./...` passed; `git diff --check` clean.

## Known Loose Ends

- No live production identity claim; dogfood against a local LinuxMice Identity
  instance before treating OIDC as operator-ready.
- Related optional clients live in RustOpViewer; LinuxMice remains the identity
  provider.
