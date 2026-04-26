# gil — VS Code extension

A slim VS Code panel for the **gil** autonomous coding harness. The extension talks to a locally running `gild` daemon over a Unix domain socket and lets you:

- list active gil sessions in a sidebar webview
- start a new session against a working directory
- run a session (detached) and see the result toast
- tail a session's event stream into the panel or an output channel

This extension is intentionally minimal: vanilla HTML+JS for the panel, gRPC over UDS for transport, no Anthropic SDK or model logic in-process. All planning, model calls, sandboxing, and verification happen in `gild`.

---

## Build

```bash
cd vscode
npm install
npm run build      # bundles src/extension.ts → dist/extension.js
```

`npm run build -- --production` produces a minified bundle without sourcemaps.

`npm run watch` runs esbuild in watch mode. `npm run check-types` runs `tsc --noEmit` for type-only validation.

## Package (.vsix)

```bash
cd vscode
npm run package    # produces gil-0.1.0.vsix
```

This invokes [`@vscode/vsce`](https://github.com/microsoft/vscode-vsce) under the hood. The first build also needs the protos copied next to the bundle so `gild_client.ts` can find them at runtime — the loader looks in (in order) `dist/../proto/gil/v1`, `proto/gil/v1` next to `dist/`, and the repo-root `proto/gil/v1`. When packaging, ensure `proto/gil/v1/*.proto` is included; see `.vscodeignore` for what is excluded.

## Install

```bash
code --install-extension gil-0.1.0.vsix
```

Or, in VS Code: **Extensions** → `…` → **Install from VSIX…**.

## Runtime requirements

- A running `gild` daemon. By default the extension connects to `~/.gil/gild.sock`. Override via:
  - environment variable `GILD_SOCKET=/path/to/gild.sock`, or
  - VS Code setting `gil.socketPath`.
- If the socket is missing on activation you'll see a non-blocking warning. Start `gild` and run the **gil: Refresh** command (or click "Refresh" in the panel).

## Commands

| Command            | What it does                                                |
| ------------------ | ----------------------------------------------------------- |
| `gil.startSession` | Prompt for a working dir + goal hint and create a session.  |
| `gil.runSession`   | Pick a session and call `RunService.Start(detach=true)`.    |
| `gil.tailEvents`   | Pick a session and stream events into the **gil** output channel. |
| `gil.openPanel`    | Focus the gil sidebar and reveal the Sessions webview view. |

## Notes on the Cline lift

The build pipeline (`package.json`/`tsconfig.json`/`esbuild.mjs`) follows Cline's shape so contributors who know that codebase can navigate this one quickly. We dropped: React + Vite webview, Anthropic/OpenAI/Bedrock/Gemini SDKs, MCP marketplace, browser tool, walkthroughs, locales, posthog telemetry, snapshot tests. None of those are needed for a session viewer.

## What's intentionally absent

- No model selection UI — gild owns the model config (per session via the spec).
- No prompt editing — interview happens via `gil interview` CLI or future TUI.
- No file diffing — VS Code's built-in diff view is sufficient when you open the worktree.
