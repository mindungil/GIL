# Phase 4 — Run Engine (자율 실행 + 검증)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` for tracking.

**Goal:** Frozen spec을 받아 LLM이 도구를 사용해 실제로 코드를 작성/실행하고, verification.checks가 모두 통과할 때까지 자율 루프를 돌린다. **gil의 핵심 가치 "자율 실행"의 첫 구현.**

**End-to-end 시나리오 (Phase 4 종료 시점):**
```bash
# 인터뷰로 spec 만들기 (Phase 1-3, 이미 작동)
gil new --working-dir /tmp/hello
gil interview <id> --provider anthropic
# → 자연어로 "Go file hello.go that prints hello world" 같은 요구사항 입력
# → spec freeze

# 실행 (Phase 4 신규)
gil run <id>
# → AgentLoop 시작: Claude가 bash/write_file 도구로 hello.go 작성
# → verifier가 spec.verification.checks 실행 (예: "go run hello.go | grep -q hello")
# → exit 0 → "✅ Done!"

# 라이브 이벤트 관찰
gil events <id> --tail
```

**Architecture:**
- `core/tool` — Tool 인터페이스 + 빌트인 (bash, write_file, read_file)
- `core/runner` — AgentLoop (Anthropic native tool use 기반), max iterations / budget
- `core/verify` — Goose `retry.checks` 패턴 (셸 단언 실행기, 0 exit = pass)
- `core/event` 통합 — 모든 run 액션이 event stream + persister에 기록
- `core/provider/retry` — exponential backoff wrapper (5xx/timeout 자동 재시도)
- `proto.RunService` — Start (단일 응답: 성공/실패) + Tail (이벤트 스트림 구독)
- `server/RunService` 구현 + gild 등록
- `cli/run`, `cli/events`

**범위 한정 (Phase 5+로 미루는 것):**
- 진짜 sandbox (bwrap/seatbelt) — Phase 5
- Shadow git checkpoint — Phase 5
- Stuck detection + 자가 회복 — Phase 5
- Memory bank — Phase 6
- 컨텍스트 압축 (캐시 보존) — Phase 6
- Sub-agent — Phase 6+

Phase 4는 "naive 자율 실행" — 사용자 워크스페이스에 직접 쓰고, git 추적 없이, 막히면 그냥 max iterations로 종료. Phase 5에서 sandbox + checkpoint 추가.

---

## Task 1: Tool 인터페이스 정의 + 빌트인 bash

**Files:**
- Create: `core/tool/tool.go` — Tool 인터페이스 + ToolCall/ToolResult 타입
- Create: `core/tool/bash.go` — Bash tool implementation
- Create: `core/tool/bash_test.go`

- [ ] **Step 1: Tool 인터페이스**

```go
// Package tool defines the Tool abstraction and built-in tools used by the
// run engine.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is implemented by anything the agent can call. Schema is the JSON
// schema sent to the LLM (Anthropic native tool use format).
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Run(ctx context.Context, argsJSON json.RawMessage) (Result, error)
}

// Result is the outcome of a tool invocation, sent back to the LLM as a
// tool_result block.
type Result struct {
	Content string // text rendered into the next LLM turn
	IsError bool   // marks the result as an error (LLM may retry)
}
```

- [ ] **Step 2: Bash tool**

```go
// core/tool/bash.go
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Bash runs shell commands in the project's working directory.
type Bash struct {
	WorkingDir string
	Timeout    time.Duration // per-command timeout; 0 → 60s default
}

const bashSchema = `{
  "type":"object",
  "properties":{
    "command":{"type":"string","description":"shell command to execute"}
  },
  "required":["command"]
}`

func (b *Bash) Name() string        { return "bash" }
func (b *Bash) Description() string { return "Execute a shell command in the project working directory and return stdout+stderr." }
func (b *Bash) Schema() json.RawMessage { return json.RawMessage(bashSchema) }

func (b *Bash) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("bash unmarshal: %w", err)
	}
	if args.Command == "" {
		return Result{Content: "command is empty", IsError: true}, nil
	}

	timeout := b.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
	cmd.Dir = b.WorkingDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		cmd.ProcessState.ExitCode(), truncate(stdout.String(), 8192), truncate(stderr.String(), 4096))

	return Result{Content: output, IsError: err != nil}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
```

- [ ] **Step 3: Tests**

