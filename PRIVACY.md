# Privacy policy

gil is **local-first**. We do not collect, transmit, or sell your data.

## What stays local

- All session data (`$XDG_DATA_HOME/gil/sessions/`)
- All credentials (`$XDG_CONFIG_HOME/gil/auth.json`, mode 0600)
- All logs (`$XDG_STATE_HOME/gil/logs/`)
- All caches (`$XDG_CACHE_HOME/gil/`)

These never leave your machine unless you explicitly export or share them (`gil export`, manual file copy, etc.).

## What goes to LLM providers

When you run a session, gil sends to your configured provider (Anthropic, OpenAI, etc.):

- The system prompt (gil's own instructions + your AGENTS.md/CLAUDE.md)
- Your interview answers
- Tool call results (file contents read via `read_file`, command output via `bash`, etc.)
- Memory bank contents (across iterations)

Each provider's own privacy policy applies to data after it leaves gil. Consult:

- Anthropic: https://www.anthropic.com/legal/privacy
- OpenAI: https://openai.com/policies/privacy-policy
- OpenRouter: https://openrouter.ai/privacy
- Self-hosted (vLLM, etc.): your responsibility

gil does NOT add any header, parameter, or beacon to provider requests for tracking purposes.

## Telemetry

gil does **not** send telemetry. There is no opt-out because there is no opt-in.

If you enable Prometheus metrics (`gild --metrics :PORT`), the metrics are exposed on a local TCP port — your responsibility to scrape or block. We never push to an external endpoint.

## Logs

gil writes structured logs to `$XDG_STATE_HOME/gil/logs/`. These are local files. Secrets are masked at write time (`sk-ant-...`, `Bearer X`, `password=Y` patterns are redacted before disk write).

## Updates

`gil update` checks https://api.github.com/repos/jedutools/gil/releases/latest. This is a public endpoint and does not include any user-identifying information beyond standard GitHub API request metadata (User-Agent header, IP address). You can disable update checks by never running `gil update`.

## Contact

Privacy questions: privacy@jedutools.io
