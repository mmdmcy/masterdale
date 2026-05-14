# Security Policy

Masterdale is intended for local and private-network environments. Do not expose `daled` directly to the public internet.

## Supported Versions

The public repository currently supports the latest `main` branch only.

## Reporting A Vulnerability

Please report vulnerabilities through GitHub private vulnerability reporting when available, or open a minimal public issue that avoids sensitive details and ask for a private contact path.

Include:

- affected command or endpoint
- expected impact
- reproduction steps
- whether remote command execution or file APIs are involved

## Security Defaults

- Non-localhost API access requires bearer-token authentication.
- Public internet source IPs are blocked unless `DALE_REMOTE_SCOPE=public` is explicitly configured.
- Remote command execution is disabled unless `DALE_REMOTE_EXEC=1`.
- File APIs are limited to configured safe roots and refuse known secret/runtime filenames.
- Runtime state and `.env` files are ignored by Git.

See [docs/security.md](docs/security.md) for the detailed threat model.
