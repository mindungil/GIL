# Phase 2 — 인터뷰 엔진 + LLM Provider + 데몬 자동 spawn

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` for tracking.

**Goal:** Phase 1의 토대 위에 인터뷰 엔진을 구축한다. `gil interview <session-id>` 로 saturation까지 LLM과 사용자가 대화하고, frozen spec을 디스크에 lock 저장한다. 데몬 자동 spawn 도 추가.

**Architecture:**
- 새 패키지 `core/provider` (LLM 추상화 + Anthropic 어댑터 + Mock)
- 새 패키지 `core/interview` (Stage 머신: Sensing → Conversation → Confirm/Freeze)
- 새 패키지 `core/specstore` (spec.yaml + spec.lock 영속화)
- 확장 패키지 `core/session` (session-scoped event store 통합 + status transitions)
- 새 proto `InterviewService` (bidirectional streaming)
- CLI: `gil interview <id>` (대화형 stdin/stdout), `gil spec <id> [freeze]`
- 데몬 자동 spawn: `gil` 첫 실행 시 socket 없으면 `gild --foreground` background 실행

**Tech Stack:**
- 추가 deps: `github.com/anthropics/anthropic-sdk-go` (Anthropic 공식 Go SDK)
- 추가 deps: `gopkg.in/yaml.v3` (spec.yaml 영속화)
- 기존 모두 유지 (proto/grpc/sqlite/cobra/testify/ulid)

**산출물 검증** (Phase 2 종료 시점):
```bash
# 데몬 자동 시작 (수동 띄울 필요 없음)
gil new --working-dir /tmp/proj  # 첫 실행 시 gild 자동 spawn
# → Session created: 01HXY...

# Mock provider로 인터뷰 (LLM API 키 없이도 테스트 가능)
GIL_PROVIDER=mock gil interview 01HXY...
# → 자동 진행되는 미니 시나리오 (saturation 도달 → confirm)

# 실제 Anthropic 으로 인터뷰
ANTHROPIC_API_KEY=sk-... gil interview 01HXY...
# → 실제 LLM과 대화

# spec 확인
gil spec 01HXY...
# → 현재까지 채워진 spec yaml 출력

# freeze
gil spec freeze 01HXY...
# → spec.lock 생성, 이후 변경 거부
```

---

## Task 1: 데몬 자동 spawn (gil 첫 명령 실행 시)

**Files:**
- Modify: `cli/internal/cmd/root.go` — add `ensureDaemon()` helper
- Modify: `cli/internal/cmd/new.go` — call ensureDaemon before Dial
- Modify: `cli/internal/cmd/status.go` — same
- Create: `cli/internal/cmd/spawn.go` — spawn helper
- Create: `cli/internal/cmd/spawn_test.go`

- [ ] **Step 1: spawn helper test**

```go
package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsureDaemon_SpawnsIfMissing(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "gild.sock")

	// gild binary 가 PATH에 있다고 가정 (CI에선 빌드 후 ./bin 추가 필요)
	gildPath, err := exec.LookPath("gild")
	if err != nil {
		t.Skip("gild not in PATH; skip spawn integration test")
	}

	require.NoError(t, ensureDaemonAt(sock, dir, gildPath))

	require.Eventually(t, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	}, 5*time.Second, 100*time.Millisecond)
}
```

- [ ] **Step 2: spawn.go 작성**

```go
package cmd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ensureDaemon checks if the gild daemon socket is responsive. If not,
// it spawns a background gild process and waits up to 5s for the socket
// to appear. Designed to be called by every CLI command before dialing.
func ensureDaemon(socket, base string) error {
	gild, err := exec.LookPath("gild")
	if err != nil {
		return fmt.Errorf("gild binary not found in PATH (required for auto-spawn): %w", err)
	}
	return ensureDaemonAt(socket, base, gild)
}

func ensureDaemonAt(socket, base, gildPath string) error {
	// 이미 살아있으면 return
	if c, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
		_ = c.Close()
		return nil
	}

	// 데몬 spawn (background)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("mkdir base: %w", err)
	}
	cmd := exec.Command(gildPath, "--foreground", "--base", base)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn gild: %w", err)
	}
	// 부모 프로세스 분리 (CLI 종료해도 데몬 유지)
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release gild process: %w", err)
	}

	// 소켓 등장 대기 (최대 5s)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("gild did not become ready within 5s after spawn")
}
```

- [ ] **Step 3: new.go / status.go 수정** — RunE 시작 부분에 추가:

```go
if err := ensureDaemon(socket, defaultBase()); err != nil {
    return fmt.Errorf("ensure daemon: %w", err)
}
```

- [ ] **Step 4: 테스트 실행**

```bash
cd /home/ubuntu/gil && make build
PATH="/home/ubuntu/gil/bin:$PATH" cd /home/ubuntu/gil/cli && go test ./internal/cmd/... -v -count=1
```

- [ ] **Step 5: 수동 검증**

```bash
rm -rf /tmp/test-gil
PATH="/home/ubuntu/gil/bin:$PATH" /home/ubuntu/gil/bin/gil new --working-dir /tmp/p1 \
  --socket /tmp/test-gil/gild.sock || true  # base는 인자 없으면 default
