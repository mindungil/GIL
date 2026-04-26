# Phase 1 — 코어 골격 (Core Skeleton)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** gil의 데몬-클라이언트 토대를 만든다. `gil daemon`으로 gRPC 서버를 띄우고, `gil new`로 세션을 만들어 SQLite에 영속화하고, `gil status`로 조회하고, 이벤트 스트림을 append-only로 기록/구독한다. frozen spec 직렬화와 SHA-256 lock도 포함.

**Architecture:** Go 1.22+ workspace, 8 모듈 (core/runtime/proto/server/cli/tui/sdk/mcp). gRPC over HTTP/2 (UDS 기본, TCP 옵션). 모든 도메인 로직은 `core/`에, gRPC 서비스 구현은 `server/`에. 클라이언트(`cli`, `tui`)는 `sdk/`(gRPC client wrapper)를 통해서만 데몬과 통신.

**Tech Stack:**
- Go 1.22+ (`go.work`)
- protobuf: `buf` CLI + `google.golang.org/protobuf` + `google.golang.org/grpc`
- SQLite: `modernc.org/sqlite` (CGO 불필요)
- ULID: `github.com/oklog/ulid/v2`
- CLI: `github.com/spf13/cobra`
- Tests: 표준 `testing` + `github.com/stretchr/testify`

**산출물 검증** (Phase 1 종료 시점):
```bash
gil daemon --foreground  &     # 1초 안에 ready
gil new                        # session ID 출력
gil status                     # 세션 1개 표시
# 모든 단위 테스트 + 통합 테스트 통과
```

---

## Task 1: Go workspace 부트스트랩

**Files:**
- Create: `go.work`
- Create: `Makefile`

- [ ] **Step 1: `go.work` 작성**

```bash
cd /home/ubuntu/gil
cat > go.work <<'EOF'
go 1.22

use (
    ./core
    ./runtime
    ./proto
    ./server
    ./cli
    ./tui
    ./sdk
    ./mcp
)
EOF
```

- [ ] **Step 2: 모듈 디렉토리들 생성**

```bash
cd /home/ubuntu/gil
for m in core runtime proto server cli tui sdk mcp; do
  mkdir -p "$m"
done
```

- [ ] **Step 3: 각 모듈 `go mod init` 실행**

```bash
cd /home/ubuntu/gil
for m in core runtime proto server cli tui sdk mcp; do
  (cd "$m" && go mod init "github.com/jedutools/gil/$m")
done
```

- [ ] **Step 4: `Makefile` 작성** (자주 쓰는 명령 모음)

```makefile
.PHONY: tidy test gen build clean

tidy:
	@for m in core runtime proto server cli tui sdk mcp; do \
		(cd $$m && go mod tidy) || exit 1; \
	done

test:
	@for m in core runtime proto server cli tui sdk mcp; do \
		(cd $$m && go test ./...) || exit 1; \
	done

gen:
	@cd proto && buf generate

build:
	@mkdir -p bin
	@cd cli && go build -o ../bin/gil ./cmd/gil
	@cd server && go build -o ../bin/gild ./cmd/gild

clean:
	@rm -rf bin
```

- [ ] **Step 5: 검증**

```bash
cd /home/ubuntu/gil && go work sync && go env GOWORK
# Expected: /home/ubuntu/gil/go.work
```

- [ ] **Step 6: 커밋**

```bash
git add go.work Makefile core/go.mod runtime/go.mod proto/go.mod server/go.mod cli/go.mod tui/go.mod sdk/go.mod mcp/go.mod
git commit -m "chore: bootstrap Go workspace with 8 modules"
```

---

## Task 2: protobuf 도구 셋업 + 빈 빌드 검증

**Files:**
- Create: `proto/buf.yaml`
- Create: `proto/buf.gen.yaml`

- [ ] **Step 1: `buf` CLI 설치 확인 (없으면 설치)**

```bash
which buf || (curl -sSL "https://github.com/bufbuild/buf/releases/latest/download/buf-$(uname -s)-$(uname -m)" -o /tmp/buf && chmod +x /tmp/buf && sudo mv /tmp/buf /usr/local/bin/buf)
buf --version
# Expected: 1.x.x output
```

- [ ] **Step 2: `proto/buf.yaml` 작성**

```yaml
version: v2
modules:
  - path: gil
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

- [ ] **Step 3: `proto/buf.gen.yaml` 작성**

```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: gen
    opt:
      - paths=source_relative
      - require_unimplemented_servers=false
```

- [ ] **Step 4: `proto/gen/.gitkeep` 생성** (디렉토리 추적용)

```bash
mkdir -p proto/gen && touch proto/gen/.gitkeep
```

- [ ] **Step 5: `proto/go.mod` 의존성 추가**

```bash
cd /home/ubuntu/gil/proto
go get google.golang.org/protobuf google.golang.org/grpc
```

- [ ] **Step 6: 커밋**

```bash
git add proto/buf.yaml proto/buf.gen.yaml proto/gen/.gitkeep proto/go.mod proto/go.sum
git commit -m "chore: configure buf for protobuf+gRPC code generation"
```

---

## Task 3: spec.proto, event.proto, session.proto 정의

**Files:**
- Create: `proto/gil/v1/spec.proto`
- Create: `proto/gil/v1/event.proto`
- Create: `proto/gil/v1/session.proto`

- [ ] **Step 1: `proto/gil/v1/spec.proto` 작성**

```protobuf
syntax = "proto3";

package gil.v1;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/jedutools/gil/proto/gen/gil/v1;gilv1";

message FrozenSpec {
  string spec_id = 1;
  string session_id = 2;
  google.protobuf.Timestamp frozen_at = 3;
  string content_sha256 = 4;

  Goal goal = 10;
  Constraints constraints = 11;
  Verification verification = 12;
  Workspace workspace = 13;
  ModelConfig models = 14;
  Budget budget = 15;
  Tools tools = 16;
  RiskProfile risk = 17;
  repeated Microagent microagents = 18;
  Setup setup = 20;
}

message Goal {
  string one_liner = 1;
  string detailed = 2;
  repeated string success_criteria_natural = 3;
  repeated string non_goals = 4;
}

message Constraints {
  repeated string tech_stack = 1;
  repeated string forbidden = 2;
  string license = 3;
  string code_style = 4;
}

message Verification {
  repeated Check checks = 1;
  int32 max_retries_per_check = 2;
  int64 per_check_timeout_seconds = 3;
}

message Check {
  string name = 1;
  CheckKind kind = 2;
  string command = 3;
  int32 expected_exit_code = 4;
}

enum CheckKind {
  CHECK_KIND_UNSPECIFIED = 0;
  SHELL = 1;
  FILE_EXISTS = 2;
  HTTP = 3;
  REGEX_MATCH = 4;
  CUSTOM_SCRIPT = 5;
}

message Workspace {
  WorkspaceBackend backend = 1;
  string path = 2;
}

enum WorkspaceBackend {
  BACKEND_UNSPECIFIED = 0;
  LOCAL_NATIVE = 1;
  LOCAL_SANDBOX = 2;
  DOCKER = 3;
  SSH = 4;
  VM = 5;
}

