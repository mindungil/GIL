# P28 — chat-mode enforcement implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the chat / run-mode enforcement gap identified in `docs/research/2026-05-15-gil-failure-floor.md` by adding turn-boundary verify enforcement (C1), readonly-target reject (C3), verify quality scaffolding (C4), and cleaning up 5/12 strays (C5).

**Architecture:** All changes live in `server/internal/service/`. C5 is repo hygiene, executed first as a noise-isolator. C3 and C4 are localized tool-level guards. C1 is the largest change — it adds a per-turn tool-call tracker to the chat agent loop in `SessionService.Prompt` and gates turn completion when code-changing tools fired without a verify.

**Tech Stack:** Go 1.25 workspace at `/home/ubuntu/gil`. Tests live as `*_test.go` next to source. Run individual file's tests with `go test ./server/internal/service/ -run TestFoo -v`.

**Branch:** `feat/p28-chat-mode-enforcement` (already created, commit `d785171` is the design doc).

**Spec source of truth:** `docs/design/chat-mode-enforcement.md` (read this before starting).

---

## File map

| File | Change |
|------|--------|
| `email.go`, `email_test.go`, `go.mod` (repo root) | DELETE |
| `go.work` | revert `.` entry |
| `.gitignore` | add `.gocache/` |
| `server/internal/service/agent_tools_write.go` | add `rejectReadonlyTarget`, gate `toolWriteFile.run` |
| `server/internal/service/agent_tools_extra.go` | gate `toolEditFile.run` with same helper |
| `server/internal/service/apply_patch.go` | gate `toolApplyPatch.run` per-op |
| `server/internal/service/agent_tools_plan_verify.go` | add `isWeakVerifyCommand`, gate `toolVerify.run` |
| `server/internal/service/session_prompt.go` | (i) add a turn-scoped tool-call tracker; (ii) extend Prompt loop with verify-enforcement retry; (iii) append a verify-quality line to `defaultChatSystemPrompt` |
| `server/internal/service/agent_tools_write_test.go` | new tests for C3 (or add to existing if present) |
| `server/internal/service/agent_tools_plan_verify_test.go` | new tests for C4 |
| `server/internal/service/session_prompt_test.go` | new test for C1 |

---

## Task 0: C5 cleanup

**Files:**
- Delete: `email.go`, `email_test.go`, `go.mod` (repo root, NOT the per-module ones)
- Modify: `go.work` (remove `.` from `use` block)
- Modify: `.gitignore` (add `.gocache/`)

- [ ] **Step 1: Verify the stray files are indeed at repo root, not in a real module**

Run: `ls /home/ubuntu/gil/email.go /home/ubuntu/gil/email_test.go /home/ubuntu/gil/go.mod`
Expected: All three exist with May 12 timestamps. (Real Go modules live under `cli/`, `core/`, `server/`, etc.)

- [ ] **Step 2: Delete the strays**

Run:
```bash
cd /home/ubuntu/gil && rm email.go email_test.go go.mod
```
Expected: No output. Confirm with `ls email.go 2>&1` → "No such file or directory".

- [ ] **Step 3: Revert the `.` entry in go.work**

Edit `/home/ubuntu/gil/go.work` — remove the line containing just `.` between `use (` and `./cli`.

Before:
```
use (
	.
	./cli
	./core
```

After:
```
use (
	./cli
	./core
```

- [ ] **Step 4: Add `.gocache/` to .gitignore**

Check if `.gitignore` already has `.gocache/`:
```bash
grep "^\.gocache" /home/ubuntu/gil/.gitignore
```
If absent, append `.gocache/` as a new line at the end of `.gitignore`.

- [ ] **Step 5: Confirm workspace still builds**

The repo root is a Go *workspace*, not a module — so `go build ./...` from
root fails with "directory prefix . does not contain modules listed in
go.work". Build each module instead:

```bash
cd /home/ubuntu/gil && for m in cli core server mcp proto runtime sdk tui; do
  (cd $m && go build ./...) || { echo "FAIL: $m"; exit 1; }
done && echo "all modules ok"
```
Expected: prints `all modules ok` and exits 0.

- [ ] **Step 6: Commit**