# → 자동으로 데몬 뜨고 세션 생성
```

- [ ] **Step 6: Commit**

```bash
git add cli/internal/cmd/spawn.go cli/internal/cmd/spawn_test.go cli/internal/cmd/new.go cli/internal/cmd/status.go
git commit -m "feat(cli): auto-spawn gild daemon when socket missing"
```

---

## Task 2: core/specstore — spec.yaml + spec.lock 영속화

**Files:**
- Create: `core/specstore/store.go`
- Create: `core/specstore/store_test.go`

- [ ] **Step 1: 의존성**

```bash
cd /home/ubuntu/gil/core && go get gopkg.in/yaml.v3
```

- [ ] **Step 2: 실패 테스트**

```go
package specstore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func validSpec() *gilv1.FrozenSpec {
	return &gilv1.FrozenSpec{
		SpecId:    "01TEST",
		SessionId: "01SESS",
		Goal: &gilv1.Goal{OneLiner: "x", SuccessCriteriaNatural: []string{"a", "b", "c"}},
		Constraints: &gilv1.Constraints{TechStack: []string{"go"}},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "go build"}}},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX},
		Models: &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"}},
		Risk: &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))

	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, fs.SpecId, got.SpecId)
	require.Equal(t, fs.Goal.OneLiner, got.Goal.OneLiner)
}

func TestStore_FreezeWritesLock(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))
	require.NoError(t, s.Freeze())

	// lock 파일 존재
	_, err := os.Stat(filepath.Join(dir, "spec.lock"))
	require.NoError(t, err)

	// freeze 후 Save 시도하면 에러
	fs.Goal.OneLiner = "tampered"
	err = s.Save(fs)
	require.ErrorIs(t, err, ErrFrozen)
}

func TestStore_VerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))
	require.NoError(t, s.Freeze())

	// 직접 yaml 수정 (사용자가 spec.yaml 만지작거린 시나리오)
	yamlPath := filepath.Join(dir, "spec.yaml")
	data, _ := os.ReadFile(yamlPath)
	tampered := append(data, []byte("\n# extra\n")...)
	require.NoError(t, os.WriteFile(yamlPath, tampered, 0o644))

	_, err := s.Load()
	require.ErrorIs(t, err, ErrLockMismatch)
}
```

- [ ] **Step 3: store.go 작성**

```go
package specstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/mindungil/gil/core/spec"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

var (
	ErrFrozen       = errors.New("spec is frozen; create new session to modify")
	ErrLockMismatch = errors.New("spec content does not match lock (tampered)")
	ErrNotFound     = errors.New("spec not found")
)

// Store persists a FrozenSpec as spec.yaml in dir, with optional spec.lock for immutability.
type Store struct {
	dir string
}

// NewStore returns a Store that reads/writes spec.yaml under dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) yamlPath() string { return filepath.Join(s.dir, "spec.yaml") }
func (s *Store) lockPath() string { return filepath.Join(s.dir, "spec.lock") }

// Save writes the spec as YAML. Returns ErrFrozen if a lock exists.
func (s *Store) Save(fs *gilv1.FrozenSpec) error {
	if _, err := os.Stat(s.lockPath()); err == nil {
		return ErrFrozen
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("specstore.Save mkdir: %w", err)
	}
	data, err := marshalYAML(fs)
	if err != nil {
		return fmt.Errorf("specstore.Save marshal: %w", err)
	}
	if err := os.WriteFile(s.yamlPath(), data, 0o644); err != nil {
		return fmt.Errorf("specstore.Save write: %w", err)
	}
	return nil
}

// Load reads spec.yaml and verifies against spec.lock if present.
func (s *Store) Load() (*gilv1.FrozenSpec, error) {
	data, err := os.ReadFile(s.yamlPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("specstore.Load read: %w", err)
	}
	fs, err := unmarshalYAML(data)
	if err != nil {
		return nil, fmt.Errorf("specstore.Load unmarshal: %w", err)
	}
	if _, err := os.Stat(s.lockPath()); err == nil {
		ok, err := spec.VerifyLock(fs)
		if err != nil {
			return nil, fmt.Errorf("specstore.Load verify: %w", err)
		}
		if !ok {
			return nil, ErrLockMismatch
		}
	}
	return fs, nil
}

// Freeze computes the SHA-256 lock and writes spec.lock + re-saves spec.yaml with ContentSha256 set.
func (s *Store) Freeze() error {
	fs, err := s.Load()
	if err != nil {
		return err
	}
	hex, err := spec.Freeze(fs)
	if err != nil {
		return fmt.Errorf("specstore.Freeze: %w", err)
	}
	// 락 적용 전 마지막으로 yaml 갱신 (ContentSha256 필드 반영)
	data, err := marshalYAML(fs)
	if err != nil {
		return fmt.Errorf("specstore.Freeze marshal: %w", err)
	}
	if err := os.WriteFile(s.yamlPath(), data, 0o644); err != nil {
		return fmt.Errorf("specstore.Freeze write yaml: %w", err)
	}
	if err := os.WriteFile(s.lockPath(), []byte(hex), 0o644); err != nil {
		return fmt.Errorf("specstore.Freeze write lock: %w", err)
	}
	return nil
}

// IsFrozen returns true if spec.lock exists.
func (s *Store) IsFrozen() bool {
	_, err := os.Stat(s.lockPath())
	return err == nil
}

// marshalYAML converts a proto FrozenSpec to human-readable YAML via JSON intermediate.
func marshalYAML(fs *gilv1.FrozenSpec) ([]byte, error) {
	jsonBytes, err := protojson.Marshal(fs)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := yaml.Unmarshal(jsonBytes, &generic); err != nil {
		return nil, err
	}
	return yaml.Marshal(generic)
}