message ModelConfig {
  ModelChoice main = 1;
  ModelChoice weak = 2;
  ModelChoice editor = 3;
  ModelChoice adversary = 4;
  ModelChoice interview = 5;
}

message ModelChoice {
  string provider = 1;
  string model_id = 2;
}

message Budget {
  int64 max_total_tokens = 1;
  double max_total_cost_usd = 2;
  int64 max_wall_clock_seconds = 3;
  int32 max_iterations = 4;
  int32 max_subagent_depth = 5;
  bool grace_call_on_exhaustion = 6;
}

message Tools {
  bool bash = 1;
  bool file_ops = 2;
  bool web_search = 3;
  bool web_fetch = 4;
  bool repomap = 5;
  bool exec_code = 6;
  repeated string mcp_servers = 10;
}

message RiskProfile {
  AutonomyDial autonomy = 1;
  bool adversary_reviewer_enabled = 2;
  bool stuck_detector_enabled = 3;
}

enum AutonomyDial {
  AUTONOMY_UNSPECIFIED = 0;
  PLAN_ONLY = 1;
  ASK_PER_ACTION = 2;
  ASK_DESTRUCTIVE_ONLY = 3;
  FULL = 4;
}

message Microagent {
  string name = 1;
  string trigger_type = 2;
  repeated string keywords = 3;
  string content = 4;
}

message Setup {
  repeated string commands = 1;
  int64 timeout_seconds = 2;
  bool fail_fast = 3;
}
```

- [ ] **Step 2: `proto/gil/v1/event.proto` 작성**

```protobuf
syntax = "proto3";

package gil.v1;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/jedutools/gil/proto/gen/gil/v1;gilv1";

message Event {
  int64 id = 1;
  google.protobuf.Timestamp timestamp = 2;
  EventSource source = 3;
  EventKind kind = 4;
  string type = 5;
  bytes data_json = 6;     // 자유형 JSON payload
  int64 cause = 7;          // 선행 이벤트 ID (없으면 0)
  EventMetrics metrics = 8;
}

enum EventSource {
  SOURCE_UNSPECIFIED = 0;
  AGENT = 1;
  USER = 2;
  ENVIRONMENT = 3;
  SYSTEM = 4;
}

enum EventKind {
  KIND_UNSPECIFIED = 0;
  ACTION = 1;
  OBSERVATION = 2;
  NOTE = 3;
}

message EventMetrics {
  int64 tokens = 1;
  double cost_usd = 2;
  int64 latency_ms = 3;
}
```

- [ ] **Step 3: `proto/gil/v1/session.proto` 작성**

```protobuf
syntax = "proto3";

package gil.v1;

import "google/protobuf/timestamp.proto";
import "gil/v1/spec.proto";

option go_package = "github.com/jedutools/gil/proto/gen/gil/v1;gilv1";

message Session {
  string id = 1;
  SessionStatus status = 2;
  google.protobuf.Timestamp created_at = 3;
  google.protobuf.Timestamp updated_at = 4;
  string spec_id = 5;          // freeze 전이면 빈 문자열
  string working_dir = 6;
  string goal_hint = 7;
  int64 total_tokens = 8;
  double total_cost_usd = 9;
}

enum SessionStatus {
  SESSION_STATUS_UNSPECIFIED = 0;
  CREATED = 1;
  INTERVIEWING = 2;
  FROZEN = 3;
  RUNNING = 4;
  AUTO_PAUSED = 5;
  DONE = 6;
  STOPPED = 7;
}

service SessionService {
  rpc Create(CreateRequest) returns (Session);
  rpc Get(GetRequest) returns (Session);
  rpc List(ListRequest) returns (ListResponse);
}

message CreateRequest {
  string working_dir = 1;
  string goal_hint = 2;
}

message GetRequest {
  string id = 1;
}

message ListRequest {
  int32 limit = 1;
  string status_filter = 2;
}

message ListResponse {
  repeated Session sessions = 1;
}
```

- [ ] **Step 4: `buf generate` 실행**

```bash
cd /home/ubuntu/gil/proto && buf generate
```

- [ ] **Step 5: 생성된 파일 확인**

```bash
ls /home/ubuntu/gil/proto/gen/gil/v1/
# Expected: event.pb.go  session.pb.go  session_grpc.pb.go  spec.pb.go
```

- [ ] **Step 6: 컴파일 확인**

```bash
cd /home/ubuntu/gil/proto && go build ./...
# Expected: 에러 없음
```

- [ ] **Step 7: 커밋**

```bash
git add proto/
git commit -m "feat(proto): define spec/event/session proto + generate Go code"
```

---

## Task 4: core/event Stream 기본 (in-memory + auto-id)

**Files:**
- Create: `core/event/event.go`
- Create: `core/event/stream.go`
- Create: `core/event/stream_test.go`

- [ ] **Step 1: 의존성 추가 (testify)**

```bash
cd /home/ubuntu/gil/core
go get github.com/stretchr/testify/require github.com/oklog/ulid/v2
```

- [ ] **Step 2: 실패하는 테스트 작성** — `core/event/stream_test.go`

```go
package event

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStream_Append_AssignsIDs(t *testing.T) {
	s := NewStream()

	id1, err := s.Append(Event{Type: "test", Timestamp: time.Now()})
	require.NoError(t, err)
	require.Equal(t, int64(1), id1)

	id2, err := s.Append(Event{Type: "test", Timestamp: time.Now()})
	require.NoError(t, err)
	require.Equal(t, int64(2), id2)
}

func TestStream_Append_DuplicateIDFails(t *testing.T) {
	s := NewStream()

	_, err := s.Append(Event{ID: 5, Type: "test"})
	require.Error(t, err)
}
```

- [ ] **Step 3: 테스트 실행 — 실패 확인**

```bash
cd /home/ubuntu/gil/core && go test ./event/...
# Expected: FAIL (package doesn't exist)
```

- [ ] **Step 4: `core/event/event.go` 최소 타입 작성**

```go
package event

import "time"

// Source는 이벤트 출처를 나타낸다.
type Source int

const (
	SourceUnspecified Source = iota
	SourceAgent
	SourceUser
	SourceEnvironment
	SourceSystem
)

// Kind는 이벤트 종류를 나타낸다.
type Kind int

const (
	KindUnspecified Kind = iota
	KindAction
	KindObservation
	KindNote
)

// Event는 단일 이벤트의 메모리 표현이다.
type Event struct {
	ID        int64
	Timestamp time.Time
	Source    Source
	Kind      Kind
	Type      string
	Data      []byte // JSON
	Cause     int64
	Metrics   Metrics
}

type Metrics struct {
	Tokens    int64
	CostUSD   float64
	LatencyMs int64
}
```

- [ ] **Step 5: `core/event/stream.go` 최소 구현**

```go
package event

import (
	"errors"
	"sync"
)

var ErrDuplicateID = errors.New("event already has ID assigned")

type Stream struct {
	mu     sync.Mutex
	events []Event
	curID  int64
}

