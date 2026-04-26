# Phase 5 — Sandbox + Shadow Git + Stuck Recovery + Async Run + Event Integration + Per-Stage Models

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` for tracking.

**Goal:** Phase 4의 naive run engine을 production-grade로 끌어올린다. 6가지 capability 추가:

1. **Event session 통합** + secret masking (foundational — 다른 모든 task가 이 위에 올라감)
2. **Async run + Tail** (사용자가 며칠 무인 실행을 백그라운드로 돌리고 attach)
3. **Stuck detection** (OpenHands 5패턴) + **자가 회복** (5단계: alt_tool_order/model_escalate/subagent_branch/reset_section/adversary_consult)
4. **OS sandbox** (Linux: bwrap+seccomp; macOS는 v6)
5. **Shadow git checkpoint** per step (Cline 패턴 — 사용자 .git 비오염, restore 가능)
6. **Per-stage model separation** (main/weak/editor/adversary 분리)

**Architecture:**
- `core/event` 확장 — per-session Persister 래핑, `core/event/secret.go` (마스킹)
- `core/runner.AgentLoop` 확장 — 모든 turn을 EventStream으로 emit (provider call, tool call, tool result, verify, stage)
- `server/RunService.Start --detach` — goroutine 백그라운드 실행 + DB status update
- `server/RunService.Tail` — EventStream subscriber + 이벤트 스트림 (실제 구현)
- `core/stuck.Detector` (5 패턴) + `core/stuck.Recovery` (5 전략)
- `runtime/local/bwrap.go` — Linux bubblewrap 래퍼 (Codex 패턴 차용)
- `core/checkpoint.ShadowGit` — go-git 또는 exec git, `~/.gil/shadow/{cwd-hash}/.git`
- `core/runner` 확장 — 매 step 후 ShadowGit.Commit
- `core/runner` 확장 — Provider factory가 spec.models.{main/weak/editor/adversary} 별 분리
- `cli/run --detach`, `cli/restore <session-id> <step-id>`, `cli/status` 강화

**범위 한정:**
- macOS sandbox (Seatbelt) — Phase 6
- TUI (Bubbletea) — Phase 6+
- 메모리 뱅크 (6 markdown files) — Phase 6
- 컨텍스트 압축 (캐시 보존) — Phase 6
- Microagents — Phase 7

---

## Task 1: core/event session 통합 + secret masking

**Files:**
- Modify: `core/event/persist.go` — secret masking on serialize
- Create: `core/event/secret.go` + tests
- Modify: `core/session/repo.go` — add EventDir(sessionID) helper
- Create: `core/event/session_persist_test.go`

- [ ] **Step 1: secret masking helper**

```go
// core/event/secret.go
package event

import "regexp"

// SecretPattern matches common secret formats (API keys, tokens, passwords).
var secretPatterns = []*regexp.Regexp{
    regexp.MustCompile(`sk-(ant-)?[A-Za-z0-9_-]{20,}`),       // Anthropic / OpenAI
    regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),               // GitHub
    regexp.MustCompile(`(?i)password[\s:=]+["']?[^\s"']{8,}`),
    regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]{20,}`),
}

// MaskSecrets returns s with detected secrets replaced by <secret_hidden>.
func MaskSecrets(s string) string {
    out := s
    for _, re := range secretPatterns {
        out = re.ReplaceAllString(out, "<secret_hidden>")
    }
    return out
}
```

- [ ] **Step 2: Persister applies masking on Write**

In `Persister.Write()`, before marshaling, apply `MaskSecrets` to e.Type, e.Data (treat as string for masking; convert back to []byte after).

- [ ] **Step 3: Tests** (3-4 tests covering API key masks, password masks, no-false-positive on non-secret text)
- [ ] **Step 4: Commit** `feat(core/event): secret masking on persistence (Anthropic/OpenAI/GitHub/Bearer/password patterns)`