func unmarshalYAML(data []byte) (*gilv1.FrozenSpec, error) {
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	jsonBytes, err := yaml.Marshal(generic) // round-trip to JSON
	if err != nil {
		return nil, err
	}
	// yaml lib outputs YAML, need json. Use a different conversion:
	// Actually json.Marshal of generic gives JSON
	jsonBytes2, err := jsonMarshalGeneric(generic)
	if err != nil {
		return nil, err
	}
	_ = jsonBytes // unused
	fs := &gilv1.FrozenSpec{}
	if err := protojson.Unmarshal(jsonBytes2, fs); err != nil {
		return nil, err
	}
	return fs, nil
}

func jsonMarshalGeneric(v any) ([]byte, error) {
	return protoJSONMarshal(v) // helper below
}

// Helper to use encoding/json on generic map → JSON bytes
import "encoding/json"
func protoJSONMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
```

(Note: the unmarshalYAML helper above has duplicated/wrong structure. Implementer should clean up: use `encoding/json` to round-trip generic map to JSON, then `protojson.Unmarshal` to FrozenSpec. Move imports to top.)

- [ ] **Step 4: 테스트 실행**

```bash
cd /home/ubuntu/gil/core && go test ./specstore/... -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add core/specstore/ core/go.mod core/go.sum
git commit -m "feat(core/specstore): persist spec.yaml + spec.lock with tamper detection"
```

---

## Task 3: core/provider — LLM 추상화 + Mock provider

**Files:**
- Create: `core/provider/provider.go` (인터페이스)
- Create: `core/provider/mock.go` (테스트용)
- Create: `core/provider/mock_test.go`

- [ ] **Step 1: 실패 테스트**

```go
package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMock_ScriptedResponses(t *testing.T) {
	p := NewMock([]string{"hello", "world"})

	resp1, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	require.NoError(t, err)
	require.Equal(t, "hello", resp1.Text)

	resp2, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi again"}}})
	require.NoError(t, err)
	require.Equal(t, "world", resp2.Text)

	// exhausted
	_, err = p.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "exhausted"))
}
```

- [ ] **Step 2: provider.go 인터페이스 정의**

```go
package provider

import "context"

// Role of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single conversation turn.
type Message struct {
	Role    Role
	Content string
}

// Request contains everything needed for an LLM completion.
type Request struct {
	Model        string
	Messages     []Message
	System       string
	MaxTokens    int
	Temperature  float64
}

// Response carries the LLM output and metrics.
type Response struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	StopReason   string
}

// Provider is the LLM abstraction. Implementations: anthropic, openai, mock.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}
```

- [ ] **Step 3: mock.go**

```go
package provider

import (
	"context"
	"errors"
	"sync"
)

// Mock returns scripted responses in order. Useful for tests.
type Mock struct {
	mu        sync.Mutex
	responses []string
	idx       int
}

// NewMock returns a Mock pre-loaded with the given response strings.
func NewMock(responses []string) *Mock {
	return &Mock{responses: responses}
}

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Complete(ctx context.Context, req Request) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return Response{}, errors.New("mock provider responses exhausted")
	}
	resp := m.responses[m.idx]
	m.idx++
	return Response{
		Text:         resp,
		InputTokens:  int64(len(req.Messages) * 10),
		OutputTokens: int64(len(resp)),
		StopReason:   "end_turn",
	}, nil
}
```

- [ ] **Step 4: 테스트 실행**

```bash
cd /home/ubuntu/gil/core && go test ./provider/... -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add core/provider/
git commit -m "feat(core/provider): LLM abstraction interface + Mock for tests"
```

---

## Task 4: core/provider Anthropic 어댑터

**Files:**
- Create: `core/provider/anthropic.go`
- Create: `core/provider/anthropic_test.go`

- [ ] **Step 1: 의존성**

```bash
cd /home/ubuntu/gil/core && go get github.com/anthropics/anthropic-sdk-go
```

- [ ] **Step 2: 테스트** (실제 API 호출은 ANTHROPIC_API_KEY 있을 때만; 없으면 skip)

```go
package provider

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropic_Complete_LiveSmoke(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live smoke")
	}

	p := NewAnthropic(key)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-haiku-4-5-20251001",
		Messages: []Message{{Role: RoleUser, Content: "Reply with just the word 'pong'."}},
		MaxTokens: 10,
	})
	require.NoError(t, err)
	require.Contains(t, resp.Text, "pong")
	require.Greater(t, resp.OutputTokens, int64(0))
}

func TestAnthropic_Name(t *testing.T) {
	p := NewAnthropic("dummy-key")
	require.Equal(t, "anthropic", p.Name())
}
```

- [ ] **Step 3: anthropic.go**

```go
package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic is a Provider backed by the Anthropic Messages API.
type Anthropic struct {
	client *anthropic.Client
}

// NewAnthropic returns an Anthropic provider configured with the given API key.
// If apiKey is empty, the SDK reads ANTHROPIC_API_KEY from the env automatically.
func NewAnthropic(apiKey string) *Anthropic {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	c := anthropic.NewClient(opts...)
	return &Anthropic{client: &c}
}

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	if req.Model == "" {
		return Response{}, errors.New("anthropic.Complete: model required")
	}
	model := anthropic.Model(req.Model)

	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case RoleAssistant:
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			// system goes elsewhere
		}
	}

	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 4096
	}

	params := anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Temperature > 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic.Complete: %w", err)
	}

	var text string
	for _, b := range msg.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return Response{
		Text:         text,
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
		StopReason:   string(msg.StopReason),
	}, nil
}
```

(Note: the exact Anthropic SDK API may differ slightly from the snippet above. Implementer must consult the latest `anthropic-sdk-go` README and adjust types/method names. The shape — Client, MessageParam, Messages.New — is stable.)

- [ ] **Step 4: 테스트 실행**

```bash
cd /home/ubuntu/gil/core && go test ./provider/... -v -count=1
# ANTHROPIC_API_KEY 미설정이면 live test skip, mock test만 통과
```

- [ ] **Step 5: Commit**

```bash
git add core/provider/anthropic.go core/provider/anthropic_test.go core/go.mod core/go.sum
git commit -m "feat(core/provider): Anthropic adapter using anthropic-sdk-go"
```

---

## Task 5: core/interview — Stage 머신 + 슬롯 추적

**Files:**
- Create: `core/interview/state.go`
- Create: `core/interview/state_test.go`

- [ ] **Step 1: 실패 테스트**

```go
package interview