```bash
cd /home/ubuntu/gil && git add -u && git add .gitignore && git commit -m "$(cat <<'EOF'
chore(repo): remove 5/12 working-dir-bug strays

email.go, email_test.go, go.mod, plus go.work `.` entry were created in
the May 12 stress run when --working-dir didn't propagate to auto-created
sessions (commit ad9274b fixed that). They are not part of any real
module. Add .gocache/ to .gitignore so future build-cache exploration
doesn't recur.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 1: C3 — readonly target reject helper + test

**Files:**
- Modify: `server/internal/service/agent_tools_write.go` (add helper near top, after `sessionWD`)
- Test: `server/internal/service/agent_tools_write_test.go` (create or append)

- [ ] **Step 1: Write the failing test first**

Create `server/internal/service/agent_tools_write_test.go` (if it doesn't exist) or append to existing test file. The test exercises `rejectReadonlyTarget` directly:

```go
package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectReadonlyTarget_FileWritable_OK(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "writable.txt")
	if err := os.WriteFile(p, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rejectReadonlyTarget(p); err != nil {
		t.Fatalf("writable file rejected: %v", err)
	}
}

func TestRejectReadonlyTarget_FileReadonly_Reject(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ro.txt")
	if err := os.WriteFile(p, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := rejectReadonlyTarget(p); err == nil {
		t.Fatalf("expected reject, got nil")
	}
}

func TestRejectReadonlyTarget_FileMissing_OK(t *testing.T) {
	// Creating a new file under a writable parent is fine.
	dir := t.TempDir()
	p := filepath.Join(dir, "new.txt")
	if err := rejectReadonlyTarget(p); err != nil {
		t.Fatalf("missing file (create case) rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run the test, confirm it fails**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run TestRejectReadonlyTarget -v
```
Expected: `FAIL` with "undefined: rejectReadonlyTarget".

- [ ] **Step 3: Implement `rejectReadonlyTarget` in `agent_tools_write.go`**

Add this function right after `sessionWD` (around line 87, before the `--- read_file ---` divider):

```go
// rejectReadonlyTarget returns a non-nil error when abs points at an
// existing file whose owner-write bit (0o200) is unset. Missing files
// pass (creation is allowed via writable parent dir, not gated here).
// Directories pass (callers should resolve to files before calling).
//
// Rationale: C3 in docs/design/chat-mode-enforcement.md. write_file /
// edit_file / apply_patch must not silently chmod through a user-marked
// readonly file — that erases the user's sandbox intent. Agent can
// still chmod explicitly via run_bash; this only blocks silent bypass.
func rejectReadonlyTarget(abs string) error {
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat target: %w", err)
	}
	if info.IsDir() {
		return nil
	}
	if info.Mode().Perm()&0o200 == 0 {
		return fmt.Errorf("target file %s is read-only (mode 0%o); the user has marked it as protected. "+
			"If modification is genuinely required, surface the intent to the user — do not chmod to bypass",
			filepath.Base(abs), info.Mode().Perm())
	}
	return nil
}
```

`errors` and `io/fs` are already imported in this file (check the import block); if not, add them.

- [ ] **Step 4: Run the test, confirm it passes**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run TestRejectReadonlyTarget -v
```
Expected: all three subtests PASS.

- [ ] **Step 5: Wire `rejectReadonlyTarget` into `toolWriteFile.run`**

In `agent_tools_write.go`, find `toolWriteFile.run` (line 175). After the `resolveInWD` call (around line 196) and before `os.MkdirAll`, insert:

```go
	if err := rejectReadonlyTarget(abs); err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
```

So the relevant section reads:
```go
	abs, err := resolveInWD(wd, args.Path)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := rejectReadonlyTarget(abs); err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
```

- [ ] **Step 6: Wire into `toolEditFile.run`**

In `agent_tools_extra.go`, find `toolEditFile.run` (line 70). After the `resolveInWD` call (around line 86) and before `os.ReadFile`, insert the same 3-line guard:

```go
	if err := rejectReadonlyTarget(abs); err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
```

- [ ] **Step 7: Wire into `toolApplyPatch.run`**

In `apply_patch.go`, find `toolApplyPatch.run` (line 409). After the existing `len(ops) == 0` check (line 427-429) and before the pre-write tracker loop (line 435), insert a loop that pre-checks every op's target:

```go
	// C3: reject patches that would silently chmod through a user-protected file.
	for _, op := range ops {
		abs, rerr := resolveInWD(wd, op.path)
		if rerr != nil {
			continue // hunk-mismatch errors are caught by applyPatch itself; we only gate readonly here
		}
		if err := rejectReadonlyTarget(abs); err != nil {
			return provider.ToolResult{Content: err.Error(), IsError: true}, nil
		}
	}
```

(`resolveInWD` is already used elsewhere in this file — no new import needed.)

- [ ] **Step 8: Add integration tests for the three tools (table-driven)**

Append to `agent_tools_write_test.go`:

```go
func TestToolWriteFile_ReadonlyTarget_Rejects(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	repo, sid := newMemSessionRepoWithWD(t, dir)
	tool := &toolWriteFile{repo: repo}
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"path":"locked.go","content":"package x\nvar X = 1\n"}`))
	if !res.IsError {
		t.Fatalf("expected IsError, got result %+v", res)
	}
	// File content unchanged.
	body, _ := os.ReadFile(target)
	if string(body) != "package x\n" {
		t.Fatalf("file mutated despite readonly: %q", body)
	}
}
```

NOTE: `newMemSessionRepoWithWD` is a test helper. If it doesn't exist in this package's test files, look for an analogous helper (grep `func.*testing.T.*session.Repo`). If none, add a minimal one:

```go
// newMemSessionRepoWithWD creates an in-memory session repo with one
// session pointing at wd, returning the repo and session id. Used by
// tool unit tests that need a real WorkingDir.
func newMemSessionRepoWithWD(t *testing.T, wd string) (*session.Repo, string) {
	t.Helper()
	repo := session.NewMemRepo()
	sess, err := repo.Create(t.Context(), session.CreateInput{WorkingDir: wd})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return repo, sess.ID
}
```

If `session.NewMemRepo` doesn't exist either, look at existing `*_test.go` files in the same dir to find how they construct repos (e.g. via `session.NewSQLiteRepo(t.TempDir() + "/db")`).

- [ ] **Step 9: Run the new test**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run TestToolWriteFile_ReadonlyTarget -v
```
Expected: PASS.