func NewStream() *Stream {
	return &Stream{}
}

func (s *Stream) Append(e Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e.ID != 0 {
		return 0, ErrDuplicateID
	}
	s.curID++
	e.ID = s.curID
	s.events = append(s.events, e)
	return e.ID, nil
}

// Len returns the number of events currently in the stream.
func (s *Stream) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}
```

- [ ] **Step 6: 테스트 실행 — 통과 확인**

```bash
cd /home/ubuntu/gil/core && go test ./event/... -v
# Expected: PASS for both tests
```

- [ ] **Step 7: 커밋**

```bash
git add core/go.mod core/go.sum core/event/
git commit -m "feat(core/event): add Stream with auto-ID assignment + duplicate guard"
```

---

## Task 5: core/event Subscribe (pub/sub via channels)

**Files:**
- Modify: `core/event/stream.go`
- Modify: `core/event/stream_test.go`

- [ ] **Step 1: 실패 테스트 추가**

```go
// core/event/stream_test.go 에 추가
func TestStream_Subscribe_ReceivesAppendedEvents(t *testing.T) {
	s := NewStream()
	sub := s.Subscribe(10)
	defer sub.Close()

	go func() {
		s.Append(Event{Type: "first"})
		s.Append(Event{Type: "second"})
	}()

	got1 := <-sub.Events()
	got2 := <-sub.Events()
	require.Equal(t, "first", got1.Type)
	require.Equal(t, "second", got2.Type)
}

func TestStream_Subscribe_MultipleSubscribers(t *testing.T) {
	s := NewStream()
	sub1 := s.Subscribe(5)
	defer sub1.Close()
	sub2 := s.Subscribe(5)
	defer sub2.Close()

	s.Append(Event{Type: "broadcast"})

	r1 := <-sub1.Events()
	r2 := <-sub2.Events()
	require.Equal(t, "broadcast", r1.Type)
	require.Equal(t, "broadcast", r2.Type)
}
```

- [ ] **Step 2: 테스트 실행 — 실패 확인**

```bash
cd /home/ubuntu/gil/core && go test ./event/... -run Subscribe
# Expected: FAIL (Subscribe not defined)
```

- [ ] **Step 3: `Subscription` 타입 + `Subscribe` 메서드 추가**

```go
// core/event/stream.go 에 추가

// Subscription은 stream 이벤트를 받는 핸들이다.
type Subscription struct {
	ch     chan Event
	closed bool
	mu     sync.Mutex
	stream *Stream
}

func (sub *Subscription) Events() <-chan Event {
	return sub.ch
}

func (sub *Subscription) Close() {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	close(sub.ch)
	sub.stream.removeSubscription(sub)
}

// Stream 구조체에 subs 필드 추가
// type Stream struct {
//   mu     sync.Mutex
//   events []Event
//   curID  int64
//   subs   []*Subscription
// }

func (s *Stream) Subscribe(buffer int) *Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &Subscription{
		ch:     make(chan Event, buffer),
		stream: s,
	}
	s.subs = append(s.subs, sub)
	return sub
}

func (s *Stream) removeSubscription(target *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sub := range s.subs {
		if sub == target {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			return
		}
	}
}

// Append를 수정해서 fan-out
// (기존 Append 메서드 끝에 다음 추가)
//   for _, sub := range s.subs {
//     select {
//     case sub.ch <- e:
//     default: // slow consumer는 drop
//     }
//   }
```

- [ ] **Step 4: 전체 stream.go 최종 버전 — 한 번에 덮어쓰기**

```go
package event

import (
	"errors"
	"sync"
)

var ErrDuplicateID = errors.New("event already has ID assigned")

type Stream struct {
	mu     sync.Mutex
	events []Event
	curID  int64
	subs   []*Subscription
}

func NewStream() *Stream {
	return &Stream{}
}

func (s *Stream) Append(e Event) (int64, error) {
	s.mu.Lock()
	if e.ID != 0 {
		s.mu.Unlock()
		return 0, ErrDuplicateID
	}
	s.curID++
	e.ID = s.curID
	s.events = append(s.events, e)
	subs := append([]*Subscription(nil), s.subs...)
	s.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub.ch <- e:
		default:
			// slow consumer drop. TODO Phase 2: dead-letter log
		}
	}
	return e.ID, nil
}

func (s *Stream) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

type Subscription struct {
	ch     chan Event
	mu     sync.Mutex
	closed bool
	stream *Stream
}

func (sub *Subscription) Events() <-chan Event {
	return sub.ch
}

func (sub *Subscription) Close() {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	close(sub.ch)
	sub.stream.removeSubscription(sub)
}

func (s *Stream) Subscribe(buffer int) *Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &Subscription{
		ch:     make(chan Event, buffer),
		stream: s,
	}
	s.subs = append(s.subs, sub)
	return sub
}

func (s *Stream) removeSubscription(target *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sub := range s.subs {
		if sub == target {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			return
		}
	}
}
```

- [ ] **Step 5: 테스트 실행 — 통과 확인**

```bash
cd /home/ubuntu/gil/core && go test ./event/... -v
# Expected: PASS for all 4 tests
```

- [ ] **Step 6: 커밋**

```bash
git add core/event/
git commit -m "feat(core/event): pub/sub via channels with multi-subscriber fan-out"
```

---

## Task 6: core/event JSONL 영속화

**Files:**
- Create: `core/event/persist.go`
- Create: `core/event/persist_test.go`

- [ ] **Step 1: 실패 테스트 작성**

```go
package event

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPersister_AppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersister(dir)
	require.NoError(t, err)
	defer p.Close()

	e1 := Event{ID: 1, Type: "first", Timestamp: time.Unix(1700000000, 0)}
	e2 := Event{ID: 2, Type: "second", Timestamp: time.Unix(1700000001, 0)}

	require.NoError(t, p.Write(e1))
	require.NoError(t, p.Write(e2))
	require.NoError(t, p.Sync())

	loaded, err := LoadAll(filepath.Join(dir, "events.jsonl"))
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	require.Equal(t, "first", loaded[0].Type)
	require.Equal(t, int64(2), loaded[1].ID)
}
```

- [ ] **Step 2: 테스트 실행 — 실패 확인**

```bash
cd /home/ubuntu/gil/core && go test ./event/... -run Persister
# Expected: FAIL (Persister not defined)
```

- [ ] **Step 3: `core/event/persist.go` 작성**

```go
package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Persister는 이벤트를 JSONL 파일에 append-only로 기록한다.
type Persister struct {
	mu   sync.Mutex
	file *os.File
	w    *bufio.Writer
}