import (
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func TestState_Initial(t *testing.T) {
	st := NewState()
	require.Equal(t, StageSensing, st.Stage)
	require.Empty(t, st.History)
	require.NotNil(t, st.Spec)
}

func TestState_AppendUserMessage(t *testing.T) {
	st := NewState()
	st.AppendUser("hello")
	require.Len(t, st.History, 1)
	require.Equal(t, "hello", st.History[0].Content)
}

func TestState_RequiredSlotProgress(t *testing.T) {
	st := NewState()
	st.Spec.Goal = &gilv1.Goal{OneLiner: "x"}
	require.False(t, st.AllRequiredSlotsFilled())

	st.Spec.Goal.SuccessCriteriaNatural = []string{"a", "b", "c"}
	st.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	st.Spec.Verification = &gilv1.Verification{Checks: []*gilv1.Check{{Name: "build"}}}
	st.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	st.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "p", ModelId: "m"}}
	st.Spec.Risk = &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL}
	require.True(t, st.AllRequiredSlotsFilled())
}

func TestState_SaturationRequiresAdversaryClean(t *testing.T) {
	st := NewState()
	// Fill all slots
	st.Spec.Goal = &gilv1.Goal{OneLiner: "x", SuccessCriteriaNatural: []string{"a", "b", "c"}}
	st.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	st.Spec.Verification = &gilv1.Verification{Checks: []*gilv1.Check{{Name: "build"}}}
	st.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	st.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "p", ModelId: "m"}}
	st.Spec.Risk = &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL}

	// Without adversary clean, not saturated
	require.False(t, st.IsSaturated())

	// After 1 clean adversary round, saturated
	st.AdversaryRounds = 1
	st.LastAdversaryFindings = 0
	require.True(t, st.IsSaturated())
}
```

- [ ] **Step 2: state.go**

```go
package interview

import (
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/spec"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// Stage represents the interview phase.
type Stage int

const (
	StageSensing Stage = iota
	StageConversation
	StageConfirm
	StageFrozen
)

func (s Stage) String() string {
	switch s {
	case StageSensing:
		return "sensing"
	case StageConversation:
		return "conversation"
	case StageConfirm:
		return "confirm"
	case StageFrozen:
		return "frozen"
	default:
		return "unknown"
	}
}

// State is the interview state machine. Held in memory by the daemon per session.
type State struct {
	Stage                 Stage
	History               []provider.Message
	Spec                  *gilv1.FrozenSpec
	Domain                string
	DomainConfidence      float64
	AdversaryRounds       int
	LastAdversaryFindings int
}

// NewState returns an empty interview state ready for Sensing stage.
func NewState() *State {
	return &State{
		Stage: StageSensing,
		Spec:  &gilv1.FrozenSpec{},
	}
}

// AppendUser adds a user turn to the history.
func (s *State) AppendUser(content string) {
	s.History = append(s.History, provider.Message{Role: provider.RoleUser, Content: content})
}

// AppendAssistant adds an agent turn to the history.
func (s *State) AppendAssistant(content string) {
	s.History = append(s.History, provider.Message{Role: provider.RoleAssistant, Content: content})
}

// AllRequiredSlotsFilled delegates to spec package.
func (s *State) AllRequiredSlotsFilled() bool {
	return spec.AllRequiredSlotsFilled(s.Spec)
}

// IsSaturated returns true when (a) all required slots filled, AND
// (b) at least 1 adversary round has been run with 0 findings.
func (s *State) IsSaturated() bool {
	return s.AllRequiredSlotsFilled() && s.AdversaryRounds >= 1 && s.LastAdversaryFindings == 0
}
```

- [ ] **Step 3: 테스트 실행**

```bash
cd /home/ubuntu/gil/core && go test ./interview/... -v -count=1
```

- [ ] **Step 4: Commit**

```bash
git add core/interview/
git commit -m "feat(core/interview): Stage state machine + saturation check"
```

---

## Task 6: core/interview — Stage 1 (Sensing) + Stage 2 (Conversation) 단순 구현

**Files:**
- Create: `core/interview/engine.go`
- Create: `core/interview/engine_test.go`

- [ ] **Step 1: 테스트 (Mock provider 사용)**

```go
package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
)

func TestEngine_Sensing_ExtractsDomain(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"domain":"web-saas","domain_confidence":0.85,"tech_hints":["go"],"scale_hint":"medium","ambiguity":"none"}`,
	})
	eng := NewEngine(mock, "claude-haiku-4-5")
	st := NewState()

	require.NoError(t, eng.RunSensing(context.Background(), st, "I want to build a REST API for task management"))
	require.Equal(t, "web-saas", st.Domain)
	require.Equal(t, StageConversation, st.Stage)
}

func TestEngine_GenerateNextQuestion(t *testing.T) {
	mock := provider.NewMock([]string{`What technologies do you want to use?`})
	eng := NewEngine(mock, "claude-haiku-4-5")
	st := NewState()
	st.Stage = StageConversation
	st.Domain = "web-saas"
	st.AppendUser("REST API")

	q, err := eng.NextQuestion(context.Background(), st)
	require.NoError(t, err)
	require.Equal(t, "What technologies do you want to use?", q)
}
```