---

## Task 2: AgentLoop emits events to per-session EventStream

**Files:**
- Modify: `core/runner/runner.go` — add `Events *event.Stream` field; emit per-iteration events
- Modify: `core/runner/runner_test.go` — verify events emitted

- [ ] **Step 1: Events field on AgentLoop**

```go
type AgentLoop struct {
    // ... existing fields
    Events *event.Stream  // optional; if nil, no events emitted
}
```

- [ ] **Step 2: Emit at key points** (each as a single Event with Source/Kind/Type/Data)

- `iteration_start` (system, note, type="iteration_start", data={"iter":N})
- `provider_request` (agent, action, type="provider_request", data={tokens estimate})
- `provider_response` (agent, observation, type="provider_response", data={text snippet, tool_call count})
- `tool_call` per tool call (agent, action, type="tool_call", data={name, input snippet})
- `tool_result` per tool result (environment, observation, type="tool_result", data={iserror, content snippet})
- `verify_run` (system, action)
- `verify_result` per check (environment, observation, data={name, passed, exit_code})
- `iteration_end` (system, note, data={action_count})
- `run_done` or `run_max_iterations` or `run_error` at end

(Implementer can simplify to ~5 most important event types if 9 is too many for v1.)

- [ ] **Step 3: Test that emitted events are observable via `events.Subscribe()`**
- [ ] **Step 4: Commit** `feat(core/runner): AgentLoop emits events to optional Stream subscriber`

---

## Task 3: per-session event Persister wired in RunService

**Files:**
- Modify: `server/internal/service/run.go` — create per-session event.Stream + Persister, attach to AgentLoop
- Add: per-session event dir at `~/.gil/sessions/{id}/events/` (Persister already supports this)

- [ ] **Step 1: In RunService.Start, before AgentLoop:**

```go
eventDir := filepath.Join(s.sessionDir(req.SessionId), "events")
persister, err := event.NewPersister(eventDir)
if err != nil { return error }
defer persister.Close()

stream := event.NewStream()
sub := stream.Subscribe(256)
go func() {
    for evt := range sub.Events() {
        _ = persister.Write(evt)
    }
}()
defer sub.Close()

loop := runner.NewAgentLoop(spec, prov, model, tools, ver)
loop.Events = stream  // wire it up
```

- [ ] **Step 2: Add this server-side stream to a serviceMap so Tail can subscribe later**

Add `runStreams map[string]*event.Stream` to RunService (per-session map keyed by sessionID). Need mutex.

- [ ] **Step 3: Test** that after Start completes, events.jsonl in session dir has events
- [ ] **Step 4: Commit** `feat(server): per-session event Persister wired into RunService.Start`

---

## Task 4: RunService.Tail real implementation

**Files:**
- Modify: `server/internal/service/run.go` — Tail subscribes to per-session stream

- [ ] **Step 1: Implement Tail**

```go
func (s *RunService) Tail(req *gilv1.TailRequest, stream gilv1.RunService_TailServer) error {
    s.mu.Lock()
    rs, ok := s.runStreams[req.SessionId]
    s.mu.Unlock()
    if !ok {
        // No active run — could try replay from disk, but for v1 just return Unimplemented
        return status.Errorf(codes.NotFound, "no active run for session %q", req.SessionId)
    }
    sub := rs.Subscribe(256)
    defer sub.Close()
    for evt := range sub.Events() {
        if err := stream.Send(toProtoEvent(evt)); err != nil {
            return err
        }
    }
    return nil
}
```

`toProtoEvent` converts `event.Event` to `gilv1.Event` proto.

- [ ] **Step 2: Cleanup** — when run finishes, delete from runStreams map
- [ ] **Step 3: Test** with bufconn-based gRPC test that verifies events flow client-side
- [ ] **Step 4: Commit** `feat(server): RunService.Tail subscribes to per-session event stream`

---