```go
// bash_test.go
func TestBash_Echo(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	require.NoError(t, err)
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "exit=0")
	require.Contains(t, r.Content, "hi")
}

func TestBash_FailingCommand(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"exit 7"}`))
	require.NoError(t, err) // Run itself doesn't error on non-zero exit
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "exit=7")
}

func TestBash_Timeout(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir(), Timeout: 100 * time.Millisecond}
	_, err := b.Run(context.Background(), json.RawMessage(`{"command":"sleep 1"}`))
	require.NoError(t, err) // CommandContext kills, exit code != 0 returned in result
}

func TestBash_EmptyCommand(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":""}`))
	require.NoError(t, err)
	require.True(t, r.IsError)
}
```

- [ ] **Step 4: Test + commit**

```bash
git add core/tool/tool.go core/tool/bash.go core/tool/bash_test.go
git commit -m "feat(core/tool): Tool interface + Bash builtin (with timeout + truncation)"
```

---

## Task 2: write_file + read_file tools

**Files:**
- Create: `core/tool/file.go`
- Create: `core/tool/file_test.go`

- [ ] **Step 1: write_file**

```go
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes a file relative to the working directory.
type WriteFile struct {
	WorkingDir string
}

const writeFileSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"relative path within the project"},
    "content":{"type":"string","description":"full file content"}
  },
  "required":["path","content"]
}`

func (w *WriteFile) Name() string        { return "write_file" }
func (w *WriteFile) Description() string { return "Create or overwrite a file with the given content." }
func (w *WriteFile) Schema() json.RawMessage { return json.RawMessage(writeFileSchema) }

func (w *WriteFile) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("write_file unmarshal: %w", err)
	}
	abs := filepath.Join(w.WorkingDir, args.Path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := os.WriteFile(abs, []byte(args.Content), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)}, nil
}

// ReadFile reads a file relative to the working directory.
type ReadFile struct {
	WorkingDir string
}

const readFileSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"relative path"}
  },
  "required":["path"]
}`

func (r *ReadFile) Name() string        { return "read_file" }
func (r *ReadFile) Description() string { return "Return the contents of a file." }
func (r *ReadFile) Schema() json.RawMessage { return json.RawMessage(readFileSchema) }

func (r *ReadFile) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("read_file unmarshal: %w", err)
	}
	abs := filepath.Join(r.WorkingDir, args.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: truncate(string(data), 16384)}, nil
}
```

- [ ] **Step 2: Tests** (4-5 cases: write+read roundtrip, missing file, MkdirAll path)
- [ ] **Step 3: Commit**

```bash
git add core/tool/file.go core/tool/file_test.go
git commit -m "feat(core/tool): WriteFile + ReadFile builtins"
```

---

## Task 3: core/verify.Runner — shell check 실행기

**Files:**
- Create: `core/verify/runner.go`
- Create: `core/verify/runner_test.go`

- [ ] **Step 1: Tests**

```go
package verify

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestRunner_AllPass(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "true-check", Kind: gilv1.CheckKind_SHELL, Command: "true"},
		{Name: "echo-check", Kind: gilv1.CheckKind_SHELL, Command: "echo ok"},
	}

	results, allPass := r.RunAll(context.Background(), checks)
	require.True(t, allPass)
	require.Len(t, results, 2)
	for _, res := range results {
		require.True(t, res.Passed)
		require.Equal(t, 0, res.ExitCode)
	}
}

func TestRunner_OnePassOneFail(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "ok", Kind: gilv1.CheckKind_SHELL, Command: "true"},
		{Name: "bad", Kind: gilv1.CheckKind_SHELL, Command: "exit 5"},
	}

	results, allPass := r.RunAll(context.Background(), checks)
	require.False(t, allPass)
	require.True(t, results[0].Passed)
	require.False(t, results[1].Passed)
	require.Equal(t, 5, results[1].ExitCode)
}

func TestRunner_RespectsExpectedExitCode(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "expect-2", Kind: gilv1.CheckKind_SHELL, Command: "exit 2", ExpectedExitCode: 2},
	}

	results, allPass := r.RunAll(context.Background(), checks)
	require.True(t, allPass) // exit 2 matches expected
	require.True(t, results[0].Passed)
}