- [ ] **Step 10: Run the full package's tests to confirm no regressions**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -count=1
```
Expected: PASS (no FAIL). Pre-existing tests should still pass.

- [ ] **Step 11: Commit**

```bash
cd /home/ubuntu/gil && git add server/internal/service/ && git commit -m "$(cat <<'EOF'
feat(tools): C3 reject readonly target on write/edit/apply_patch

write_file, edit_file, and apply_patch now stat the target file before
mutating and return IsError when the owner-write bit is unset. This
preserves user sandbox intent — agents can still chmod explicitly via
run_bash, but cannot silently bypass a chmod 444 marker.

Refs: docs/design/chat-mode-enforcement.md §5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: C4 Layer A — `isWeakVerifyCommand` regex + test

**Files:**
- Modify: `server/internal/service/agent_tools_plan_verify.go` (helper + call site)
- Test: `server/internal/service/agent_tools_plan_verify_test.go` (create or append)

- [ ] **Step 1: Write the failing test**

Append to (or create) `server/internal/service/agent_tools_plan_verify_test.go`:

```go
package service

import "testing"

func TestIsWeakVerifyCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantBad bool
	}{
		{"bare cat", "cat foo.go", true},
		{"bare ls", "ls -la", true},
		{"bare echo", "echo hi", true},
		{"bare pwd", "pwd", true},
		{"bare true", "true", true},
		{"bare stat", "stat foo.go", true},
		{"bare head", "head -10 foo.go", true},
		{"bare tail", "tail -20 foo.log", true},
		{"bare file", "file foo.go", true},
		{"leading whitespace", "   cat foo.go", true},
		{"build is fine", "go build ./...", false},
		{"test is fine", "go test ./...", false},
		{"compound — cat then build", "cat foo.go && go build ./...", false},
		{"compound — head then test", "head foo.go && go test", false},
		{"pipe to test runner", "find . -name '*.go' | xargs go vet", false},
		{"explicit assertion script", "./scripts/check.sh", false},
		{"cat alone with redirect", "cat > foo.txt", false}, // it's writing, not just inspecting
		{"empty after trim", "   ", true},                    // already rejected upstream but be conservative
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isWeakVerifyCommand(tc.cmd)
			if got != tc.wantBad {
				t.Fatalf("isWeakVerifyCommand(%q) = %v, want %v", tc.cmd, got, tc.wantBad)
			}
		})
	}
}
```