## Task 5: gil events --tail real subscription + tests

**Files:**
- Modify: `cli/internal/cmd/events.go` — remove "Phase 5 stub" handling, just stream
- Modify: `cli/internal/cmd/events_test.go` — test stub server now returns real events

- [ ] **Step 1: Update events.go** to format events more readably:

```go
fmt.Fprintf(out, "%s %s %s\n", evt.GetTimestamp().AsTime().Format(time.RFC3339), evt.GetType(), string(evt.GetDataJson()))
```

- [ ] **Step 2: Test stub server** to emit a few synthetic events
- [ ] **Step 3: Commit** `feat(cli): gil events --tail prints live events (formatted)`

---

## Task 6: gil run --detach + CLI status enrich

**Files:**
- Modify: `cli/internal/cmd/run.go` — add `--detach` flag; if set, return immediately after Start kicks off
- Modify: `server/internal/service/run.go` — when called with `detach=true`, run AgentLoop in goroutine and return immediately
- Modify: proto Start/StartRunRequest — add `detach bool` field
- Regenerate proto

- [ ] **Step 1: proto change**: Add `bool detach = 4;` to StartRunRequest. `cd proto && buf generate`
- [ ] **Step 2: server side detach handling**:

```go
if req.Detach {
    go func() {
        // run loop in goroutine; events still emit
        _, _ = loop.Run(context.Background())  // detached from caller ctx
        // update session status
    }()
    return &gilv1.StartRunResponse{Status: "running", Iterations: 0}, nil
}
// else: existing synchronous path
```

- [ ] **Step 3: CLI** — if --detach, just print "Run started in background. Use 'gil status <id>' or 'gil events <id> --tail' to observe."
- [ ] **Step 4: Enrich `gil status <id>`** to show: status, current iterations (from event log count if running), last event timestamp
- [ ] **Step 5: E2E sanity** — start detached, tail, see events flow
- [ ] **Step 6: Commit** `feat: gil run --detach + per-session async run + status enrich`

---

## Task 7: core/stuck Detector (OpenHands 5 patterns)

**Files:**
- Create: `core/stuck/detector.go`
- Create: `core/stuck/detector_test.go`

- [ ] **Step 1: Detector reads recent events, returns Signal if any pattern matches**

```go
type Pattern int
const (
    PatternRepeatedActionObservation Pattern = iota  // same action+obs 4+
    PatternRepeatedActionError                        // same action+err 3+
    PatternMonologue                                   // 3+ assistant turns no user/tool input
    PatternPingPong                                    // ABABAB 6+
    PatternContextWindowError                          // ctx overflow repeats
)

type Signal struct {
    Pattern Pattern
    Detail  string
}

type Detector struct {
    Window int  // number of recent events to consider; default 50
}

func (d *Detector) Check(events []event.Event) []Signal { ... }
```

- [ ] **Step 2: Implement each pattern detection** (semantic comparison: tool name + args hash + observation hash)
- [ ] **Step 3: Tests for each pattern**
- [ ] **Step 4: Commit** `feat(core/stuck): Detector for OpenHands 5 stuck patterns`

---

## Task 8: core/stuck Recovery strategies

**Files:**
- Create: `core/stuck/recovery.go`
- Create: `core/stuck/recovery_test.go`

- [ ] **Step 1: Strategy interface + 5 implementations**

```go
type Strategy interface {
    Name() string
    Apply(ctx context.Context, ar *ApplyRequest) (*ApplyResult, error)
}

type ApplyRequest struct {
    State    *runner.AgentLoopState  // would need to expose state from runner
    Provider provider.Provider
    // ... 
}

type AltToolOrderStrategy struct{}     // suggest different tool order via prompt
type ModelEscalateStrategy struct{}    // switch to escalation_chain[next]
type SubagentBranchStrategy struct{}   // dispatch fresh sub-engine on same goal
type ResetSectionStrategy struct{}     // restore to last shadow git commit + retry
type AdversaryConsultStrategy struct{} // call adversary on stuck context
```