func TestRunner_FilesystemContext(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir)
	// Create a file
	rChecks := []*gilv1.Check{
		{Name: "create", Kind: gilv1.CheckKind_SHELL, Command: "touch /tmp/never; touch ./local"},
	}
	results, _ := r.RunAll(context.Background(), rChecks)
	require.True(t, results[0].Passed)
	// Verify ./local appeared in dir, not /tmp
	_, err := filepath.Glob(filepath.Join(dir, "local"))
	require.NoError(t, err)
}
```

- [ ] **Step 2: runner.go**

```go
// Package verify executes spec.Verification.Checks (shell assertions) and
// reports per-check results.
package verify

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// Result is the outcome of a single check.
type Result struct {
	Name     string
	Passed   bool
	ExitCode int
	Stdout   string // truncated to 4KB
	Stderr   string // truncated to 4KB
	Duration time.Duration
}

// Runner executes Check shell commands in a working directory.
type Runner struct {
	WorkingDir string
}

// NewRunner returns a Runner bound to workingDir.
func NewRunner(workingDir string) *Runner {
	return &Runner{WorkingDir: workingDir}
}

// RunAll runs every check and returns the per-check results plus an overall
// allPass flag. Each check has up to 60s wall-clock by default.
func (r *Runner) RunAll(ctx context.Context, checks []*gilv1.Check) ([]Result, bool) {
	out := make([]Result, 0, len(checks))
	allPass := true
	for _, c := range checks {
		res := r.runOne(ctx, c)
		out = append(out, res)
		if !res.Passed {
			allPass = false
		}
	}
	return out, allPass
}

func (r *Runner) runOne(ctx context.Context, c *gilv1.Check) Result {
	start := time.Now()
	timeout := 60 * time.Second
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(rctx, "bash", "-c", c.Command)
	cmd.Dir = r.WorkingDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // we read exit code from ProcessState
	exitCode := cmd.ProcessState.ExitCode()

	expected := int(c.ExpectedExitCode)
	passed := exitCode == expected

	return Result{
		Name:     c.Name,
		Passed:   passed,
		ExitCode: exitCode,
		Stdout:   trunc4k(stdout.String()),
		Stderr:   trunc4k(stderr.String()),
		Duration: time.Since(start),
	}
}

