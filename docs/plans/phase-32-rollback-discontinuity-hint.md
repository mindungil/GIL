# Phase 32 — workspace rollback discontinuity hint

> Severity 2 line item from `roadmap-post-v0.2.0.md`:
> "Workspace rollback safety net. `restore_checkpoint` refuses while a
> run is active; OK. But after a restore there's no automatic
> 'refreeze + restart' hint — the agent is mid-conversation when the
> files under it changed. Either surface the discontinuity to the LLM
> via a system note or document the manual recovery (reset_session →
> re-freeze)."

## Goal

After a successful `restore_checkpoint`, the agent's next turn must
treat any prior assumption about file contents as stale and re-read the
affected files. The tool result itself is the only chokepoint we can
rely on — there is no per-turn "system note" channel that fires after a
tool call returns. So the hint lands inside the tool result content.

## Non-goals

- Automatic re-freeze. The user owns that decision; we just signal that
  it may be needed.
- Touching `Reset` (the stuck-recovery rollback). That path is internal,
  not agent-driven, and the agent loop already restarts from a clean
  state after Reset.
- Smart "what changed in the conversation" reasoning. We surface the
  raw changed-file list; the agent decides what's stale.

## Design

Three changes, one branch:

1. **proto** — extend `RestoreResponse` with `repeated string
   changed_files` + `bool changed_files_truncated`. Capped at 50
   entries server-side; truncation flagged separately.

2. **core/checkpoint** — two new read-only `ShadowGit` methods:
   - `HeadSHA(ctx)` — current HEAD, returns "" on fresh init.
   - `FilesDiffFromWorktree(ctx, target)` — `git diff --name-only
     <target>` against the live workspace. Returns the files that
     *would* change if we checked target back in. This is what the
     agent sees on disk, vs commit-vs-commit which can lie when a
     previous `git checkout <sha> -- .` updated the workspace without
     moving HEAD.

3. **server/internal/service** — `RunService.Restore` captures the
   workspace-vs-target diff before calling `ShadowGit.Restore`, then
   populates the new RestoreResponse fields. `toolRestoreCheckpoint`
   delegates rendering to a new `renderRestoreResult` helper that:
   - leads with the existing `restored to <sha> · "<msg>" (N total)`
     header
   - appends a loud **WORKSPACE STATE CHANGED** warning + the changed-
     file list when the diff is non-empty
   - emits `workspace tracked content unchanged (target == prior
     HEAD).` when the diff is empty (no false alarm on a no-op restore)
   - shows `… (more changed files omitted; cap is 50)` when the list
     was truncated server-side

## Why `FilesDiffFromWorktree` instead of HEAD-vs-target

`ShadowGit.Restore` uses `git checkout <sha> -- .`, which writes the
target snapshot into the working tree but leaves HEAD pointed at
wherever it was before. So:

- After `Restore(sha1)` from a HEAD-at-sha2 state: HEAD=sha2, workdir
  matches sha1.
- A second `Restore(sha2)` would look like "HEAD already at sha2, no
  change" via HEAD-vs-target, but the workdir actually flips sha1 →
  sha2.

Comparing against the working tree directly avoids that false negative.

## Tests

- `core/checkpoint`:
  - `TestShadowGit_HeadSHA` — fresh-init returns "", post-commit
    returns commit SHA.
  - `TestShadowGit_ChangedFiles` — commit-vs-commit diff (kept as a
    general-purpose helper even though Restore now uses the workspace
    variant). Same-SHA and empty-from cases return empty without error.
- `server/internal/service`:
  - `TestRunService_Restore_RollsBackWorkspace` extended — asserts
    `resp.ChangedFiles` contains `file.txt` after the rollback, then
    contains `file.txt` again after the reverse, then is empty for the
    second-in-a-row no-op restore.
  - `TestRenderRestoreResult_WithChangedFiles` — warning copy and file
    list rendered.
  - `TestRenderRestoreResult_TruncatedList` — truncation note appears.
  - `TestRenderRestoreResult_NoOpRestore` — no warning, "tracked
    content unchanged" message.

## Out of scope (followup candidates)

- Reasoning-trace surface for "here is what your prior plan thought
  files contained vs what they now contain" — would need a plan-store
  snapshot to compare against.
- Auto-reset_session prompt when the diff is "obviously catastrophic"
  (e.g., >half the workingset changed). The current cap-and-truncate
  behavior is the conservative default; the user can ask.
- Renderer-side narration of the discontinuity in chat. Currently the
  tool result content surfaces in the standard `⚒ ✓ ...` line and is
  truncated to 200 chars by the chat-result formatter. Lifting the cap
  for restore_checkpoint specifically would let users see the full
  changed-file list in the transcript without asking. Skip for V1.