// jsonEvent는 JSON 직렬화 형식. proto 미사용 (단순화 + 사람 읽기 좋음)
type jsonEvent struct {
	ID        int64   `json:"id"`
	Timestamp string  `json:"timestamp"`
	Source    int     `json:"source"`
	Kind      int     `json:"kind"`
	Type      string  `json:"type"`
	Data      string  `json:"data,omitempty"`
	Cause     int64   `json:"cause,omitempty"`
	Tokens    int64   `json:"tokens,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	LatencyMs int64   `json:"latency_ms,omitempty"`
}

func NewPersister(dir string) (*Persister, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Persister{file: f, w: bufio.NewWriter(f)}, nil
}

func (p *Persister) Write(e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	je := jsonEvent{
		ID:        e.ID,
		Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
		Source:    int(e.Source),
		Kind:      int(e.Kind),
		Type:      e.Type,
		Data:      string(e.Data),
		Cause:     e.Cause,
		Tokens:    e.Metrics.Tokens,
		CostUSD:   e.Metrics.CostUSD,
		LatencyMs: e.Metrics.LatencyMs,
	}
	b, err := json.Marshal(je)
	if err != nil {
		return err
	}
	if _, err := p.w.Write(b); err != nil {
		return err
	}
	return p.w.WriteByte('\n')
}

func (p *Persister) Sync() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.w.Flush(); err != nil {
		return err
	}
	return p.file.Sync()
}

func (p *Persister) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w != nil {
		_ = p.w.Flush()
	}
	return p.file.Close()
}

// LoadAll reads every event from a JSONL file.
func LoadAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Event
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var je jsonEvent
			if jerr := json.Unmarshal(line, &je); jerr != nil {
				return nil, jerr
			}
			ts, _ := time.Parse(time.RFC3339Nano, je.Timestamp)
			out = append(out, Event{
				ID:        je.ID,
				Timestamp: ts,
				Source:    Source(je.Source),
				Kind:      Kind(je.Kind),
				Type:      je.Type,
				Data:      []byte(je.Data),
				Cause:     je.Cause,
				Metrics: Metrics{
					Tokens:    je.Tokens,
					CostUSD:   je.CostUSD,
					LatencyMs: je.LatencyMs,
				},
			})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
	}
}
```

- [ ] **Step 4: 테스트 실행 — 통과 확인**

```bash
cd /home/ubuntu/gil/core && go test ./event/... -v
# Expected: PASS (all 5 tests)
```

- [ ] **Step 5: 커밋**

```bash
git add core/event/persist.go core/event/persist_test.go
git commit -m "feat(core/event): JSONL persistence with sync/load"
```

---

## Task 7: core/spec FrozenSpec + SHA-256 lock

**Files:**
- Create: `core/spec/spec.go`
- Create: `core/spec/spec_test.go`

- [ ] **Step 1: 의존성 — proto 모듈 참조**

```bash
cd /home/ubuntu/gil/core
go get github.com/jedutools/gil/proto
# (workspace 모드라 로컬 디렉토리에서 resolve됨)
```

- [ ] **Step 2: 실패 테스트 작성**

```go
package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func validSpec() *gilv1.FrozenSpec {
	return &gilv1.FrozenSpec{
		SpecId:    "01HXY1",
		SessionId: "01HSESS",
		Goal: &gilv1.Goal{
			OneLiner:               "build a CLI",
			SuccessCriteriaNatural: []string{"a", "b", "c"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"go"}},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{
			{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "go build"},
		}},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
	}
}

func TestSpec_Freeze_ProducesLock(t *testing.T) {
	fs := validSpec()

	lock, err := Freeze(fs)
	require.NoError(t, err)
	require.Len(t, lock, 64) // SHA-256 hex
	require.Equal(t, lock, fs.ContentSha256)
}

func TestSpec_VerifyLock_DetectsTamper(t *testing.T) {
	fs := validSpec()
	_, err := Freeze(fs)
	require.NoError(t, err)

	ok, err := VerifyLock(fs)
	require.NoError(t, err)
	require.True(t, ok)

	// 변형 후 lock 검증 실패해야
	fs.Goal.OneLiner = "tampered"
	ok, err = VerifyLock(fs)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSpec_RequiredSlots_AllFilled(t *testing.T) {
	require.True(t, AllRequiredSlotsFilled(validSpec()))

	missing := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "x"}}
	require.False(t, AllRequiredSlotsFilled(missing))
}
```

- [ ] **Step 3: `core/spec/spec.go` 작성**

```go
package spec

import (
	"crypto/sha256"
	"encoding/hex"

	"google.golang.org/protobuf/proto"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// Freeze는 spec 내용을 결정적 bytes로 직렬화하고 SHA-256 hex를 ContentSha256에 set한다.
// 호출 후 fs.ContentSha256은 lock 값과 동일해진다.
func Freeze(fs *gilv1.FrozenSpec) (string, error) {
	fs.ContentSha256 = ""
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(fs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	hex := hex.EncodeToString(sum[:])
	fs.ContentSha256 = hex
	return hex, nil
}

// VerifyLock은 spec의 ContentSha256이 실제 내용 해시와 일치하는지 검증한다.
func VerifyLock(fs *gilv1.FrozenSpec) (bool, error) {
	stored := fs.ContentSha256
	fs.ContentSha256 = ""
	defer func() { fs.ContentSha256 = stored }()

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(fs)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == stored, nil
}

// AllRequiredSlotsFilled는 design.md 5.3에 정의된 필수 슬롯이 모두 채워졌는지 검사한다.
func AllRequiredSlotsFilled(fs *gilv1.FrozenSpec) bool {
	if fs == nil {
		return false
	}
	if fs.Goal == nil || fs.Goal.OneLiner == "" || len(fs.Goal.SuccessCriteriaNatural) < 3 {
		return false
	}
	if fs.Constraints == nil || len(fs.Constraints.TechStack) == 0 {
		return false
	}
	if fs.Verification == nil || len(fs.Verification.Checks) == 0 {
		return false
	}
	if fs.Workspace == nil || fs.Workspace.Backend == gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED {
		return false
	}
	if fs.Models == nil || fs.Models.Main == nil || fs.Models.Main.Provider == "" || fs.Models.Main.ModelId == "" {
		return false
	}
	if fs.Risk == nil || fs.Risk.Autonomy == gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED {
		return false
	}
	return true
}
```

- [ ] **Step 4: 테스트 실행 — 통과 확인**

```bash
cd /home/ubuntu/gil/core && go test ./spec/... -v
# Expected: PASS (3 tests)
```

- [ ] **Step 5: 커밋**

```bash
git add core/spec/ core/go.mod core/go.sum
git commit -m "feat(core/spec): Freeze + VerifyLock with SHA-256, required slot check"
```

---

## Task 8: core/session SQLite 스키마 + Migration runner

**Files:**
- Create: `core/session/schema.go`
- Create: `core/session/schema_test.go`

- [ ] **Step 1: 의존성 추가**

```bash
cd /home/ubuntu/gil/core
go get modernc.org/sqlite
```

- [ ] **Step 2: 실패 테스트 작성**

```go
package session

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMigrate_FreshDB_CreatesTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, Migrate(db))

	row := db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1")
	var v int
	require.NoError(t, row.Scan(&v))
	require.Equal(t, 1, v)
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, Migrate(db))
	require.NoError(t, Migrate(db)) // 재호출 OK
}
```

- [ ] **Step 3: 테스트 실행 — 실패 확인**

```bash
cd /home/ubuntu/gil/core && go test ./session/... -run Migrate
# Expected: FAIL (Migrate not defined)
```

- [ ] **Step 4: `core/session/schema.go` 작성**

```go
package session

import (
	"database/sql"
)

const currentSchemaVersion = 1

var migrations = []string{
	// v1
	`
	CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id            TEXT PRIMARY KEY,
		status        TEXT NOT NULL DEFAULT 'created',
		created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		spec_id       TEXT NOT NULL DEFAULT '',
		working_dir   TEXT NOT NULL DEFAULT '',
		goal_hint     TEXT NOT NULL DEFAULT '',
		total_tokens  INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
	CREATE INDEX IF NOT EXISTS idx_sessions_created_at ON sessions(created_at);
	`,
}

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}

	var current int
	row := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
	if err := row.Scan(&current); err != nil {
		return err
	}

	for v := current + 1; v <= currentSchemaVersion; v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v-1]); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", v); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: 테스트 실행 — 통과 확인**

```bash
cd /home/ubuntu/gil/core && go test ./session/... -v
# Expected: PASS for both tests
```

- [ ] **Step 6: 커밋**

```bash
git add core/session/ core/go.mod core/go.sum
git commit -m "feat(core/session): SQLite schema v1 with migration runner"
```

---

## Task 9: core/session Repo CRUD

**Files:**
- Create: `core/session/repo.go`
- Create: `core/session/repo_test.go`

- [ ] **Step 1: 실패 테스트 작성**

```go
package session

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	require.NoError(t, Migrate(db))
	return db
}

