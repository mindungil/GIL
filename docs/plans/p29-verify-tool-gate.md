# P29 — Verify-tool-gate (close §11.4 loophole) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tighten `isWeakVerifyCommand` so that `verify` calls whose
shell command is a *write* (heredoc or top-level redirect to file) are
rejected as weak — closing the silent-write-as-verify loophole that
`docs/research/2026-05-15-gil-failure-floor.md` §11.4 surfaced.

**Architecture:** Single-function tightening in
`server/internal/service/agent_tools_plan_verify.go`. Two new rules
enter `isWeakVerifyCommand` *before* the existing leading-command
check: heredoc detection (`<<` or `<<-`) and top-level redirect
detection (`>` or `>>` with non-`/dev/null` target). One existing
test case (`"cat alone with redirect"`) inverts from `wantBad: false`
to `wantBad: true` because its premise was the bug. Spec
amendment lands in `docs/design/chat-mode-enforcement.md` §6.

**Tech Stack:** Go (server module), `testify/require`.

---

## §0. Spec — what changes and why

### 0.1 The loophole

`agent_tools_plan_verify.go:397` short-circuits `isWeakVerifyCommand`
to `false` whenever `>` appears in the command. The original comment
defended this as "redirects mean the command is writing state — not
weak". That framing is wrong: the *agent* calls `verify` to satisfy
the C1 turn-boundary gate, and the verify tool does not distinguish
"command runs a real check" from "command writes a file and exits 0".
So a write-shaped command (`cat <<EOF > test.go`) reaches the verify
tool, exec succeeds, `IsError=false`, `needsVerify` resets — and the
agent has now "verified" without ever running build/test/lint on the
artifact. f1 in the failure-floor §11.1 trace shows this pattern: the
agent's first verify call was `cat <<EOF > /tmp/clean_test.go`.

### 0.2 The fix — three rules in `isWeakVerifyCommand`

Insert **before** the existing leading-command check, **after** the
chain/pipe check (chains still pass, since `cat foo && go build` is
legit):

1. If trimmed command contains `<<` (heredoc), return `true` (weak).
2. If trimmed command contains a top-level redirect token (`>` or
   `>>`) where the redirect target is not `/dev/null`, `/dev/stderr`,
   or `/dev/stdout`, return `true` (weak).
3. Existing leading-command lookup against `weakVerifyLeadingCommands`
   stays.

The `>` rule is intentionally redirect-aware (not a substring check)
so legitimate `2>&1` patterns and stderr suppression don't trip. The
`/dev/null` carve-out preserves common patterns like
`go build ./... > /dev/null` (silenced build is still a build).

### 0.3 Why not also block `tee`, named pipes, etc.?

Same reason as §C4 Layer A's original scope: this is a quality
scaffold, not a sandbox. We catch the patterns we observed in real
traces (heredoc, redirect-to-file). Adversarial agents can always
disguise — that's not the threat model.

### 0.4 Spec amendment in `chat-mode-enforcement.md`

Append a §6 "P29 amendment — write-shaped verify" that:
- Documents the loophole and trace (link to failure-floor §11.1).
- States the three rules.
- Notes the `/dev/null` carve-out.
- References this plan file.

---

## §1. File structure

**Modify:**
- `server/internal/service/agent_tools_plan_verify.go:385-406` — `isWeakVerifyCommand` body.
- `server/internal/service/agent_tools_plan_verify_test.go:143-175` — expand `TestIsWeakVerifyCommand`, invert one case.
- `docs/design/chat-mode-enforcement.md` — append §6.

**Create:** none.

**No test infra needed** — existing table-driven test is the right home.

---

## §2. Tasks

### Task 1: Expand the test table (TDD — failing first)

**Files:**
- Modify: `server/internal/service/agent_tools_plan_verify_test.go:143-175`

- [ ] **Step 1: Invert the `cat alone with redirect` case and add new cases**

Replace lines 165 (the existing `cat alone with redirect` row) and add the new write-shaped-verify rows. The full updated table block (replace the current cases array entirely):