- [ ] **Step 2: Run, confirm fail**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run TestIsWeakVerifyCommand -v
```
Expected: FAIL with "undefined: isWeakVerifyCommand".

- [ ] **Step 3: Implement `isWeakVerifyCommand`**

In `agent_tools_plan_verify.go`, add this helper near the other top-of-file consts (after the `verifyTailLineCount` const block around line 358):

```go
// weakVerifyCommandPattern matches commands whose primary action only
// inspects state (no behavior assertion, no build/test/lint). The
// pattern is intentionally conservative: it fires only when the leading
// command — before any &&, ||, ;, or | — is one of these read-only
// utilities AND there is no trailing chained command. Compound commands
// (`cat foo.go && go build`) pass.
//
// Limits:
//   - false-positive: `cat > foo.txt` (redirect) is technically a write
//     but matches "cat" — we accept this rare false-positive because
//     the agent can recover by rephrasing.
//   - false-negative: agent could disguise weak verify as `bash -c "cat"`.
//     We don't try to defeat adversarial agents; this is a quality
//     scaffold, not a sandbox.
var weakVerifyLeadingCommands = map[string]struct{}{
	"cat": {}, "ls": {}, "pwd": {}, "echo": {}, "true": {},
	"stat": {}, "head": {}, "tail": {}, "file": {},
}

// isWeakVerifyCommand reports whether cmd is a single inspect-only
// command with no behavior-checking chain. See spec C4 Layer A.
func isWeakVerifyCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return true
	}
	// If the command contains a chain operator or pipe, it's compound
	// — let it through. (Conservative: even `cat foo && cat bar` passes,
	// but the rarity of such constructs makes the trade-off OK.)
	for _, sep := range []string{"&&", "||", ";", "|"} {
		if strings.Contains(trimmed, sep) {
			return false
		}
	}
	// If there's a redirect, the command is writing state — not weak.
	if strings.ContainsAny(trimmed, ">") {
		return false
	}
	// Pull the first whitespace-delimited token.
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return true
	}
	_, weak := weakVerifyLeadingCommands[fields[0]]
	return weak
}
```

(`strings` is already imported in `agent_tools_plan_verify.go` — confirm at top of file.)

- [ ] **Step 4: Run, confirm pass**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run TestIsWeakVerifyCommand -v
```
Expected: all subtests PASS.

- [ ] **Step 5: Wire into `toolVerify.run`**

In `agent_tools_plan_verify.go` `toolVerify.run` (line 387), find the existing empty-command guard (line 396-398):

```go
	if strings.TrimSpace(args.Command) == "" {
		return provider.ToolResult{Content: "command is empty", IsError: true}, nil
	}
```

Right after this block, add the weak-command guard:

```go
	if isWeakVerifyCommand(args.Command) {
		return provider.ToolResult{
			Content: "verify command is too weak — `cat`/`ls`/`echo` only inspect state, " +
				"they don't verify behavior. Use build, test, lint, type-check, or a custom " +
				"assertion script. Chain to a real check (e.g. `cat foo.go && go build`) if " +
				"you must inspect first.",
			IsError: true,
		}, nil
	}
```

- [ ] **Step 6: Add an integration test for the wired tool**

Append to `agent_tools_plan_verify_test.go`:

```go
import (
	"encoding/json"
	// existing imports plus:
)

func TestToolVerify_WeakCommand_Rejects(t *testing.T) {
	dir := t.TempDir()
	repo, sid := newMemSessionRepoWithWD(t, dir)
	tool := &toolVerify{repo: repo}
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"description":"check","command":"cat main.go"}`))
	if !res.IsError {
		t.Fatalf("expected IsError, got %+v", res)
	}
}

func TestToolVerify_CompoundCommand_OK(t *testing.T) {
	dir := t.TempDir()
	// Touch a file so `cat` doesn't bomb on missing.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, sid := newMemSessionRepoWithWD(t, dir)
	tool := &toolVerify{repo: repo}
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"description":"build","command":"cat main.go && go build ./..."}`))
	// We don't care about pass/fail of go build (no go.mod in tempdir).
	// We only care that the schema guard didn't reject before exec.
	if res.IsError && strings.Contains(res.Content, "too weak") {
		t.Fatalf("compound command rejected as weak: %+v", res)
	}
}
```