func TestRepo_Create_PersistsRow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	s, err := repo.Create(ctx, CreateInput{
		WorkingDir: "/tmp/proj",
		GoalHint:   "build x",
	})
	require.NoError(t, err)
	require.NotEmpty(t, s.ID)
	require.Equal(t, "created", s.Status)
	require.Equal(t, "/tmp/proj", s.WorkingDir)
}

func TestRepo_Get_ReturnsCreated(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	created, err := repo.Create(ctx, CreateInput{WorkingDir: "/tmp", GoalHint: ""})
	require.NoError(t, err)

	got, err := repo.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
}

func TestRepo_Get_MissingReturnsErr(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	_, err := repo.Get(ctx, "nonexistent")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRepo_List_ReturnsAll(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	for i := 0; i < 3; i++ {
		_, err := repo.Create(ctx, CreateInput{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	list, err := repo.List(ctx, ListOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, list, 3)
}
```

- [ ] **Step 2: 테스트 실행 — 실패 확인**

```bash
cd /home/ubuntu/gil/core && go test ./session/... -run Repo
# Expected: FAIL (Repo not defined)
```

- [ ] **Step 3: `core/session/repo.go` 작성**

```go
package session

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/oklog/ulid/v2"
)

var ErrNotFound = errors.New("session not found")

type Session struct {
	ID           string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	SpecID       string
	WorkingDir   string
	GoalHint     string
	TotalTokens  int64
	TotalCostUSD float64
}

type CreateInput struct {
	WorkingDir string
	GoalHint   string
}

type ListOptions struct {
	Limit        int
	StatusFilter string
}

type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) Create(ctx context.Context, in CreateInput) (Session, error) {
	id := ulid.Make().String()
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sessions (id, status, created_at, updated_at, working_dir, goal_hint)
		VALUES (?, 'created', ?, ?, ?, ?)
	`, id, now, now, in.WorkingDir, in.GoalHint)
	if err != nil {
		return Session{}, err
	}
	return Session{
		ID:         id,
		Status:     "created",
		CreatedAt:  now,
		UpdatedAt:  now,
		WorkingDir: in.WorkingDir,
		GoalHint:   in.GoalHint,
	}, nil
}

func (r *Repo) Get(ctx context.Context, id string) (Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint, total_tokens, total_cost_usd
		FROM sessions WHERE id = ?
	`, id)
	var s Session
	err := row.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint, &s.TotalTokens, &s.TotalCostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return s, nil
}

func (r *Repo) List(ctx context.Context, opts ListOptions) ([]Session, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint, total_tokens, total_cost_usd
	      FROM sessions`
	args := []any{}
	if opts.StatusFilter != "" {
		q += ` WHERE status = ?`
		args = append(args, opts.StatusFilter)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint, &s.TotalTokens, &s.TotalCostUSD); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 테스트 실행 — 통과 확인**

```bash
cd /home/ubuntu/gil/core && go test ./session/... -v
# Expected: PASS (6 tests total)
```

- [ ] **Step 5: 커밋**

```bash
git add core/session/repo.go core/session/repo_test.go
git commit -m "feat(core/session): Repo with Create/Get/List + ErrNotFound"
```

---

## Task 10: server/ gRPC 부트스트랩 + UDS 리스닝

**Files:**
- Create: `server/cmd/gild/main.go`
- Create: `server/internal/uds/listener.go`
- Create: `server/internal/uds/listener_test.go`

- [ ] **Step 1: 실패 테스트 작성**

```go
package uds

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListener_AcceptsConnection(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	lis, err := Listen(sockPath)
	require.NoError(t, err)
	defer lis.Close()

	connCh := make(chan net.Conn, 1)
	go func() {
		c, err := lis.Accept()
		if err != nil {
			t.Logf("accept: %v", err)
			return
		}
		connCh <- c
	}()

	c, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	defer c.Close()

	select {
	case got := <-connCh:
		require.NotNil(t, got)
		got.Close()
	case <-time.After(time.Second):
		t.Fatal("did not accept connection within 1s")
	}
}
```

- [ ] **Step 2: 테스트 실행 — 실패**

```bash
cd /home/ubuntu/gil/server && go test ./internal/uds/...
# Expected: FAIL
```

- [ ] **Step 3: `server/internal/uds/listener.go` 작성**

```go
package uds

import (
	"errors"
	"net"
	"os"
	"path/filepath"
)

func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, err
	}
	return lis, nil
}
```

- [ ] **Step 4: 테스트 실행 — 통과**

```bash
cd /home/ubuntu/gil/server && go test ./internal/uds/... -v
# Expected: PASS
```

- [ ] **Step 5: 커밋**

```bash
git add server/internal/uds/
git commit -m "feat(server/uds): UDS listener with mode 0600"
```

---

## Task 11: server/ SessionService gRPC 구현

**Files:**
- Create: `server/internal/service/session.go`
- Create: `server/internal/service/session_test.go`

- [ ] **Step 1: 의존성 추가**

```bash
cd /home/ubuntu/gil/server
go get github.com/jedutools/gil/core github.com/jedutools/gil/proto google.golang.org/grpc google.golang.org/protobuf
```

- [ ] **Step 2: 실패 테스트 작성**

```go
package service

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func newTestService(t *testing.T) *SessionService {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	return NewSessionService(session.NewRepo(db))
}