```go
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
    // P29: write-shaped verify is now weak (bug fix — was false).
    {"cat redirect to file", "cat > foo.txt", true},
    {"echo redirect to file", "echo hi > foo.txt", true},
    {"heredoc cat to file", "cat <<EOF > foo.go\npackage x\nEOF", true},
    {"heredoc indented", "cat <<-EOF > foo.go\npackage x\nEOF", true},
    {"append redirect to file", "echo hi >> foo.txt", true},
    // /dev/null carve-out: silenced output is fine if leading cmd is real.
    {"build silenced to devnull", "go build ./... > /dev/null", false},
    {"test stderr to devnull", "go test ./... 2>/dev/null", false},
    {"stderr merge to stdout", "go vet ./... 2>&1", false},
    // Compound writes still pass (chain short-circuits before redirect).
    {"write then check", "cat <<EOF > t.go\npackage x\nEOF\n && go build", false},
    {"empty after trim", "   ", true},
}
```

- [ ] **Step 2: Run the test to verify failures**

Run: `cd server && go test ./internal/service/ -run TestIsWeakVerifyCommand -v`
Expected: FAIL on at least 5 cases — "cat redirect to file", "echo redirect to file", "heredoc cat to file", "heredoc indented", "append redirect to file" (these all currently return `false`, test now wants `true`).

- [ ] **Step 3: Commit the failing test**

```bash
git add server/internal/service/agent_tools_plan_verify_test.go
git commit -m "test(verify): P29 — failing cases for write-shaped verify"
```

### Task 2: Tighten `isWeakVerifyCommand`

**Files:**
- Modify: `server/internal/service/agent_tools_plan_verify.go:385-406`

- [ ] **Step 1: Replace the body of `isWeakVerifyCommand` and update the docstring**

Replace the function block (current lines 372-406) entirely with:

```go
// isWeakVerifyCommand reports whether cmd is unsuitable for verifying
// behavior. Three checks, ordered cheap-first:
//
//  1. Compound (chain or pipe) → not weak; agent threaded a real check.
//  2. Heredoc (`<<` / `<<-`) → weak; the command is writing content,
//     not checking it. Closes failure-floor §11.4 loophole.
//  3. Top-level redirect (`>` or `>>`) to a non-`/dev/{null,stderr,stdout}`
//     target → weak; the command is writing a file, not checking.
//     Carve-out preserves `go build ./... > /dev/null` style usage.
//  4. Leading command in weakVerifyLeadingCommands → weak.
//
// Conservative by design: false-negatives possible (e.g. `bash -c`
// disguise) — this is a quality scaffold, not a sandbox.
func isWeakVerifyCommand(cmd string) bool {
    trimmed := strings.TrimSpace(cmd)
    if trimmed == "" {
        return true
    }
    // Compound commands (chain or pipe) pass.
    for _, sep := range []string{"&&", "||", ";", "|"} {
        if strings.Contains(trimmed, sep) {
            return false
        }
    }
    // Heredoc → write-shaped, weak.
    if strings.Contains(trimmed, "<<") {
        return true
    }
    // Top-level redirect to file → write-shaped, weak.
    // Carve out /dev/null, /dev/stderr, /dev/stdout (legit silencing).
    if redirectsToFile(trimmed) {
        return true
    }
    fields := strings.Fields(trimmed)
    if len(fields) == 0 {
        return true
    }
    _, weak := weakVerifyLeadingCommands[fields[0]]
    return weak
}

// redirectsToFile reports whether trimmed contains an output redirect
// (`>` or `>>`) whose target is a regular file path (not /dev/null,
// /dev/stderr, /dev/stdout, and not a stderr-merge like `2>&1`).
//
// Cheap parse: split on whitespace, walk tokens, when a token is `>`
// or `>>` (or ends with one, e.g. `2>`), inspect the next token. The
// `&` prefix on the target (`>&1`) is the merge form — not a file.
func redirectsToFile(trimmed string) bool {
    devTargets := map[string]struct{}{
        "/dev/null":   {},
        "/dev/stderr": {},
        "/dev/stdout": {},
    }
    fields := strings.Fields(trimmed)
    for i, tok := range fields {
        // Strip a leading FD digit: `2>` → `>`, `1>>` → `>>`.
        op := tok
        if len(op) > 1 && (op[0] >= '0' && op[0] <= '9') {
            op = op[1:]
        }
        // Operators where the next token is the target.
        if op == ">" || op == ">>" {
            if i+1 >= len(fields) {
                return false
            }
            target := fields[i+1]
            if strings.HasPrefix(target, "&") {
                // Merge form: `>&1`, `>&2` — not a file.
                continue
            }
            if _, ok := devTargets[target]; ok {
                continue
            }
            return true
        }
        // Glued form: `>file` or `>>file` (uncommon but valid).
        if strings.HasPrefix(op, ">>") && len(op) > 2 {
            target := op[2:]
            if strings.HasPrefix(target, "&") {
                continue
            }
            if _, ok := devTargets[target]; ok {
                continue
            }
            return true
        }
        if strings.HasPrefix(op, ">") && len(op) > 1 {
            target := op[1:]
            if strings.HasPrefix(target, "&") {
                continue
            }
            if _, ok := devTargets[target]; ok {
                continue
            }
            return true
        }
    }
    return false
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `cd server && go test ./internal/service/ -run TestIsWeakVerifyCommand -v`
Expected: PASS — all 27 cases.

- [ ] **Step 3: Run full service package tests for regression**

Run: `cd server && go test ./internal/service/...`
Expected: PASS — total ~210 tests (P28 added 3, P29 adds 7).

- [ ] **Step 4: Commit**

```bash
git add server/internal/service/agent_tools_plan_verify.go
git commit -m "fix(verify): P29 — reject write-shaped verify (heredoc, redirect-to-file)"
```

### Task 3: Spec amendment

**Files:**
- Modify: `docs/design/chat-mode-enforcement.md` (append §6)

- [ ] **Step 1: Read the current end of the file**

Use Read to determine the last section number and append after it.

- [ ] **Step 2: Append §6**

Append (with the exact §6 number that follows the last existing section):

```markdown
---