For Phase 5, implement skeletons that LOG-ONLY (don't actually mutate). Real recovery → Phase 6 (needs interaction with AgentLoop's runtime state which requires refactoring).

Actually let me scope down: just implement Detector (T7) and a SIMPLE "abort on N stucks" reaction in AgentLoop (T9). Full strategy engine = Phase 6.

- [ ] **Step 1 (revised): Strategy is a stub — interface + 1 working implementation (ModelEscalateStrategy: change model name in next provider call)**
- [ ] **Step 2: Document deferred strategies in code comments**
- [ ] **Step 3: Tests for ModelEscalateStrategy**
- [ ] **Step 4: Commit** `feat(core/stuck): Recovery interface + ModelEscalateStrategy (others Phase 6)`

---

## Task 9: AgentLoop integrates stuck detection

**Files:**
- Modify: `core/runner/runner.go` — every N iterations call Detector.Check, log Signal as event, optionally apply ModelEscalate

- [ ] **Step 1: Add Stuck *stuck.Detector field to AgentLoop**
- [ ] **Step 2: Every iteration, run Detector**; if signal → emit `stuck_detected` event; after 3 detections → emit `stuck_unrecovered` and return Result{Status: "stuck"}
- [ ] **Step 3: Test** with mock that loops same tool call 5+ times → AgentLoop returns "stuck"
- [ ] **Step 4: Commit** `feat(core/runner): AgentLoop detects stuck patterns and aborts after 3 unrecovered`

---

## Task 10: runtime/local/bwrap (Linux sandbox)

**Files:**
- Create: `runtime/local/bwrap.go`
- Create: `runtime/local/bwrap_test.go`

Codex의 `linux-sandbox/src/bwrap.rs` 패턴 차용. Go에서는 `exec.Command("bwrap", args...)` 래퍼.

- [ ] **Step 1: Sandbox interface**

```go
package local

type Mode int
const (
    ModeReadOnly Mode = iota
    ModeWorkspaceWrite
    ModeFullAccess
)

type Sandbox struct {
    WorkspaceDir string
    Mode         Mode
}

// Wrap returns args needed to run cmd inside bwrap with this sandbox config.
func (s *Sandbox) Wrap(cmd string, args ...string) []string { ... }
```

- [ ] **Step 2: Generate bwrap args** based on mode:
  - All modes: `--unshare-user --unshare-pid --die-with-parent`
  - ReadOnly: `--ro-bind / / --tmpfs /tmp` + workspace `--ro-bind {dir}`
  - WorkspaceWrite: `--ro-bind / / --bind {workspace} {workspace} --tmpfs /tmp`
  - FullAccess: pass-through (no bwrap)

- [ ] **Step 3: Tests** (skip on macOS / no bwrap installed). Verify generated args match expected.
- [ ] **Step 4: Commit** `feat(runtime/local): Linux bwrap sandbox wrapper`

---

## Task 11: core/tool wraps via Sandbox (when configured)

**Files:**
- Modify: `core/tool/bash.go` — Bash takes optional Sandbox; if set, wraps the command via bwrap
- Modify: `core/tool/file.go` — file ops respect read-only mode (no-op in v1; just check Sandbox.Mode)

For now, only Bash uses Sandbox. WriteFile/ReadFile operate via Go's os package (which is process-level). To truly sandbox file ops we'd need to run them through bwrap too, but that's complex. v1: if Mode==ReadOnly, WriteFile errors out.

- [ ] **Step 1: Bash.Sandbox field + Run wrapping**
- [ ] **Step 2: WriteFile.Mode check (ReadOnly → IsError=true)**
- [ ] **Step 3: Tests** — verify Bash with sandbox actually executes (will skip if bwrap not installed)
- [ ] **Step 4: Commit** `feat(core/tool): Bash optional sandbox + WriteFile read-only enforcement`