func TestSessionService_Create(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &gilv1.CreateRequest{
		WorkingDir: "/tmp/x",
		GoalHint:   "test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Id)
	require.Equal(t, "/tmp/x", resp.WorkingDir)
	require.Equal(t, gilv1.SessionStatus_CREATED, resp.Status)
}

func TestSessionService_Get(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
	require.NoError(t, err)

	got, err := svc.Get(ctx, &gilv1.GetRequest{Id: created.Id})
	require.NoError(t, err)
	require.Equal(t, created.Id, got.Id)
}

func TestSessionService_List(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	resp, err := svc.List(ctx, &gilv1.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, resp.Sessions, 2)
}
```

- [ ] **Step 3: 테스트 실행 — 실패**

```bash
cd /home/ubuntu/gil/server && go test ./internal/service/...
# Expected: FAIL (NewSessionService undefined)
```

- [ ] **Step 4: `server/internal/service/session.go` 작성**

```go
package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

type SessionService struct {
	gilv1.UnimplementedSessionServiceServer
	repo *session.Repo
}

func NewSessionService(repo *session.Repo) *SessionService {
	return &SessionService{repo: repo}
}

func (s *SessionService) Create(ctx context.Context, req *gilv1.CreateRequest) (*gilv1.Session, error) {
	created, err := s.repo.Create(ctx, session.CreateInput{
		WorkingDir: req.WorkingDir,
		GoalHint:   req.GoalHint,
	})
	if err != nil {
		return nil, err
	}
	return toProto(created), nil
}

func (s *SessionService) Get(ctx context.Context, req *gilv1.GetRequest) (*gilv1.Session, error) {
	got, err := s.repo.Get(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return toProto(got), nil
}

func (s *SessionService) List(ctx context.Context, req *gilv1.ListRequest) (*gilv1.ListResponse, error) {
	limit := int(req.Limit)
	got, err := s.repo.List(ctx, session.ListOptions{Limit: limit, StatusFilter: req.StatusFilter})
	if err != nil {
		return nil, err
	}
	out := make([]*gilv1.Session, 0, len(got))
	for _, s := range got {
		out = append(out, toProto(s))
	}
	return &gilv1.ListResponse{Sessions: out}, nil
}

func toProto(s session.Session) *gilv1.Session {
	return &gilv1.Session{
		Id:           s.ID,
		Status:       statusToProto(s.Status),
		CreatedAt:    timestamppb.New(s.CreatedAt),
		UpdatedAt:    timestamppb.New(s.UpdatedAt),
		SpecId:       s.SpecID,
		WorkingDir:   s.WorkingDir,
		GoalHint:     s.GoalHint,
		TotalTokens:  s.TotalTokens,
		TotalCostUsd: s.TotalCostUSD,
	}
}

func statusToProto(s string) gilv1.SessionStatus {
	switch s {
	case "created":
		return gilv1.SessionStatus_CREATED
	case "interviewing":
		return gilv1.SessionStatus_INTERVIEWING
	case "frozen":
		return gilv1.SessionStatus_FROZEN
	case "running":
		return gilv1.SessionStatus_RUNNING
	case "auto_paused":
		return gilv1.SessionStatus_AUTO_PAUSED
	case "done":
		return gilv1.SessionStatus_DONE
	case "stopped":
		return gilv1.SessionStatus_STOPPED
	default:
		return gilv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}
```

- [ ] **Step 5: 테스트 실행 — 통과**

```bash
cd /home/ubuntu/gil/server && go test ./internal/service/... -v
# Expected: PASS (3 tests)
```

- [ ] **Step 6: 커밋**

```bash
git add server/go.mod server/go.sum server/internal/service/
git commit -m "feat(server/service): SessionService with Create/Get/List"
```

---

## Task 12: server/cmd/gild — main 데몬 entry point

**Files:**
- Create: `server/cmd/gild/main.go`
- Create: `server/cmd/gild/main_test.go`

- [ ] **Step 1: 통합 테스트 작성**

```go
package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestGild_StartsAndAcceptsCreate(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "gild.sock")
	dbPath := filepath.Join(dir, "sessions.db")

	srv, err := newServer(dbPath, sockPath)
	require.NoError(t, err)

	go func() {
		_ = srv.Serve()
	}()
	defer srv.Stop()

	// 클라이언트로 연결
	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPath)
		return err == nil
	}, time.Second, 20*time.Millisecond)

	conn, err := grpc.NewClient(
		"unix:"+sockPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		}),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := gilv1.NewSessionServiceClient(conn)
	resp, err := client.Create(context.Background(), &gilv1.CreateRequest{WorkingDir: "/tmp"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Id)
}
```

- [ ] **Step 2: `server/cmd/gild/main.go` 작성**

```go
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
	"google.golang.org/grpc"

	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/server/internal/service"
	"github.com/jedutools/gil/server/internal/uds"
)

type server struct {
	grpc *grpc.Server
	lis  net.Listener
	db   *sql.DB
}

func newServer(dbPath, sockPath string) (*server, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if err := session.Migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	lis, err := uds.Listen(sockPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	g := grpc.NewServer()
	gilv1.RegisterSessionServiceServer(g, service.NewSessionService(session.NewRepo(db)))

	return &server{grpc: g, lis: lis, db: db}, nil
}

func (s *server) Serve() error {
	return s.grpc.Serve(s.lis)
}

func (s *server) Stop() {
	s.grpc.GracefulStop()
	_ = s.lis.Close()
	_ = s.db.Close()
}

func main() {
	home, _ := os.UserHomeDir()
	defaultBase := filepath.Join(home, ".gil")
	base := flag.String("base", defaultBase, "data directory")
	foreground := flag.Bool("foreground", false, "run in foreground")
	flag.Parse()

	if !*foreground {
		fmt.Fprintln(os.Stderr, "gild: --foreground required for now (detach mode in Phase 2)")
		os.Exit(2)
	}

	if err := os.MkdirAll(*base, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(*base, "sessions.db")
	sockPath := filepath.Join(*base, "gild.sock")

	srv, err := newServer(dbPath, sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	slog.Info("gild ready", "socket", sockPath, "db", dbPath)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		slog.Info("gild shutting down")
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, "gild serve:", err)
	}
	srv.Stop()
}
```

- [ ] **Step 3: 테스트 실행 — 통과**

```bash
cd /home/ubuntu/gil/server && go test ./cmd/gild/... -v
# Expected: PASS
```

- [ ] **Step 4: 빌드 확인**

```bash
cd /home/ubuntu/gil && make build
# Expected: bin/gild 생성
ls -la bin/
```

- [ ] **Step 5: 커밋**

```bash
git add server/cmd/gild/
git commit -m "feat(server): gild daemon entry with gRPC SessionService over UDS"
```

---

## Task 13: sdk/ — gRPC 클라이언트 wrapper

**Files:**
- Create: `sdk/client.go`
- Create: `sdk/client_test.go`

- [ ] **Step 1: 의존성 추가**

```bash
cd /home/ubuntu/gil/sdk
go get github.com/jedutools/gil/proto google.golang.org/grpc
```

- [ ] **Step 2: 실패 테스트 (mock-free, 실제 gRPC server 띄움)**

```go
package sdk

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	svc "github.com/jedutools/gil/server/internal/service"
	"github.com/jedutools/gil/server/internal/uds"
)