func trunc4k(s string) string {
	const max = 4096
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
```

- [ ] **Step 3: Test + commit**

```bash
git add core/verify/
git commit -m "feat(core/verify): Runner executes spec.Verification.Checks (shell, exit code based)"
```

---

## Task 4: Provider retry/backoff wrapper

**Files:**
- Create: `core/provider/retry.go`
- Create: `core/provider/retry_test.go`

- [ ] **Step 1: Wrapper that retries 5xx/timeout with exponential backoff**

```go
package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Retry wraps a Provider and retries transient errors (5xx, timeouts) with
// exponential backoff. Retries up to maxAttempts times.
type Retry struct {
	Wrapped     Provider
	MaxAttempts int           // total attempts (1 = no retry); default 4
	BaseDelay   time.Duration // initial backoff; default 500ms
}

// NewRetry returns a Retry around inner with sensible defaults.
func NewRetry(inner Provider) *Retry {
	return &Retry{Wrapped: inner, MaxAttempts: 4, BaseDelay: 500 * time.Millisecond}
}

func (r *Retry) Name() string { return r.Wrapped.Name() + "+retry" }

func (r *Retry) Complete(ctx context.Context, req Request) (Response, error) {
	max := r.MaxAttempts
	if max <= 0 {
		max = 4
	}
	delay := r.BaseDelay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}

	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		resp, err := r.Wrapped.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return resp, err
		}
		if attempt == max {
			break
		}
		// Wait, but respect ctx cancellation
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return resp, ctx.Err()
		}
		delay *= 2
	}
	return Response{}, lastErr
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false // caller cancelled
	}
	msg := err.Error()
	for _, sig := range []string{
		"500", "502", "503", "504", "529",
		"timeout", "connection reset", "EOF",
		"overloaded", "rate_limit", "rate limit",
	} {
		if strings.Contains(strings.ToLower(msg), sig) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Tests** with a fake provider that fails N times then succeeds:

```go
type flakyProvider struct {
	failsLeft int
	failErr   error
}

func (f *flakyProvider) Name() string { return "flaky" }
func (f *flakyProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if f.failsLeft > 0 {
		f.failsLeft--
		return Response{}, f.failErr
	}
	return Response{Text: "ok"}, nil
}

func TestRetry_RetriesTransient(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 2, failErr: errors.New("status 503 service unavailable")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 4, BaseDelay: 1 * time.Millisecond}
	resp, err := r.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Text)
}

func TestRetry_GivesUpAfterMax(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 100, failErr: errors.New("503")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}
	_, err := r.Complete(context.Background(), Request{})
	require.Error(t, err)
}

func TestRetry_NonRetryablePropagates(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 100, failErr: errors.New("invalid api key")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 4, BaseDelay: 1 * time.Millisecond}
	_, err := r.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid api key")
}
```

- [ ] **Step 3: Commit**

```bash
git add core/provider/retry.go core/provider/retry_test.go
git commit -m "feat(core/provider): Retry wrapper with exponential backoff for transient errors"
```

---

## Task 5: core/runner — AgentLoop with Anthropic native tool use

**Files:**
- Create: `core/runner/runner.go`
- Create: `core/runner/runner_test.go`

This is the biggest task. AgentLoop:
1. System prompt = derive from spec (goal, constraints, available tools)
2. Loop:
   - Call provider.Complete with tools schema
   - If response has tool_calls: dispatch each to the matching tool, append tool_result blocks to next turn
   - If response has no tool_calls: assume done, run verifier
   - If verifier passes: return Done
   - If verifier fails: append observation, continue
   - Track iteration count, hard cap at spec.budget.max_iterations (default 100)
   - Track tokens, hard cap at spec.budget.max_total_tokens

**Note:** Anthropic native tool use is the cleanest path. We need to extend `core/provider.Request` and `Response` to carry tool definitions and tool_use blocks. Or we keep current text-only Request and use a separate `CompleteWithTools` method.

**Recommended extension to provider package** (do this BEFORE implementing runner):

Add to `core/provider/provider.go`:

```go
// ToolDef is a tool definition sent to the LLM.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage  // JSON schema
}

// ToolCall is a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Add fields to Request:
//   Tools []ToolDef
//
// Add fields to Response:
//   ToolCalls []ToolCall  // populated if model wants to call tools
//   StopReason "end_turn" | "tool_use" | ...
```

Then update `core/provider/anthropic.go` to translate Tools → params.Tools and parse tool_use blocks from response.

Update `core/provider/mock.go` to support a script of `MockResponse{Text, ToolCalls}` instead of just strings (or use a new mock type for tool tests).

This is part of Task 5 itself — do all this together since runner depends on it.

- [ ] **Step 1: Extend provider types + Anthropic adapter**
- [ ] **Step 2: Update Mock provider to script tool calls** (probably new type `MockToolProvider`)
- [ ] **Step 3: runner.go**

Skeleton:

```go
// Package runner implements the autonomous AgentLoop that drives a Frozen
// Spec to completion.
package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// Result is the final outcome of an AgentLoop run.
type Result struct {
	Status      string // "done" | "max_iterations" | "budget_exhausted" | "error"
	Iterations  int
	Tokens      int64
	CostUSD     float64
	VerifyAll   []verify.Result
	FinalError  error
}

// AgentLoop drives Spec to completion using prov + tools.
type AgentLoop struct {
	Spec      *gilv1.FrozenSpec
	Provider  provider.Provider
	Model     string
	Tools     []tool.Tool
	Verifier  *verify.Runner
}

// NewAgentLoop constructs a loop. tools is keyed by tool name.
func NewAgentLoop(spec *gilv1.FrozenSpec, prov provider.Provider, model string, tools []tool.Tool, ver *verify.Runner) *AgentLoop {
	return &AgentLoop{Spec: spec, Provider: prov, Model: model, Tools: tools, Verifier: ver}
}

// Run executes the loop until verifier passes, max iterations, or budget exhausted.
func (a *AgentLoop) Run(ctx context.Context) (*Result, error) {
	maxIter := int(a.Spec.Budget.GetMaxIterations())
	if maxIter == 0 {
		maxIter = 100
	}

	system := buildSystemPrompt(a.Spec, a.Tools)
	tools := make([]provider.ToolDef, 0, len(a.Tools))
	toolByName := map[string]tool.Tool{}
	for _, t := range a.Tools {
		tools = append(tools, provider.ToolDef{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
		toolByName[t.Name()] = t
	}

	messages := []provider.Message{}
	// Initial user prompt
	messages = append(messages, provider.Message{
		Role:    provider.RoleUser,
		Content: "Begin by analyzing the spec, then take actions to satisfy the verification checks.",
	})

	var totalTokens int64
	for iter := 1; iter <= maxIter; iter++ {
		resp, err := a.Provider.Complete(ctx, provider.Request{
			Model:    a.Model,
			System:   system,
			Messages: messages,
			Tools:    tools,
			MaxTokens: 4096,
		})
		if err != nil {
			return &Result{Status: "error", Iterations: iter, FinalError: err}, err
		}
		totalTokens += resp.InputTokens + resp.OutputTokens

		// Append assistant turn
		messages = append(messages, provider.Message{Role: provider.RoleAssistant, Content: resp.Text})

		if len(resp.ToolCalls) == 0 {
			// No tool calls — agent thinks it's done; run verifier
			results, allPass := a.Verifier.RunAll(ctx, a.Spec.Verification.Checks)
			if allPass {
				return &Result{Status: "done", Iterations: iter, Tokens: totalTokens, VerifyAll: results}, nil
			}
			// Feed verifier failures back
			messages = append(messages, provider.Message{
				Role:    provider.RoleUser,
				Content: formatVerifyFeedback(results),
			})
			continue
		}

		// Execute each tool call, build a single user message with tool_result blocks
		var toolResultsContent string
		for _, tc := range resp.ToolCalls {
			t, ok := toolByName[tc.Name]
			if !ok {
				toolResultsContent += fmt.Sprintf("[tool %s] unknown tool\n", tc.Name)
				continue
			}
			r, err := t.Run(ctx, tc.Input)
			if err != nil {
				toolResultsContent += fmt.Sprintf("[tool %s] error: %v\n", tc.Name, err)
				continue
			}
			toolResultsContent += fmt.Sprintf("[tool %s id=%s]\n%s\n\n", tc.Name, tc.ID, r.Content)
		}
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: toolResultsContent})
	}

	return &Result{Status: "max_iterations", Iterations: maxIter, Tokens: totalTokens}, nil
}

func buildSystemPrompt(spec *gilv1.FrozenSpec, tools []tool.Tool) string {
	// concise system prompt: goal, constraints, list of tools, "verify often"
	// (implementer fleshes this out)
	return fmt.Sprintf("Goal: %s\n...", spec.Goal.GetOneLiner())
}

func formatVerifyFeedback(results []verify.Result) string {
	out := "Verification results:\n"
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		out += fmt.Sprintf("- %s: %s (exit=%d)\n", r.Name, status, r.ExitCode)
		if !r.Passed {
			out += "  stdout: " + r.Stdout + "\n"
			out += "  stderr: " + r.Stderr + "\n"
		}
	}
	out += "\nKeep going — fix the failing checks."
	return out
}
```

- [ ] **Step 4: Tests** — use Mock provider that scripts tool_use responses for a trivial 1-tool, 1-verify scenario:

```go
func TestAgentLoop_HelloWorld(t *testing.T) {
	dir := t.TempDir()

	// Mock provider scripts:
	// turn 1: tool_use write_file hello.go
	// turn 2: tool_use bash "go build ./..."
	// turn 3: text only "done" (triggers verify)
	// verify: "test -f hello.go" passes
	mock := newMockToolProvider([]mockTurn{
		{ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.go","content":"package main\nfunc main(){}"}`)},
		}},
		{Text: "I'm done."},
	})

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "create hello.go"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f hello.go"}},
		},
		Budget: &gilv1.Budget{MaxIterations: 5},
	}

	tools := []tool.Tool{&tool.WriteFile{WorkingDir: dir}, &tool.Bash{WorkingDir: dir}}
	loop := NewAgentLoop(spec, mock, "test-model", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
}
```

- [ ] **Step 5: Commit**

```bash
git add core/provider/provider.go core/provider/anthropic.go core/provider/mock.go \
    core/runner/runner.go core/runner/runner_test.go