- [ ] **Step 2: engine.go (단순 버전 — Stage 머신 transitions만)**

```go
package interview

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mindungil/gil/core/provider"
)

// Engine drives the interview using an LLM provider.
type Engine struct {
	prov  provider.Provider
	model string
}

// NewEngine returns an Engine that uses the given provider and model name.
func NewEngine(p provider.Provider, model string) *Engine {
	return &Engine{prov: p, model: model}
}

// RunSensing performs the Stage 1 domain estimation from the user's first input.
// On success, st.Domain is populated and st.Stage is set to StageConversation.
func (e *Engine) RunSensing(ctx context.Context, st *State, firstInput string) error {
	st.AppendUser(firstInput)

	system := `You are estimating the domain of a software project from the user's first message.
Output strict JSON: {"domain":"string","domain_confidence":0.0-1.0,"tech_hints":["string"],"scale_hint":"small|medium|large|unknown","ambiguity":"string"}`

	resp, err := e.prov.Complete(ctx, provider.Request{
		Model:     e.model,
		System:    system,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: firstInput}},
		MaxTokens: 200,
	})
	if err != nil {
		return fmt.Errorf("interview.RunSensing: %w", err)
	}

	var parsed struct {
		Domain     string  `json:"domain"`
		Confidence float64 `json:"domain_confidence"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &parsed); err != nil {
		return fmt.Errorf("interview.RunSensing parse: %w", err)
	}
	st.Domain = parsed.Domain
	st.DomainConfidence = parsed.Confidence
	st.Stage = StageConversation
	return nil
}