## 6. P29 amendment — write-shaped verify rejected as weak

### 6.1 Background

[failure-floor §11.4](../research/2026-05-15-gil-failure-floor.md#114-wrap-up)
flagged a residual loophole: f1's verify call sequence began with
`cat <<EOF > /tmp/clean_test.go` — a write disguised as a verify.
The original `isWeakVerifyCommand` short-circuited on `>`, treating
"contains a redirect" as evidence of being a real check. That premise
was wrong: the verify tool exec-runs whatever the agent passes, so a
write-shaped command exits 0, `IsError=false`, `needsVerify` resets,
and the agent has "verified" without ever building or testing.

### 6.2 Rules (added in `isWeakVerifyCommand`)

After the existing chain/pipe short-circuit:

1. **Heredoc** (`<<` or `<<-` anywhere in the command) → weak. Agent
   is writing content, not checking it.
2. **Top-level redirect** (`>` or `>>`) where the target is not
   `/dev/null`, `/dev/stderr`, or `/dev/stdout`, and not the
   stderr-merge form `>&N` → weak.
3. (existing) Leading command in `weakVerifyLeadingCommands` → weak.

Compound commands still pass (chain check fires before write check),
so `cat <<EOF > t.go` followed by ` && go build` is allowed.

### 6.3 Carve-outs

`go build ./... > /dev/null`, `... 2>/dev/null`, `... 2>&1` all pass —
they're silencing output, not writing artifact files. Glued forms
(`>file`, `2>>log.txt`) are also rejected when the target is a
regular file.

### 6.4 Implementation

`server/internal/service/agent_tools_plan_verify.go` — new helper
`redirectsToFile` is a cheap shell-token walker (no real parser; we
don't need to defeat adversarial obfuscation). See
[`docs/plans/p29-verify-tool-gate.md`](../plans/p29-verify-tool-gate.md)
for the full task plan.
```

- [ ] **Step 3: Commit the amendment**

```bash
git add docs/design/chat-mode-enforcement.md
git commit -m "docs(design): P29 amendment — write-shaped verify"
```

### Task 4: Empirical sanity check on the existing failure-floor f1 trace

**Files:** none modified.

- [ ] **Step 1: Confirm the historic f1 verify pattern would now be rejected**

Construct a tiny Go test (do NOT commit) that calls
`isWeakVerifyCommand("cat <<EOF > /tmp/clean_test.go\npackage x\nEOF")`
and confirms it returns `true`. This is already covered by the
"heredoc cat to file" case in Task 1 — just visually re-confirm by
running:

Run: `cd server && go test ./internal/service/ -run TestIsWeakVerifyCommand/heredoc_cat_to_file -v`
Expected: PASS.

- [ ] **Step 2: No commit** — this task is verification-only. Skip if Task 2 step 3 was green.

---

## §3. Self-review (per writing-plans skill)

**Spec coverage:** Three rules in §0.2, all three are implemented in
Task 2 step 1 and exercised by Task 1's test cases. Spec amendment in
Task 3. f1 trace explicitly retested in Task 4.

**Placeholders:** None — every step has the actual code or command.

**Type consistency:** `isWeakVerifyCommand` keeps signature
`(cmd string) bool`; new helper `redirectsToFile` shares it. No call
sites need updating (line 447 caller is unchanged).
