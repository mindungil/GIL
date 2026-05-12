# Security policy

## Reporting

Report vulnerabilities **privately** via GitHub Security Advisory:
https://github.com/mindungil/GIL/security/advisories/new

Or email: alswnsrlf12@naver.com

We will respond within 7 days and target a fix within 30 days for high-severity issues.

Please do not file public GitHub issues for vulnerabilities.

## Supported versions

Only the latest tagged release is supported with security fixes. Pre-1.0 software — security maintenance is best-effort.

| Version | Supported |
|---|---|
| latest tag (v0.x) | yes |
| older tags | no |
| `main` branch | best-effort |

## Threat model

gil drives an LLM agent that executes commands in a workspace. The default operating mode is **untrusted code in untrusted workspaces** — assume the LLM may be wrong, the workspace may contain malicious files, and tool outputs may be hostile.

### What gil protects

- **Sandbox by default for sensitive backends**: `LOCAL_SANDBOX` (bwrap), `LOCAL_NATIVE` with permission gates, `DOCKER` (per-command exec), cloud backends (Modal/Daytona) are containerized.
- **Permission gates**: `spec.risk.autonomy` controls what the agent can run without approval. `ASK_PER_ACTION` requires per-command approval; `PLAN_ONLY` blocks all execution.
- **Credentials at rest**: API keys stored at `$XDG_CONFIG_HOME/gil/auth.json`, mode 0600 (POSIX). Never logged. Never sent in events.
- **Secret masking**: event persister scrubs `sk-ant-...`, `Bearer X`, `password=Y`, etc. before writing to disk.
- **Shadow Git checkpoints**: gil's own checkpoint git lives outside your workspace's `.git/` — your repo history is untouched.
- **OIDC auth (optional)**: gild can require bearer tokens on TCP/HTTP listeners (UDS bypass remains the default for local-trusted use).

### What gil does NOT protect (yet)

- **Local user trust**: gil assumes anyone with read access to `$XDG_CONFIG_HOME/gil/auth.json` is authorized. Multi-user systems should use OS-level user separation + per-user `gild --user`.
- **LOCAL_NATIVE without sandbox**: if `spec.workspace.backend = LOCAL_NATIVE` AND `autonomy = FULL`, the agent runs as your user with no isolation. Don't use this combination on workspaces you don't trust.
- **Network**: gil places no network restrictions. Use a network-isolated sandbox/VM if needed.
- **Supply chain**: external MCP servers run as configured (`gil mcp add ...`). Vet the binaries you register.
- **Prompt injection**: tool outputs flow back to the LLM. Hostile content in workspace files (e.g. README "ignore previous instructions") may influence the agent. We recommend running with `ASK_DESTRUCTIVE_ONLY` or stricter on untrusted workspaces.

## Best practices

- Use the autonomy dial intentionally: `ASK_DESTRUCTIVE_ONLY` is a sensible default for most projects.
- Run with `LOCAL_SANDBOX` (bwrap on Linux) when the workspace contains untrusted code.
- Never share `auth.json` — keys are personal.
- Use `--auth-issuer` (OIDC) when exposing gild over TCP.

## Disclosure timeline

After we ship a fix, we will:
- Publish a GitHub Security Advisory with CVE if applicable.
- Credit the reporter (unless you prefer otherwise).
- Document the impact + remediation in `CHANGELOG.md`.