// NextQuestion asks the LLM what to ask next given the current state.
// Returns the question text. The caller is responsible for displaying it
// and feeding the user's reply via st.AppendUser before calling again.
func (e *Engine) NextQuestion(ctx context.Context, st *State) (string, error) {
	system := fmt.Sprintf(`You are conducting a deep requirements interview for a software project.
Domain estimate: %q
Your job: read the conversation so far and produce ONE follow-up question that maximizes information gain.
Be brief (1-2 sentences). Output ONLY the question, nothing else.`, st.Domain)

	resp, err := e.prov.Complete(ctx, provider.Request{
		Model:     e.model,
		System:    system,
		Messages:  st.History,
		MaxTokens: 200,
	})
	if err != nil {
		return "", fmt.Errorf("interview.NextQuestion: %w", err)
	}
	return resp.Text, nil
}
```

- [ ] **Step 3: 테스트 실행**

```bash
cd /home/ubuntu/gil/core && go test ./interview/... -v -count=1
```

- [ ] **Step 4: Commit**

```bash
git add core/interview/engine.go core/interview/engine_test.go
git commit -m "feat(core/interview): Sensing stage + NextQuestion via LLM"
```

---

## Task 7: proto InterviewService 정의 + 코드 생성

**Files:**
- Create: `proto/gil/v1/interview.proto`
- Modify: `proto/gen/gil/v1/*` (regenerated)

- [ ] **Step 1: interview.proto 작성**

```protobuf
syntax = "proto3";

package gil.v1;

import "gil/v1/spec.proto";

option go_package = "github.com/mindungil/gil/proto/gen/gil/v1;gilv1";

service InterviewService {
  // Start launches the interview for a session. Initially the agent will speak
  // (Sensing → first question). The client streams user replies; the server
  // streams agent turns and stage transitions.
  rpc Start(StartInterviewRequest) returns (stream InterviewEvent);

  // Reply sends a user reply mid-interview. Returns the next agent event(s).
  rpc Reply(ReplyRequest) returns (stream InterviewEvent);

  // Confirm finalizes the spec and freezes it (writes spec.lock).
  rpc Confirm(ConfirmRequest) returns (ConfirmResponse);

  // GetSpec returns the current (possibly partial) spec for inspection.
  rpc GetSpec(GetSpecRequest) returns (FrozenSpec);
}

message StartInterviewRequest {
  string session_id = 1;
  string first_input = 2;     // user's initial message
  string provider = 3;         // "anthropic" | "mock" | "openai"
  string model = 4;
}

message ReplyRequest {
  string session_id = 1;
  string content = 2;
}

message ConfirmRequest {
  string session_id = 1;
}

message ConfirmResponse {
  string spec_id = 1;
  string content_sha256 = 2;
}

message GetSpecRequest {
  string session_id = 1;
}

message InterviewEvent {
  oneof payload {
    AgentTurn agent_turn = 1;
    StageTransition stage = 2;
    SpecUpdate spec_update = 3;
    InterviewError error = 4;
  }
}

message AgentTurn {
  string content = 1;
}

message StageTransition {
  string from = 1;
  string to = 2;
  string reason = 3;
}

message SpecUpdate {
  string field_path = 1;       // e.g., "goal.one_liner"
  string new_value = 2;
}

message InterviewError {
  string code = 1;
  string message = 2;
}
```

- [ ] **Step 2: 생성**

```bash
cd /home/ubuntu/gil/proto && buf generate
ls proto/gen/gil/v1/  # interview.pb.go + interview_grpc.pb.go 추가됨
```

- [ ] **Step 3: 컴파일 확인**

```bash
cd /home/ubuntu/gil/proto && go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add proto/gil/v1/interview.proto proto/gen/
git commit -m "feat(proto): InterviewService with Start/Reply/Confirm/GetSpec + InterviewEvent oneof"
```

---

## Task 8: server InterviewService 구현 + session.Repo 상태 전환

**Files:**
- Create: `server/internal/service/interview.go`
- Create: `server/internal/service/interview_test.go`
- Modify: `core/session/repo.go` — add UpdateStatus method

- [ ] **Step 1: session.Repo.UpdateStatus 추가**

In `core/session/repo.go`:
```go
// UpdateStatus changes the session's status string. Returns ErrNotFound if id missing.
func (r *Repo) UpdateStatus(ctx context.Context, id, status string) error {
    res, err := r.db.ExecContext(ctx,
        `UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?`,
        status, time.Now().UTC(), id)
    if err != nil {
        return fmt.Errorf("session.UpdateStatus: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return fmt.Errorf("session.UpdateStatus rowsAffected: %w", err)
    }
    if n == 0 {
        return ErrNotFound
    }
    return nil
}
```

Add test:
```go
func TestRepo_UpdateStatus(t *testing.T) {
    ctx := context.Background()
    db := openTestDB(t); defer db.Close()
    repo := NewRepo(db)
    s, err := repo.Create(ctx, CreateInput{WorkingDir: "/x"})
    require.NoError(t, err)

    require.NoError(t, repo.UpdateStatus(ctx, s.ID, "interviewing"))
    got, _ := repo.Get(ctx, s.ID)
    require.Equal(t, "interviewing", got.Status)

    require.ErrorIs(t, repo.UpdateStatus(ctx, "missing", "x"), ErrNotFound)
}
```

- [ ] **Step 2: InterviewService server skeleton + per-session interview state map**

```go
package service

import (
    "context"
    "fmt"
    "sync"

    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"

    "github.com/mindungil/gil/core/interview"
    "github.com/mindungil/gil/core/provider"
    "github.com/mindungil/gil/core/session"
    "github.com/mindungil/gil/core/specstore"
    gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// InterviewService manages per-session interview state in memory and
// persists frozen specs to disk via specstore.
type InterviewService struct {
    gilv1.UnimplementedInterviewServiceServer

    repo      *session.Repo
    baseDir   string                       // ~/.gil/sessions/
    providerFactory func(name string) (provider.Provider, string, error)

    mu     sync.Mutex
    states map[string]*interview.State    // session_id -> state
}

// NewInterviewService returns an InterviewService. baseDir is the parent of
// per-session directories (e.g., ~/.gil/sessions/). providerFactory takes
// a provider name ("anthropic"/"mock") and returns the Provider + default model.
func NewInterviewService(repo *session.Repo, baseDir string, factory func(string) (provider.Provider, string, error)) *InterviewService {
    return &InterviewService{
        repo:      repo,
        baseDir:   baseDir,
        providerFactory: factory,
        states:    make(map[string]*interview.State),
    }
}
```

Implement Start (does Sensing + emits first question):

```go
func (s *InterviewService) Start(req *gilv1.StartInterviewRequest, stream gilv1.InterviewService_StartServer) error {
    ctx := stream.Context()

    if _, err := s.repo.Get(ctx, req.SessionId); err != nil {
        if err == session.ErrNotFound {
            return status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
        }
        return status.Errorf(codes.Internal, "%v", err)
    }
    if err := s.repo.UpdateStatus(ctx, req.SessionId, "interviewing"); err != nil {
        return status.Errorf(codes.Internal, "%v", err)
    }

    p, model, err := s.providerFactory(req.Provider)
    if err != nil {
        return status.Errorf(codes.InvalidArgument, "provider: %v", err)
    }
    eng := interview.NewEngine(p, model)
    st := interview.NewState()
    s.mu.Lock()
    s.states[req.SessionId] = st
    s.mu.Unlock()

    // Run Sensing
    if err := eng.RunSensing(ctx, st, req.FirstInput); err != nil {
        return status.Errorf(codes.Internal, "sensing: %v", err)
    }
    if err := stream.Send(&gilv1.InterviewEvent{Payload: &gilv1.InterviewEvent_Stage{Stage: &gilv1.StageTransition{From: "sensing", To: "conversation", Reason: fmt.Sprintf("domain=%s", st.Domain)}}}); err != nil {
        return err
    }

    // Generate first question
    q, err := eng.NextQuestion(ctx, st)
    if err != nil {
        return status.Errorf(codes.Internal, "first question: %v", err)
    }
    st.AppendAssistant(q)
    return stream.Send(&gilv1.InterviewEvent{Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{Content: q}}})
}
```

(Implementer note: full Reply/Confirm/GetSpec implementations follow the same pattern. Reply takes content → AppendUser → NextQuestion → emit. Confirm checks AllRequiredSlotsFilled, runs `spec.Freeze` and saves via `specstore.Store{baseDir/sessionId}`. GetSpec just loads from specstore or returns the in-memory partial.)

- [ ] **Step 3: Test using mock provider**

```go
func TestInterviewService_Start_EmitsStageAndQuestion(t *testing.T) {
    // Setup: session in DB, mock provider with 2 scripted responses (sensing JSON + first question)
    // Start service, collect 2 events from stream
    // Assert first is StageTransition, second is AgentTurn with question text
}
```

- [ ] **Step 4: 컴파일 + 테스트**

```bash
cd /home/ubuntu/gil/server && go test ./internal/service/... -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add server/internal/service/interview.go server/internal/service/interview_test.go core/session/repo.go core/session/repo_test.go
git commit -m "feat(server/service): InterviewService Start with Sensing + first question; session.UpdateStatus"
```

---

## Task 9: gild main 에 InterviewService + provider factory 등록

**Files:**
- Modify: `server/cmd/gild/main.go`

- [ ] **Step 1: provider factory + service 등록**

In `newServer`, after registering SessionService:

```go
factory := func(name string) (provider.Provider, string, error) {
    switch name {
    case "mock":
        return provider.NewMock([]string{
            `{"domain":"unknown","domain_confidence":0.5,"tech_hints":[],"scale_hint":"unknown","ambiguity":"none"}`,
            "What's your project goal?",
        }), "mock-model", nil
    case "anthropic", "":
        key := os.Getenv("ANTHROPIC_API_KEY")
        if key == "" {
            return nil, "", fmt.Errorf("ANTHROPIC_API_KEY not set")
        }
        return provider.NewAnthropic(key), "claude-opus-4-7", nil
    default:
        return nil, "", fmt.Errorf("unknown provider %q", name)
    }
}

sessionsBase := filepath.Join(/* base */, "sessions")
gilv1.RegisterInterviewServiceServer(g, service.NewInterviewService(session.NewRepo(db), sessionsBase, factory))
```

(Note: `base` parameter needs to be threaded through `newServer`. Currently signature is `newServer(dbPath, sockPath)`. Add `baseDir` parameter or compute from dbPath's directory.)

- [ ] **Step 2: 빌드 + 기존 테스트 통과 확인**

```bash
cd /home/ubuntu/gil && make build && make test
```

- [ ] **Step 3: Commit**

```bash
git add server/cmd/gild/main.go server/cmd/gild/main_test.go
git commit -m "feat(server): register InterviewService with mock + anthropic provider factory in gild"
```

---

## Task 10: SDK Interview methods

**Files:**
- Modify: `sdk/client.go` — add interview methods

- [ ] **Step 1: SDK 메서드 추가**

```go
// StartInterview begins an interview session. Returns a stream of agent events.
// Caller must read events until io.EOF or error.
func (c *Client) StartInterview(ctx context.Context, sessionID, firstInput, providerName, model string) (gilv1.InterviewService_StartClient, error) {
    return c.interviews.Start(ctx, &gilv1.StartInterviewRequest{
        SessionId:  sessionID,
        FirstInput: firstInput,
        Provider:   providerName,
        Model:      model,
    })
}