func startTestServer(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "gild.sock")

	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))

	lis, err := uds.Listen(sockPath)
	require.NoError(t, err)
	g := grpc.NewServer()
	gilv1.RegisterSessionServiceServer(g, svc.NewSessionService(session.NewRepo(db)))
	go g.Serve(lis)

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("unix", sockPath, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}, time.Second, 20*time.Millisecond)

	return sockPath, func() {
		g.GracefulStop()
		_ = lis.Close()
		_ = db.Close()
	}
}

func TestClient_Connect_AndCreate(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	s, err := cli.CreateSession(context.Background(), CreateOptions{WorkingDir: "/tmp"})
	require.NoError(t, err)
	require.NotEmpty(t, s.ID)
}
```

- [ ] **Step 3: 테스트 실행 — 실패**

```bash
cd /home/ubuntu/gil/sdk && go test ./...
# Expected: FAIL (Dial undefined)
```

- [ ] **Step 4: `sdk/client.go` 작성**

```go
package sdk

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

type Client struct {
	conn     *grpc.ClientConn
	sessions gilv1.SessionServiceClient
}

func Dial(sockPath string) (*Client, error) {
	conn, err := grpc.NewClient(
		"unix:"+sockPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", sockPath)
		}),
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:     conn,
		sessions: gilv1.NewSessionServiceClient(conn),
	}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

type CreateOptions struct {
	WorkingDir string
	GoalHint   string
}

type Session struct {
	ID         string
	Status     string
	WorkingDir string
	GoalHint   string
}

func (c *Client) CreateSession(ctx context.Context, opts CreateOptions) (*Session, error) {
	resp, err := c.sessions.Create(ctx, &gilv1.CreateRequest{
		WorkingDir: opts.WorkingDir,
		GoalHint:   opts.GoalHint,
	})
	if err != nil {
		return nil, err
	}
	return fromProto(resp), nil
}

func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	resp, err := c.sessions.Get(ctx, &gilv1.GetRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return fromProto(resp), nil
}

func (c *Client) ListSessions(ctx context.Context, limit int) ([]*Session, error) {
	resp, err := c.sessions.List(ctx, &gilv1.ListRequest{Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		out = append(out, fromProto(s))
	}
	return out, nil
}

func fromProto(s *gilv1.Session) *Session {
	return &Session{
		ID:         s.Id,
		Status:     s.Status.String(),
		WorkingDir: s.WorkingDir,
		GoalHint:   s.GoalHint,
	}
}
```

- [ ] **Step 5: 테스트 실행 — 통과**

```bash
cd /home/ubuntu/gil/sdk && go test ./... -v
# Expected: PASS
```

- [ ] **Step 6: 커밋**

```bash
git add sdk/
git commit -m "feat(sdk): gRPC client wrapper with Dial + Session ops"
```

---

## Task 14: cli/ Cobra root + `gil daemon` 명령

**Files:**
- Create: `cli/cmd/gil/main.go`
- Create: `cli/internal/cmd/root.go`
- Create: `cli/internal/cmd/daemon.go`

- [ ] **Step 1: 의존성 추가**

```bash
cd /home/ubuntu/gil/cli
go get github.com/spf13/cobra github.com/jedutools/gil/sdk
```

- [ ] **Step 2: `cli/cmd/gil/main.go` 작성**

```go
package main

import (
	"fmt"
	"os"

	"github.com/jedutools/gil/cli/internal/cmd"
)

func main() {
	if err := cmd.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: `cli/internal/cmd/root.go` 작성**

```go
package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func defaultBase() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gil")
}

func defaultSocket() string {
	return filepath.Join(defaultBase(), "gild.sock")
}

func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "gil",
		Short: "gil — autonomous coding harness",
	}
	root.AddCommand(daemonCmd())
	return root
}
```

- [ ] **Step 4: `cli/internal/cmd/daemon.go` 작성** — gild를 spawn하지 않고, 사용자에게 직접 띄우도록 안내 (Phase 1 단순화). 향후 Phase에서 spawn 자동화.

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Show how to start the gild daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("In Phase 1, start gild manually:")
			fmt.Println()
			fmt.Println("  gild --foreground")
			fmt.Println()
			fmt.Println("Default socket:", defaultSocket())
			fmt.Println("(automatic spawn arrives in Phase 2)")
			return nil
		},
	}
}
```

- [ ] **Step 5: 빌드 + 수동 검증**

```bash
cd /home/ubuntu/gil && make build
./bin/gil daemon
# Expected: 안내 메시지 출력
```

- [ ] **Step 6: 커밋**

```bash
git add cli/
git commit -m "feat(cli): Cobra root + 'gil daemon' guidance command"
```

---

## Task 15: cli/ `gil new` 명령

**Files:**
- Create: `cli/internal/cmd/new.go`
- Create: `cli/internal/cmd/new_test.go`

- [ ] **Step 1: 통합 테스트 작성** — 실제 gild 띄우고 `gil new` 호출

```go
package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	svc "github.com/jedutools/gil/server/internal/service"
	"github.com/jedutools/gil/server/internal/uds"
)

func startGildForTest(t *testing.T) (sock string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	sock = filepath.Join(dir, "gild.sock")

	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	lis, err := uds.Listen(sock)
	require.NoError(t, err)
	g := grpc.NewServer()
	gilv1.RegisterSessionServiceServer(g, svc.NewSessionService(session.NewRepo(db)))
	go g.Serve(lis)

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}, time.Second, 20*time.Millisecond)

	return sock, func() {
		g.GracefulStop()
		_ = lis.Close()
		_ = db.Close()
	}
}

func TestNew_OutputsSessionID(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var buf bytes.Buffer
	cmd := newCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock, "--working-dir", "/tmp/p"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))
	out := buf.String()
	require.Contains(t, out, "Session created:")
	// ULID는 26 chars
	require.Greater(t, len(out), 26)
}
```

- [ ] **Step 2: 테스트 실행 — 실패**

```bash
cd /home/ubuntu/gil/cli && go test ./internal/cmd/...
# Expected: FAIL (newCmd undefined)
```

- [ ] **Step 3: `cli/internal/cmd/new.go` 작성**

```go
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

func newCmd() *cobra.Command {
	var socket, workingDir, goalHint string
	c := &cobra.Command{
		Use:   "new",
		Short: "Create a new session",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			s, err := cli.CreateSession(ctx, sdk.CreateOptions{
				WorkingDir: workingDir,
				GoalHint:   goalHint,
			})
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Session created: %s\n", s.ID)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&workingDir, "working-dir", "", "project working directory")
	c.Flags().StringVar(&goalHint, "goal", "", "optional goal hint")
	return c
}
```

- [ ] **Step 4: `root.go`에서 등록**

```go
// cli/internal/cmd/root.go 의 Root() 함수 안에 추가
root.AddCommand(newCmd())
```

- [ ] **Step 5: 테스트 실행 — 통과**

```bash
cd /home/ubuntu/gil/cli && go test ./internal/cmd/... -v
# Expected: PASS
```

- [ ] **Step 6: 커밋**

```bash
git add cli/internal/cmd/new.go cli/internal/cmd/new_test.go cli/internal/cmd/root.go cli/go.mod cli/go.sum
git commit -m "feat(cli): 'gil new' creates session via gRPC"
```

---

## Task 16: cli/ `gil status` 명령

**Files:**
- Create: `cli/internal/cmd/status.go`
- Create: `cli/internal/cmd/status_test.go`

- [ ] **Step 1: 테스트 작성**

```go
package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/sdk"
)