git commit -m "feat(core): AgentLoop with native tool use + provider Tools/ToolCalls extension"
```

---

## Task 6: proto RunService

**Files:**
- Create: `proto/gil/v1/run.proto`
- Generated: `proto/gen/gil/v1/run.pb.go` + `run_grpc.pb.go`

```protobuf
syntax = "proto3";
package gil.v1;
import "gil/v1/event.proto";
option go_package = "github.com/jedutools/gil/proto/gen/gil/v1;gilv1";

service RunService {
  rpc Start(StartRunRequest) returns (StartRunResponse);
  rpc Tail(TailRequest) returns (stream Event);
}

message StartRunRequest {
  string session_id = 1;
}

message StartRunResponse {
  string status = 1;       // "done" | "max_iterations" | "error"
  int32  iterations = 2;
  int64  tokens = 3;
  double cost_usd = 4;
  repeated VerifyResult verify_results = 5;
  string error_message = 6;
}

message VerifyResult {
  string name = 1;
  bool   passed = 2;
  int32  exit_code = 3;
  string stdout = 4;
  string stderr = 5;
}

message TailRequest {
  string session_id = 1;
}
```

- [ ] **Step 1:** Write proto file
- [ ] **Step 2:** `cd proto && buf generate`
- [ ] **Step 3:** Verify compile
- [ ] **Step 4:** Commit

```bash
git add proto/gil/v1/run.proto proto/gen/gil/v1/run*
git commit -m "feat(proto): RunService Start + Tail (event stream)"
```

---

## Task 7: server/RunService 구현

**Files:**
- Create: `server/internal/service/run.go`
- Create: `server/internal/service/run_test.go`

- [ ] Wire RunService.Start: load spec from specstore, build tools (Bash + WriteFile + ReadFile bound to spec.workspace.path), build verifier, build AgentLoop, run synchronously (Phase 4 — async run = Phase 5), translate Result to StartRunResponse.

- [ ] Tail stub for Phase 4: just streams session events from event log (assuming we eventually wire event.Persister to AgentLoop). For now Tail can return Unimplemented or empty stream.

- [ ] Tests with mock provider scripted to produce a "hello world" run.

- [ ] Commit:

```bash
git add server/internal/service/run.go server/internal/service/run_test.go
git commit -m "feat(server/service): RunService Start runs AgentLoop synchronously"
```

---

## Task 8: gild main + SDK + CLI integration

**Files:**
- Modify: `server/cmd/gild/main.go` — register RunService
- Modify: `sdk/client.go` — add RunStart, RunTail
- Create: `cli/internal/cmd/run.go` — `gil run <id>` command
- Modify: `cli/internal/cmd/root.go` — register runCmd

- [ ] Hook all the pieces. Test with `make build` + manual smoke.
- [ ] Commit:

```bash
git add server/cmd/gild/main.go sdk/client.go cli/internal/cmd/run.go cli/internal/cmd/root.go
git commit -m "feat: gil run command wires RunService end-to-end"
```

---

## Task 9: gil events command (Tail subscription)

**Files:**
- Create: `cli/internal/cmd/events.go`

- [ ] `gil events <id> --tail` subscribes to RunService.Tail, prints each event as it arrives. Phase 4 minimum: just print event JSON one per line.

- [ ] Commit:

```bash
git add cli/internal/cmd/events.go cli/internal/cmd/root.go
git commit -m "feat(cli): gil events --tail streams session events"
```

---

## Task 10: E2E phase04 — actual hello world run

**Files:**
- Create: `tests/e2e/phase04_test.sh`
- Modify: `Makefile` — e2e4 + e2e-all

- [ ] Script does:
  - manual frozen spec (skip interview, write spec.yaml directly to ~/.gil/sessions/{id}/) for "create hello.go that prints hello"
  - `gil run <id>` with mock provider that scripts the right tool calls
  - assert Status=done

- [ ] Commit + verify `make e2e-all` passes 4 phases.

---

## Task 11: progress.md Phase 4 update

- [ ] Mark Phase 4 (or rename existing Phase 4 placeholder) as complete with summary.
- [ ] Commit.

---

## Phase 4 완료 체크리스트

- [ ] `make e2e-all` (4개 phase) 모두 통과
- [ ] Mock 시나리오로 "hello world" 자율 실행 가능
- [ ] (옵션) 실제 ANTHROPIC_API_KEY로 진짜 run 한 번 시연

## Phase 5 미루는 항목

- 진짜 sandbox (bwrap/Seatbelt + bind mounts)
- Shadow git checkpoint per step
- Stuck detection + 자가 회복
- 비동기 run (gil run --detach)
- core/event session 통합 (run events persisted)
- Memory bank