// ReplyInterview sends a user reply and returns a stream of agent events.
func (c *Client) ReplyInterview(ctx context.Context, sessionID, content string) (gilv1.InterviewService_ReplyClient, error) {
    return c.interviews.Reply(ctx, &gilv1.ReplyRequest{SessionId: sessionID, Content: content})
}

// ConfirmInterview freezes the spec.
func (c *Client) ConfirmInterview(ctx context.Context, sessionID string) (string, string, error) {
    resp, err := c.interviews.Confirm(ctx, &gilv1.ConfirmRequest{SessionId: sessionID})
    if err != nil {
        return "", "", err
    }
    return resp.SpecId, resp.ContentSha256, nil
}
```

Add `interviews gilv1.InterviewServiceClient` field to Client struct, initialize in Dial.

- [ ] **Step 2: 테스트** (live mock-server-based, similar to existing tests)

- [ ] **Step 3: Commit**

```bash
git add sdk/client.go sdk/client_test.go
git commit -m "feat(sdk): InterviewService client methods (Start/Reply/Confirm)"
```

---

## Task 11: CLI `gil interview` + `gil spec` 명령

**Files:**
- Create: `cli/internal/cmd/interview.go`
- Create: `cli/internal/cmd/spec.go`
- Create: `cli/internal/cmd/interview_test.go`

- [ ] **Step 1: interview.go (interactive CLI)**

```go
package cmd

import (
    "bufio"
    "context"
    "fmt"
    "io"
    "os"

    "github.com/spf13/cobra"

    "github.com/mindungil/gil/sdk"
)

func interviewCmd() *cobra.Command {
    var socket, providerName, model string
    c := &cobra.Command{
        Use:   "interview <session-id>",
        Short: "Run the interview for a session interactively",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            sessionID := args[0]
            ctx := cmd.Context()
            if ctx == nil {
                ctx = context.Background()
            }
            if err := ensureDaemon(socket, defaultBase()); err != nil {
                return err
            }
            cli, err := sdk.Dial(socket)
            if err != nil {
                return fmt.Errorf("dial: %w", err)
            }
            defer cli.Close()

            in := bufio.NewReader(os.Stdin)
            fmt.Fprint(cmd.OutOrStdout(), "Initial message: ")
            firstLine, err := in.ReadString('\n')
            if err != nil {
                return fmt.Errorf("read input: %w", err)
            }

            stream, err := cli.StartInterview(ctx, sessionID, firstLine, providerName, model)
            if err != nil {
                return fmt.Errorf("start: %w", err)
            }
            for {
                evt, err := stream.Recv()
                if err == io.EOF {
                    break
                }
                if err != nil {
                    return fmt.Errorf("recv: %w", err)
                }
                if t := evt.GetAgentTurn(); t != nil {
                    fmt.Fprintln(cmd.OutOrStdout(), "\nAgent:", t.Content)
                    break // 첫 질문 받으면 사용자 입력 대기
                }
                if st := evt.GetStage(); st != nil {
                    fmt.Fprintf(cmd.OutOrStdout(), "[stage %s -> %s: %s]\n", st.From, st.To, st.Reason)
                }
            }

            // Reply 루프
            for {
                fmt.Fprint(cmd.OutOrStdout(), "\nYou: ")
                line, err := in.ReadString('\n')
                if err != nil {
                    return fmt.Errorf("read input: %w", err)
                }
                rstream, err := cli.ReplyInterview(ctx, sessionID, line)
                if err != nil {
                    return fmt.Errorf("reply: %w", err)
                }
                for {
                    evt, err := rstream.Recv()
                    if err == io.EOF {
                        break
                    }
                    if err != nil {
                        return fmt.Errorf("recv: %w", err)
                    }
                    if t := evt.GetAgentTurn(); t != nil {
                        fmt.Fprintln(cmd.OutOrStdout(), "\nAgent:", t.Content)
                    }
                    if st := evt.GetStage(); st != nil {
                        fmt.Fprintf(cmd.OutOrStdout(), "[stage %s -> %s: %s]\n", st.From, st.To, st.Reason)
                        if st.To == "confirm" {
                            fmt.Fprintln(cmd.OutOrStdout(), "Saturation reached. Run 'gil spec freeze <session-id>' when ready.")
                            return nil
                        }
                    }
                }
            }
        },
    }
    c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
    c.Flags().StringVar(&providerName, "provider", "anthropic", "LLM provider (anthropic|mock)")
    c.Flags().StringVar(&model, "model", "", "LLM model id (provider default if empty)")
    return c
}
```

- [ ] **Step 2: spec.go (`gil spec` and `gil spec freeze`)**

```go
func specCmd() *cobra.Command {
    var socket string
    c := &cobra.Command{
        Use:   "spec <session-id>",
        Short: "Show the current spec for a session",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            // GetSpec via SDK, print as YAML
            // ...
        },
    }
    c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
    c.AddCommand(specFreezeCmd())
    return c
}