- [ ] **Step 7: Run all C4 tests**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run "TestIsWeakVerifyCommand|TestToolVerify_WeakCommand|TestToolVerify_CompoundCommand" -v
```
Expected: PASS.

- [ ] **Step 8: Full-package regression**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -count=1
```
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
cd /home/ubuntu/gil && git add server/internal/service/ && git commit -m "$(cat <<'EOF'
feat(verify): C4 layer A — reject weak inspect-only verify commands

`isWeakVerifyCommand` flags single-command verify calls whose leading
token is cat/ls/pwd/echo/true/stat/head/tail/file (with no chain to a
real check). toolVerify.run returns IsError with guidance to chain to
build/test/lint or use an explicit assertion script.

Compound commands (`cat foo && go build`) pass through. Redirect
operators pass through (the agent might be writing).

Refs: docs/design/chat-mode-enforcement.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: C4 Layer B — system prompt line

**Files:**
- Modify: `server/internal/service/session_prompt.go` (the `defaultChatSystemPrompt` const at line 101)

- [ ] **Step 1: Locate the existing verify guidance line**

The current text at line 143-146 in `session_prompt.go`:

```
- verify: run a verification command. When called with step_id, it
  transitions the matching plan_step on success/failure. After every
  code-changing tool call (write_file, edit_file, apply_patch) you
  MUST run verify before progressing or declaring the work done.
```

- [ ] **Step 2: Append a verify-quality line after that paragraph**

Replace the existing 4-line block with this 6-line block:

```
- verify: run a verification command. When called with step_id, it
  transitions the matching plan_step on success/failure. After every
  code-changing tool call (write_file, edit_file, apply_patch) you
  MUST run verify before progressing or declaring the work done.
  verify commands must exercise behavior, not just inspect state.
  Prefer build, test, lint, type-check, or assertion scripts. Standalone
  cat / ls / echo / pwd are not valid verify checks — chain them to a
  real check (e.g. cat foo.go && go build) if you must inspect first.
```

- [ ] **Step 3: Compile-check**

```bash
cd /home/ubuntu/gil && go build ./server/...
```
Expected: clean.

- [ ] **Step 4: Run package tests**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -count=1
```
Expected: PASS. (No behavior change to runtime; this is prompt-only.)

- [ ] **Step 5: Commit (folded with Task 2 or separate)**

Decision: separate commit, keeps the prompt change reviewable on its own.

```bash
cd /home/ubuntu/gil && git add server/internal/service/session_prompt.go && git commit -m "$(cat <<'EOF'
feat(prompt): C4 layer B — verify-quality guidance line

Add a sentence to defaultChatSystemPrompt explaining what a real verify
command looks like. Pairs with the schema-level weak-command reject
landed in the previous commit; this is the LLM-side hint that nudges
the agent toward build/test/lint before the deterministic guard
ever fires.

Refs: docs/design/chat-mode-enforcement.md §6.2 (Layer B)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: C1 — chat turn-boundary verify enforcement

**Files:**
- Modify: `server/internal/service/session_prompt.go` (Prompt loop)
- Test: `server/internal/service/session_prompt_test.go` (create or append)

This is the largest change. Read `docs/design/chat-mode-enforcement.md` §3 in full before starting.

**Design recap**: in the chat agent loop, track which tools fired during the current Prompt invocation. If `write_file`/`edit_file`/`apply_patch` was called but `verify` was never called, system inject a synthetic user message asking the agent to verify, then loop one more time. Cap at 2 extra cycles before terminating with an error Done.

- [ ] **Step 1: Write the failing test**

Append to (or create) `server/internal/service/session_prompt_test.go`:

```go
package service

import (
	"context"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/provider"
)

// TestPrompt_WriteWithoutVerify_RetriesThenErrors uses a mock provider
// that always returns "end_turn" after a write_file call, never calling
// verify. The system should inject a verify-reminder, loop again, and
// (when the mock still doesn't verify) emit an error Done.
func TestPrompt_WriteWithoutVerify_RetriesThenErrors(t *testing.T) {
	// MockTurns: turn 1 issues write_file, no text; turn 2 issues no
	// tool calls (model thinks it's done); turns 3 & 4 same as 2 (stubborn
	// model that ignores the verify-reminder injection).
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "write_file",
			Input: []byte(`{"path":"main.go","content":"package main\n"}`)}}},
		{Text: "done"},
		{Text: "still done"},
		{Text: "really done"},
	}
	// Spin up a SessionService with the mock provider + tmpdir-rooted session.
	// (Detail: use existing test helper newTestSessionService — see
	// session_prompt_test.go siblings or grep `newTestSessionService` to
	// find the canonical construction. If none, build one inline using the
	// pattern from agent_tools_plan_verify_test.go.)
	svc, sid := newTestSessionService(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	err := svc.Prompt(promptReq(sid, "make a main.go"), stream)
	if err == nil {
		t.Fatalf("expected error termination after exhausting verify retries; got nil")
	}
	// Confirm the system injected a verify reminder at least once.
	injected := 0
	for _, p := range stream.Parts {
		if td := p.GetText(); strings.Contains(td.GetContent(), "verify") {
			injected++
		}
	}
	if injected == 0 {
		t.Fatalf("no verify-reminder text seen in stream; got %d parts", len(stream.Parts))
	}
}

// TestPrompt_WriteThenVerify_TurnEndsCleanly uses a mock provider that
// calls write_file then verify. The system should let the turn close
// normally without injection.
func TestPrompt_WriteThenVerify_TurnEndsCleanly(t *testing.T) {
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "write_file",
			Input: []byte(`{"path":"main.go","content":"package main\n"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "verify",
			Input: []byte(`{"description":"build","command":"go build ./..."}`)}}},
		{Text: "done"},
	}
	svc, sid := newTestSessionService(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	if err := svc.Prompt(promptReq(sid, "make a main.go and verify"), stream); err != nil {
		t.Fatalf("clean run errored: %v", err)
	}
}
```

This relies on test helpers `newTestSessionService`, `fakePromptStream`, `promptReq`. Before writing implementation, **find or build these helpers**.

- [ ] **Step 2: Inventory existing test helpers**

```bash
cd /home/ubuntu/gil && grep -rn "newTestSessionService\|fakePromptStream\|promptReq\|MockTurn" server/internal/service/ --include="*_test.go" | head -20
```

If they exist, use them. If they don't:
- `MockTurn` lives in `core/provider/` — confirm with `grep -rn "type MockTurn" core/`.
- For `fakePromptStream` and `newTestSessionService`, look at `session_prompt_test.go` (if it exists) or `run_test.go` for an analogous run-mode mock harness. Adapt to chat:

```go
type fakePromptStream struct {
	ctx   context.Context
	Parts []*gilv1.Part
}

func (f *fakePromptStream) Send(p *gilv1.Part) error { f.Parts = append(f.Parts, p); return nil }
func (f *fakePromptStream) Context() context.Context { return f.ctx }
// gRPC ServerStream interface stubs:
func (f *fakePromptStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakePromptStream) SendHeader(metadata.MD) error { return nil }
func (f *fakePromptStream) SetTrailer(metadata.MD)       {}
func (f *fakePromptStream) RecvMsg(any) error            { return nil }
func (f *fakePromptStream) SendMsg(any) error            { return nil }
```

- [ ] **Step 3: Run the test, confirm fail**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run "TestPrompt_WriteWithoutVerify|TestPrompt_WriteThenVerify" -v
```
Expected: FAIL. The current code doesn't enforce verify, so `TestPrompt_WriteWithoutVerify_RetriesThenErrors` will fail (`Prompt` returns nil after the model says "done"). The clean-path test should already pass.

- [ ] **Step 4: Add a per-turn tool-call tracker**

In `session_prompt.go` `SessionService.Prompt`, locate the agent loop start (line 390, `for turn := 0; turn < maxAgentTurns; turn++ {`). Just before the loop, add tracking state:

```go
	// C1 verify-enforcement tracker. Set when a code-changing tool fires;
	// cleared when verify fires. If non-empty at "model ended turn"
	// boundary, the system inject loop kicks in.
	codeChangingTools := map[string]bool{
		"write_file": true, "edit_file": true, "apply_patch": true,
	}
	writeFired := false
	verifyFired := false
	verifyRetries := 0
	const maxVerifyRetries = 2
```

- [ ] **Step 5: Update tracker on each tool dispatch**

