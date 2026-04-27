# Packaging the gil VS Code extension

This is the operator-facing recipe for producing a publish-ready
`.vsix` from `vscode/`. The README covers what the extension does at
runtime; this file covers how to build it.

## Verified smoke (2026-04-27)

Run on Linux with Node 22.22.2 and npm 10.9.7:

```bash
cd vscode
npm install
npm run package
```

Output (paths trimmed):

```text
> gil@0.1.0 package
> npm run build -- --production && vsce package --no-yarn

[gil-vscode] build started
[gil-vscode] build finished (0 errors)
 WARNING  A 'repository' field is missing from the 'package.json' manifest file.
 WARNING  LICENSE, LICENSE.md, or LICENSE.txt not found
 INFO  Files included in the VSIX:
gil-0.1.0.vsix
├─ [Content_Types].xml
├─ extension.vsixmanifest
└─ extension/
   ├─ package.json [2.01 KB]
   ├─ readme.md
   └─ dist/
      ├─ extension.js [416.12 KB]
      └─ webview.html [7.47 KB]

The file extension/dist/extension.js is large (416.12 KB)

 DONE  Packaged: vscode/gil-0.1.0.vsix (6 files, 118.48 KB)
```

Total `.vsix` size: **118 KB** (well under any reasonable threshold).
TypeScript compiled without errors. The bundle is single-file CJS
targeted at Node 20, which is what VS Code 1.84+ ships.

To install the produced artifact locally:

```bash
code --install-extension vscode/gil-0.1.0.vsix
```

Marketplace publish is **not** automated — that requires a publisher
account on `vsce login` and is an explicit operator action.

## Phase 17 follow-ups surfaced by this smoke

The package builds and would install, but the manifest and contents
are not yet publish-ready. Three issues were observed during the
smoke and are filed for Phase 17:

1. **`proto/gil/v1/*.proto` not bundled.** `src/gild_client.ts` calls
   `findProtoDir` at activation, which looks for `session.proto`
   relative to the extension install directory. The package above
   ships only `dist/` — so the proto loader will fail at runtime once
   the extension is installed from the marketplace (a source checkout
   works because the repo-root protos are reachable). Fix: copy
   `../proto/gil/v1/*.proto` into `vscode/proto/gil/v1/` as a build
   step (esbuild plugin, mirror the `copyWebviewAssets` pattern), and
   drop the `proto/**` line from `.vscodeignore`.

2. **`repository` field missing in `package.json`.** vsce warns and
   suggests `--allow-missing-repository`. Fix: add
   `"repository": { "type": "git", "url": "https://github.com/mindungil/GIL.git", "directory": "vscode" }`.

3. **No `LICENSE` next to the manifest.** The repo-root LICENSE is
   not copied into `vscode/`. Fix: symlink or copy
   `vscode/LICENSE` from the repo root before packaging (or add a
   prepackage step).

None of these block local install or daemon connectivity; they only
matter for a clean Marketplace listing. The runtime issue (1) bites
the moment the extension is installed from a `.vsix` rather than run
from a source checkout.

## What's intentionally not in this file

Bundle minification, sourcemap controls, watch mode, and dev install
hooks live in the README. This file documents only the
package-for-distribution recipe and the verified state of the
artifact.