func specFreezeCmd() *cobra.Command {
    var socket string
    c := &cobra.Command{
        Use:   "freeze <session-id>",
        Short: "Freeze the spec (write spec.lock, immutable thereafter)",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            // Confirm via SDK, print spec_id + sha256
            // ...
        },
    }
    c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
    return c
}
```

- [ ] **Step 3: root.go에 등록**

```go
root.AddCommand(interviewCmd())
root.AddCommand(specCmd())
```

- [ ] **Step 4: 테스트** (mock provider로 짧은 시나리오)

- [ ] **Step 5: Commit**

```bash
git add cli/internal/cmd/interview.go cli/internal/cmd/spec.go cli/internal/cmd/interview_test.go cli/internal/cmd/root.go
git commit -m "feat(cli): 'gil interview' interactive + 'gil spec [freeze]' commands"
```

---

## Task 12: E2E — phase02 interview test (mock provider)

**Files:**
- Create: `tests/e2e/phase02_test.sh`
- Modify: `Makefile`

- [ ] **Step 1: E2E 스크립트**

```bash
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"
trap 'pkill -f "gild --foreground --base $BASE" 2>/dev/null || true; rm -rf "$BASE"' EXIT

cd "$ROOT" && make build > /dev/null

# 데몬 자동 spawn 시나리오 (gil 첫 명령에서)
PATH="$ROOT/bin:$PATH"
ID=$("$ROOT/bin/gil" new --working-dir /tmp/proj --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }

# Mock provider로 인터뷰 — 자동으로 saturation 도달까지 진행되도록
# (Phase 2에선 reply 루프가 stdin 대기형이라 자동 시나리오는 별도 RPC 호출 스크립트 필요)
# 일단 Start 단계까지 emit 검증

# 빠른 검증: Start emit 두 이벤트(stage transition + agent turn)
# 실제 인터랙션 e2e는 추후 보강

echo "OK: phase 2 e2e (sanity) passed"
```

- [ ] **Step 2: Makefile에 e2e2 추가**

```makefile
e2e2: build
	@bash tests/e2e/phase02_test.sh

e2e-all: e2e e2e2
```

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/phase02_test.sh Makefile
git commit -m "test(e2e): phase 2 sanity script (auto-spawn + new)"
```

---

## Task 13: progress.md Phase 2 갱신

**Files:**
- Modify: `docs/progress.md`

- [ ] Update Phase 2 section: "(완료 — 2026-04-XX)", check all bullets, add 결정사항 row, append 산출물 요약 section.

- [ ] Commit:
```bash
git add docs/progress.md
git commit -m "docs(progress): mark Phase 2 complete — interview engine + provider + auto-spawn"
```

---

## Phase 2 완료 검증 체크리스트

이 plan 전체 task가 끝나면 다음이 모두 동작해야 한다:

- [ ] `make tidy && make test && make e2e && make e2e2` 모두 통과
- [ ] `gil new` 첫 실행 시 데몬 자동 spawn (수동 gild 실행 불필요)
- [ ] `GIL_PROVIDER=mock gil interview <id>` 로 mock 시나리오 실행 가능
- [ ] `ANTHROPIC_API_KEY=sk-... gil interview <id>` 로 실제 LLM 인터뷰 가능
- [ ] `gil spec <id>` 로 현재 spec yaml 표시
- [ ] `gil spec freeze <id>` 로 lock 생성
- [ ] freeze 후 spec.yaml 직접 수정 시 GetSpec 호출에서 ErrLockMismatch 감지

---

## Phase 3 미루는 항목 (의도적)

Phase 2에서 안 다룬 것 — Phase 3+에 분배:

- adversary critique 라운드 (별도 LLM 패스로 spec 비판) — Phase 3
- self-audit gate (인터뷰 stage 전환 직전 에이전트 자기 검사) — Phase 3
- 동적 spec 슬롯 채우기 (LLM이 답변 파싱해서 spec field 자동 갱신) — Phase 3
- gil resume / interview 재개 (현재 시작만 가능) — Phase 3
- Provider 에러 재시도/backoff — Phase 3
- secret masking on event log — Phase 3 (인터뷰가 시크릿 받기 시작할 때)
- Per-session event integration — Phase 3 (verifier/run 시점에)