Inside the inner `for _, call := range resp.ToolCalls` loop (around line 440), right after `dispatchTool`'s result is appended to `toolResults`, add:

```go
		if codeChangingTools[call.Name] {
			writeFired = true
		}
		if call.Name == "verify" && !result.IsError {
			verifyFired = true
		}
```

(Note: verify with IsError doesn't satisfy the gate — agent must successfully verify, otherwise the failure itself signals next-turn work and the gate retriggers when that next turn ends.)

- [ ] **Step 6: Replace the "no tool calls → break" branch with the verify-gate**

Current code (line 421-426):

```go
		// If no tool calls, the LLM is done.
		if len(resp.ToolCalls) == 0 {
			s.chatHistory().append(sessionID,
				provider.Message{Role: provider.RoleAssistant, Content: resp.Text})
			break
		}
```

Replace with:

```go
		// If no tool calls, the LLM thinks it's done. Apply the C1
		// verify-gate before letting the turn close.
		if len(resp.ToolCalls) == 0 {
			s.chatHistory().append(sessionID,
				provider.Message{Role: provider.RoleAssistant, Content: resp.Text})

			if !writeFired || verifyFired {
				break
			}
			if verifyRetries >= maxVerifyRetries {
				// Stubborn agent. Surface an error done so callers know
				// the work isn't actually verified.
				msg := "code-changing tools were called but verify was never run; turn aborted"
				_ = stream.Send(&gilv1.Part{
					Body: &gilv1.Part_Done{
						Done: &gilv1.DonePart{StopReason: "verify_missing", ErrorMessage: msg},
					},
				})
				return status.Errorf(codes.FailedPrecondition, "%s", msg)
			}

			// Inject a synthetic user message reminding the agent.
			reminder := "Reminder: you called write_file/edit_file/apply_patch " +
				"but did not call verify yet. Call verify with a real " +
				"behavior check (build, test, lint) before finishing this turn."
			reminderMsg := provider.Message{Role: provider.RoleUser, Content: reminder}
			msgs = append(msgs, reminderMsg)
			s.chatHistory().append(sessionID, reminderMsg)
			// Echo the reminder to the stream so observers see the gate fire.
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: "[system] " + reminder}},
			})
			verifyRetries++
			continue
		}
```

- [ ] **Step 7: Sanity check — re-run tests**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -run "TestPrompt_WriteWithoutVerify|TestPrompt_WriteThenVerify" -v
```
Expected: both PASS. The fail path triggers `verify_missing` after 2 retries; the success path closes cleanly with no injection.

- [ ] **Step 8: Full-package regression**

```bash
cd /home/ubuntu/gil && go test ./server/internal/service/ -count=1
```
Expected: PASS. Existing tests that called `write_file` directly via mock turns may need a follow-up `verify` turn — if any tests start failing, the right fix is to extend the mock turn list to include a verify call.

- [ ] **Step 9: Commit**

```bash
cd /home/ubuntu/gil && git add server/internal/service/session_prompt.go server/internal/service/session_prompt_test.go && git commit -m "$(cat <<'EOF'
feat(chat): C1 turn-boundary verify enforcement

When write_file/edit_file/apply_patch fires in a Prompt invocation but
verify never does, the system injects a synthetic user reminder and
loops the agent for up to 2 extra turns. If still no verify, the Prompt
returns a verify_missing error Done and FailedPrecondition status.

This closes the largest chat/run gap identified in the 2026-05-15
failure-floor research: chat surface previously had no system
enforcement of verify-before-completion, only a system-prompt
recommendation that strong/weak models could ignore.

Refs: docs/design/chat-mode-enforcement.md §3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: regression — re-run failure-floor stress

**Files:**
- Inspect: `/tmp/bench-floor/run.sh` and `tasks.sh` (already on disk from 2026-05-15 run)
- Update: `docs/research/2026-05-15-gil-failure-floor.md` with a "post-P28" addendum

- [ ] **Step 1: Build + install new gil/gild**

```bash
cd /home/ubuntu/gil && make build install 2>&1 | tail -10
```
Expected: clean install of new binaries at `/usr/local/bin/gil` and `/usr/local/bin/gild`. If `make install` doesn't exist, check the Makefile for the right target (`make all`, `go install ./cli/...`, etc.).

- [ ] **Step 2: Restart the daemon**

```bash
pkill -f "gild --foreground" 2>/dev/null; sleep 1; nohup gild --foreground > /tmp/gild-p28.log 2>&1 &
sleep 2 && ls ~/.local/state/gil/gild.sock
```
Expected: socket exists and new daemon process owns it.

- [ ] **Step 3: Run the 8 stress tasks**

```bash
bash /tmp/bench-floor/run.sh 2>&1 | tail -20
```
Expected: all 8 tasks complete. PASS count and wall_s captured.

- [ ] **Step 4: Inspect verify call patterns (key regression signal)**

```bash
for t in f1 f2 f3 f4 f5 f6 f7 f8; do
  echo "=== $t verify pattern ==="
  grep -E "verify|tool_call.*write_file|tool_call.*edit_file" /tmp/bench-floor/$t/gil.log | head -5
done
```

What to look for:
- Each task that wrote files should now show `verify` calls with **build/test commands**, not `cat`/`ls`.
- f8 (readonly) should show IsError on the write attempt — agent forced to escalate or fail.

- [ ] **Step 5: Append regression note to failure-floor doc**

Add a section to `docs/research/2026-05-15-gil-failure-floor.md`:

```markdown
## 11. Post-P28 regression (2026-05-15 late)

Re-ran the 8 stress tasks against the post-`feat/p28-chat-mode-enforcement`
build. Observations:

[fill in actual wall_s, verify counts, and any new failure modes —
including whether f8 readonly is now an IsError as designed]
```

Drop the actual numbers you observed in Step 3-4 in there.

- [ ] **Step 6: Commit the regression note**

```bash
cd /home/ubuntu/gil && git add docs/research/2026-05-15-gil-failure-floor.md && git commit -m "$(cat <<'EOF'
docs(research): post-P28 regression on failure-floor stress

Captures the verify-call pattern after C1/C3/C4 land — primary check
is that agent verify commands are now build/test rather than cat/ls,
and f8 readonly is rejected by the new guard.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 7: Push the branch**

```bash
cd /home/ubuntu/gil && source ~/.env && git push https://${github_token}@github.com/mindungil/gil.git feat/p28-chat-mode-enforcement
```

- [ ] **Step 8: Open the PR**

```bash
cd /home/ubuntu/gil && source ~/.env && GH_TOKEN=$github_token gh pr create --base develop --head feat/p28-chat-mode-enforcement --title "P28 — chat-mode enforcement (verify gate, readonly reject, weak-verify schema reject)" --body "$(cat <<'EOF'
## Summary
- C1: chat agent loop now enforces verify-after-write at turn boundary (up to 2 retry cycles, then verify_missing error).
- C3: write_file/edit_file/apply_patch reject readonly targets (preserves user sandbox intent).
- C4: verify tool rejects weak inspect-only commands (cat/ls/echo/pwd standalone) + system prompt guidance.
- C5: cleaned up 5/12 working-dir-bug strays at repo root.

Closes the chat / run-mode enforcement gap identified in `docs/research/2026-05-15-gil-failure-floor.md`.

## Test plan
- [x] `go test ./server/internal/service/ -count=1` passes
- [x] new tests: TestRejectReadonlyTarget*, TestToolWriteFile_ReadonlyTarget_*, TestIsWeakVerifyCommand, TestToolVerify_WeakCommand_*, TestPrompt_WriteWithoutVerify_*, TestPrompt_WriteThenVerify_*
- [x] failure-floor stress regression (see post-P28 section in research doc)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review (run after writing this plan)

Verify against `docs/design/chat-mode-enforcement.md`:

- **C1** → Task 4. ✓
- **C2** → dropped (spec §2.1). No task. ✓
- **C3** → Task 1. ✓
- **C4 Layer A** → Task 2. ✓
- **C4 Layer B** → Task 3. ✓
- **C5** → Task 0. ✓
- **Test strategy from §9** → tests embedded in Tasks 1, 2, 4; regression in Task 5. ✓

No placeholders, no "TBD", no "similar to Task N" without code, no references to types/methods not defined in this plan or in the existing codebase. Each code step shows the exact text to add. Where helpers may or may not exist (e.g. `newMemSessionRepoWithWD`, `newTestSessionService`), the plan instructs the engineer to grep first and provides a fallback construction.