func TestStatus_ListsSessions(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	// 미리 2개 세션 생성
	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	for i := 0; i < 2; i++ {
		_, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "CREATED")
	// 2 줄 + 헤더
	lines := bytes.Count([]byte(out), []byte("\n"))
	require.GreaterOrEqual(t, lines, 3)
}
```

- [ ] **Step 2: `cli/internal/cmd/status.go` 작성**

```go
package cmd

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

func statusCmd() *cobra.Command {
	var socket string
	var limit int
	c := &cobra.Command{
		Use:   "status",
		Short: "List sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			list, err := cli.ListSessions(ctx, limit)
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tWORKING_DIR\tGOAL")
			for _, s := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID, s.Status, s.WorkingDir, s.GoalHint)
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().IntVar(&limit, "limit", 100, "max sessions to list")
	return c
}
```

- [ ] **Step 3: `root.go`에 등록**

```go
// root.go의 Root() 함수에 추가
root.AddCommand(statusCmd())
```

- [ ] **Step 4: 테스트 실행 — 통과**

```bash
cd /home/ubuntu/gil/cli && go test ./internal/cmd/... -v
# Expected: PASS (both new + status)
```

- [ ] **Step 5: 커밋**

```bash
git add cli/internal/cmd/status.go cli/internal/cmd/status_test.go cli/internal/cmd/root.go
git commit -m "feat(cli): 'gil status' lists sessions in tab-separated table"
```

---

## Task 17: E2E 통합 검증 — bin/gild + bin/gil 수동 시나리오 자동화

**Files:**
- Create: `tests/e2e/phase01_test.sh`

- [ ] **Step 1: 스크립트 작성**

```bash
mkdir -p /home/ubuntu/gil/tests/e2e
cat > /home/ubuntu/gil/tests/e2e/phase01_test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"

cd "$ROOT"
make build > /dev/null

# 데몬 백그라운드 시작
"$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!
trap 'kill $GILD_PID 2>/dev/null || true; rm -rf "$BASE"' EXIT

# 소켓 대기
for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; exit 1; }

# 1. new 2회
ID1=$("$ROOT/bin/gil" new --socket "$SOCK" --working-dir /tmp/proj1 | awk '{print $3}')
ID2=$("$ROOT/bin/gil" new --socket "$SOCK" --working-dir /tmp/proj2 | awk '{print $3}')

# 2. status — 2 세션 확인
OUT=$("$ROOT/bin/gil" status --socket "$SOCK")
echo "$OUT" | grep -q "$ID1" || { echo "FAIL: $ID1 not in status"; exit 1; }
echo "$OUT" | grep -q "$ID2" || { echo "FAIL: $ID2 not in status"; exit 1; }
echo "$OUT" | grep -q CREATED || { echo "FAIL: status not CREATED"; exit 1; }

echo "OK: phase 1 e2e passed"
EOF
chmod +x /home/ubuntu/gil/tests/e2e/phase01_test.sh
```

- [ ] **Step 2: 실행 — 통과 확인**

```bash
/home/ubuntu/gil/tests/e2e/phase01_test.sh
# Expected: "OK: phase 1 e2e passed"
```

- [ ] **Step 3: Makefile에 e2e 타겟 추가**

기존 Makefile에 추가:

```makefile
e2e: build
	@bash tests/e2e/phase01_test.sh
```

- [ ] **Step 4: 전체 테스트 + e2e 한 번 더 검증**

```bash
cd /home/ubuntu/gil && make tidy && make test && make e2e
# Expected: 모든 단위 테스트 + e2e 통과
```

- [ ] **Step 5: 커밋**

```bash
git add tests/e2e/phase01_test.sh Makefile
git commit -m "test(e2e): phase 1 end-to-end validation script"
```

---

## Task 18: progress.md Phase 1 체크표시 + 다음 단계 안내

**Files:**
- Modify: `docs/progress.md`

- [ ] **Step 1: progress.md 의 Phase 1 모든 체크박스를 `[x]`로 변경, 결정 사항에 항목 추가**

`docs/progress.md`를 열어 Phase 1 섹션의 모든 `- [ ]` 를 `- [x]` 로 바꾸고, "최근 결정 사항" 표에 한 줄 추가:

```markdown
| 2026-04-26 | Phase 1 (코어 골격) 완료 — gild + gil new/status + event/spec/session 영속화 |
```

- [ ] **Step 2: 커밋**

```bash
git add docs/progress.md
git commit -m "docs(progress): mark Phase 1 complete"
```

---

## Phase 1 완료 검증 체크리스트

이 plan 전체 task가 끝나면 다음이 모두 동작해야 한다:

- [ ] `make tidy` 성공
- [ ] `make test` 모든 단위 테스트 통과
- [ ] `make build` → `bin/gil` + `bin/gild` 생성
- [ ] `make e2e` 통과
- [ ] `bin/gild --foreground` 실행 시 `~/.gil/gild.sock` 생성
- [ ] `bin/gil new` → ULID 출력
- [ ] `bin/gil status` → 표 형식 세션 목록
- [ ] `~/.gil/sessions.db` SQLite 파일에 세션 row 영속

이 모든 게 통과하면 Phase 2 (인터뷰 엔진) 로 진행. Phase 2 plan은 `docs/plans/phase-02-interview-engine.md` 로 별도 작성.

---

## 다음 Phase에 미루는 항목 (의도적)

Phase 1에서 안 다룬 것 — Phase 2~8에 분배:

- 데몬 자동 spawn (`gil` 첫 실행 시) — Phase 2
- 인터뷰 엔진 + Stage 머신 + adversary critique — Phase 2
- frozen spec lock 파일 디스크 저장/검증 — Phase 2
- core/event JSONL을 session에 통합 (현재는 세션과 무관) — Phase 2
- secret masking — Phase 2 (인터뷰가 시크릿 받기 시작할 때)
- 페이지 캐시 (25 events/page) — Phase 5 (압축과 함께)
- core/verify shell 단언 — Phase 3
- core/stuck 5패턴 — Phase 3
- core/checkpoint shadow git — Phase 4
- core/compact 캐시 보존 압축 — Phase 5
- core/memory 6 마크다운 — Phase 5
- core/edit SEARCH/REPLACE — Phase 6
- core/patch apply_patch DSL — Phase 6
- core/exec UDS RPC — Phase 6
- runtime/local sandbox — Phase 4
- TUI Bubbletea — Phase 7
- LLM provider 추상화 + Anthropic 어댑터 — Phase 2
