# Packaging the gil VS Code extension

This is the operator-facing recipe for producing a publish-ready
`.vsix` from `vscode/`. The README covers what the extension does at
runtime; this file covers how to build it.

## Verified smoke (2026-04-27, Phase 17 Track C)

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
[gil-vscode] copied 5 proto file(s) to dist/proto/gil/v1
[gil-vscode] build finished (0 errors)
 INFO  Files included in the VSIX:
gil-0.1.0.vsix
├─ [Content_Types].xml
├─ extension.vsixmanifest
└─ extension/
   ├─ LICENSE.txt [1.04 KB]
   ├─ package.json [2.11 KB]
   ├─ readme.md
   └─ dist/
      ├─ extension.js [416.15 KB]
      ├─ webview.html [7.47 KB]
      └─ proto/
         └─ gil/
            └─ v1/
               ├─ event.proto [0.64 KB]
               ├─ interview.proto [2.1 KB]
               ├─ run.proto [6.93 KB]
               ├─ session.proto [2.32 KB]
               └─ spec.proto [2.6 KB]

The file extension/dist/extension.js is large (416.15 KB)

 DONE  Packaged: vscode/gil-0.1.0.vsix (12 files, 126.04 KB)
```

**Zero vsce warnings.** Total `.vsix` size: **126 KB**. The
`extension.js is large` line is an informational notice, not a
warning. TypeScript compiled without errors. The bundle is single-file
CJS targeted at Node 20 (what VS Code 1.84+ ships).

To install the produced artifact locally:

```bash
code --install-extension vscode/gil-0.1.0.vsix
```

If the `code` CLI is not on PATH (headless servers, CI), the manual
verification path is:

1. Copy the `.vsix` to a workstation that has VS Code installed.
2. From the VS Code command palette, run
   `Extensions: Install from VSIX...` and pick the file.
3. Open the gil sidebar (rocket icon) and confirm the panel renders
   without an error toast about `proto/gil/v1` lookup.
4. Configure `gil.socketPath` (or `$GILD_SOCKET`) and run
   `gil: List Sessions` from the command palette — a successful gRPC
   round-trip confirms the bundled protos loaded.

Marketplace publish is **not** automated — that requires a publisher
account on `vsce login` and is an explicit operator action.

## Phase 17 Track C — fixes applied

Three issues were filed by the prior smoke (Phase 16) and are now
resolved in this build:

1. **`proto/gil/v1/*.proto` bundled.** `vscode/esbuild.mjs` now
   includes a `copyProtoAssets` plugin that mirrors `copyWebviewAssets`:
   it copies `../proto/gil/v1/*.proto` into `vscode/dist/proto/gil/v1/`
   on every build. `src/gild_client.ts:findProtoDir` was updated to
   probe `<extension-root>/dist/proto/gil/v1` first, falling back to
   the source-tree paths used in dev. The packaged `.vsix` now ships
   all five `.proto` files under `dist/proto/gil/v1/`.

2. **`repository` field added.** `vscode/package.json` carries
   `"repository": { "type": "git", "url": "https://github.com/mindungil/GIL.git", "directory": "vscode" }`.

3. **`LICENSE` shipped.** `vscode/LICENSE` is a copy of the repo-root
   MIT LICENSE; vsce auto-renames it to `LICENSE.txt` inside the
   `.vsix`. (We chose copy over symlink because vsce does not always
   follow symlinks across packaging hosts.) The `license` field in
   `package.json` was also corrected from `Apache-2.0` to `MIT` to
   match the project root.

## What's intentionally not in this file

Bundle minification, sourcemap controls, watch mode, and dev install
hooks live in the README. This file documents only the
package-for-distribution recipe and the verified state of the
artifact.