---

## Task 12: RunService builds tools with sandbox from spec.workspace.backend

**Files:**
- Modify: `server/internal/service/run.go` — when spec.workspace.backend == LOCAL_SANDBOX, wrap Bash with sandbox

- [ ] **Step 1: Build Sandbox from spec.workspace.backend**:

```go
var sb *local.Sandbox
if spec.Workspace.Backend == gilv1.WorkspaceBackend_LOCAL_SANDBOX {
    sb = &local.Sandbox{WorkspaceDir: workspaceDir, Mode: local.ModeWorkspaceWrite}
}
tools := []tool.Tool{
    &tool.Bash{WorkingDir: workspaceDir, Sandbox: sb},
    // ...
}
```

- [ ] **Step 2: Test** — verify with LOCAL_SANDBOX backend, tools are sandboxed
- [ ] **Step 3: Commit** `feat(server): RunService respects spec.workspace.backend (LOCAL_SANDBOX → bwrap)`

---

## Task 13: core/checkpoint Shadow Git

**Files:**
- Create: `core/checkpoint/shadow.go`
- Create: `core/checkpoint/shadow_test.go`

Cline 패턴: 별도 `.git` directory at `~/.gil/shadow/{cwd-hash}/.git`, `core.worktree` 가 사용자 워크스페이스 가리킴. 매 step 후 `git add -A && git commit --allow-empty --no-verify`.

Use `os/exec` to invoke `git` (no go-git dependency for simplicity).

```go
package checkpoint

type ShadowGit struct {
    GitDir       string  // ~/.gil/shadow/{hash}/.git
    WorkspaceDir string
}

func New(workspaceDir, baseDir string) *ShadowGit  // computes hash
func (s *ShadowGit) Init() error                   // git init + config
func (s *ShadowGit) Commit(msg string) (string, error)  // returns commit SHA
func (s *ShadowGit) Restore(commitSHA string) error
func (s *ShadowGit) ListCommits() ([]Commit, error)
```

- [ ] **Step 1: Init implementation** — `git init --bare` then set core.worktree, user.name, etc.
- [ ] **Step 2: Commit** — `git --git-dir=... --work-tree=... add -A && commit --allow-empty --no-verify -m msg`
- [ ] **Step 3: Restore** — `git --git-dir=... --work-tree=... checkout <SHA> -- .`
- [ ] **Step 4: Tests** — create temp workspace, init shadow git, write file, commit, modify file, restore → verify content reverted
- [ ] **Step 5: Commit** `feat(core/checkpoint): Shadow Git per-step checkpoints (separate .git, untouched user repo)`

---

## Task 14: AgentLoop checkpoints per step

**Files:**
- Modify: `core/runner/runner.go` — Checkpoint *checkpoint.ShadowGit field; after each tool dispatch, commit step

- [ ] **Step 1: Add field, call Commit after tool dispatch loop in iteration**
- [ ] **Step 2: Emit `checkpoint_committed` event with SHA**
- [ ] **Step 3: Test** — after run, verify shadow git has N commits matching iterations
- [ ] **Step 4: Commit** `feat(core/runner): AgentLoop commits to ShadowGit after each step`

---

## Task 15: gil restore <session-id> <step-n>

**Files:**
- Create: `cli/internal/cmd/restore.go`
- Modify: `proto/gil/v1/run.proto` — add Restore RPC
- Modify: `server/internal/service/run.go` — Restore handler
- Modify: `sdk/client.go` — RestoreRun method

- [ ] **Step 1: Restore RPC takes session_id + step (1-indexed iteration number); server restores to corresponding shadow git commit**
- [ ] **Step 2: CLI prints "Restored session <id> to step <n> (commit <sha>)"**
- [ ] **Step 3: Test** — checkpoint a couple of states, restore, verify
- [ ] **Step 4: Commit** `feat: gil restore <id> <step> rolls back via ShadowGit`

---

## Task 16: Per-stage model separation

**Files:**
- Modify: `server/internal/service/run.go` — read spec.models.{main,weak,editor,adversary}, pass appropriate models to AgentLoop sub-engines (well, AgentLoop is single-model in Phase 4 — actually the place this matters is in the Engine sub-engines from Phase 3 (SlotFiller/Adversary/Audit). Already factored.)
- Modify: `server/internal/service/interview.go` — already supports model-per-sub-engine, just pass spec.models.X.ModelId values instead of single shared model

For runner specifically, AgentLoop uses one model. Per-stage isn't applicable to runner directly. Update runner to optionally use spec.models.editor for "small" sub-tasks (e.g., file-write tool calls) — but this is an optimization that doesn't fit Phase 4 cleanly. Defer to Phase 6 fully.

For interview only (Phase 3 already had this), pass appropriate models from spec.

- [ ] **Step 1: InterviewService factory uses spec.models.{interview, slot, adversary} instead of single model**
- [ ] **Step 2: Test** — verify different models named in resp logs
- [ ] **Step 3: Commit** `feat(server): per-stage model selection from spec.models for interview sub-engines`

---

## Task 17: E2E phase05 — full async run with sandbox + checkpoint + tail

**Files:**
- Create: `tests/e2e/phase05_test.sh`
- Modify: `Makefile` — e2e5 + e2e-all

- [ ] **Script:**
  - GIL_MOCK_MODE=run-hello daemon
  - manual frozen spec with workspace.backend=LOCAL_SANDBOX (or LOCAL_NATIVE if bwrap not installed)
  - `gil run <id> --detach`
  - `gil events <id> --tail` (background, capture for N seconds)
  - verify hello.txt created
  - verify shadow git has commits (`git --git-dir=...shadow/{hash}/.git log --oneline | wc -l`)
  - `gil restore <id> 1` rolls back
  - verify hello.txt no longer exists (or reverted)

- [ ] **Commit** `test(e2e): phase 5 — async run + checkpoint + restore + sandbox sanity`

---

## Task 18: Makefile install + progress.md Phase 5 update

**Files:**
- Modify: `Makefile` — add `install` target
- Modify: `docs/progress.md` — Phase 5 complete

- [ ] **Step 1: Makefile install**:

```makefile
install: build
	@install -m 0755 bin/gil  /usr/local/bin/gil  || sudo install -m 0755 bin/gil  /usr/local/bin/gil
	@install -m 0755 bin/gild /usr/local/bin/gild || sudo install -m 0755 bin/gild /usr/local/bin/gild
	@echo "Installed gil and gild to /usr/local/bin"
```

- [ ] **Step 2: progress.md Phase 5 update** — mark all checked, add 결정사항 row, add 산출물 요약
- [ ] **Step 3: Commit** `feat: make install target + Phase 5 complete`

---

## Phase 5 완료 체크리스트

- [ ] `make e2e-all` 5 phase 모두 통과
- [ ] `gil run --detach` 작동
- [ ] `gil events <id> --tail` 실시간 이벤트 출력
- [ ] `gil restore <id> <step>` 작동
- [ ] LOCAL_SANDBOX 백엔드에서 bwrap 활성 (Linux only)
- [ ] secret masking 검증 (sk-ant- 시작 문자열이 events.jsonl에서 안 보임)
- [ ] make install로 /usr/local/bin에 설치 가능

## Phase 6 미루는 항목

- 진짜 sub-agent (delegate_task) 도구
- 메모리 뱅크 (6 markdown files)
- 컨텍스트 압축 (캐시 보존, Hermes 패턴)
- macOS Seatbelt sandbox
- Stuck 회복 strategy 5종 풀 구현 (현재는 ModelEscalate만)
- TUI (Bubbletea)
- microagents
