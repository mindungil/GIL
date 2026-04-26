# Phase 11 — Harness UX foundations (fresh-install dogfood path)

> Phase 1-10에서 엔진은 ref-grade로 lift됐지만 사용자가 처음 만나는 표면(auth/init/error/path layout)은 거의 비어 있음. 이 phase는 **fresh-install 사용자가 ANTHROPIC_API_KEY를 export하지 않고도 dogfood 가능**한 상태로 만든다.

**Goal**: `git clone && make install && gil` → 친절한 onboarding → `gil auth login` → `gil interview` → `gil run`이 가능한 상태. Daemon 자동 spawn. XDG 표준 준수. 명령 표면 확장.

**Scope (deferred to Phase 12)**: AGENTS.md 디스커버리, MCP add/remove, 슬래시 명령, project-local `.gil/`, permission 영속화, cost 가시성, JSON 출력, export.

---

## Track A — XDG 표준 + 1회 마이그레이션

### T1: core/paths 패키지

**Files**: `core/paths/xdg.go`, `core/paths/migrate.go`, `core/paths/xdg_test.go`

```go
type Layout struct {
    Config string  // $XDG_CONFIG_HOME/gil  (default ~/.config/gil)
    Data   string  // $XDG_DATA_HOME/gil    (default ~/.local/share/gil)
    State  string  // $XDG_STATE_HOME/gil   (default ~/.local/state/gil)
    Cache  string  // $XDG_CACHE_HOME/gil   (default ~/.cache/gil)
}

func Default() Layout
func FromEnv() Layout  // honors GIL_HOME override (single-dir for test/legacy)

// Specific helpers
func (l Layout) AuthFile() string       // Config/auth.json
func (l Layout) ConfigFile() string     // Config/config.toml
func (l Layout) MCPConfigFile() string  // Config/mcp.toml
func (l Layout) AgentsFile() string     // Config/AGENTS.md (global)
func (l Layout) SessionsDir() string    // Data/sessions
func (l Layout) Sock() string           // State/gild.sock
func (l Layout) Pid() string            // State/gild.pid
func (l Layout) Logs() string           // State/logs/
func (l Layout) ModelCatalog() string   // Cache/models.json
func (l Layout) RepomapCache() string   // Cache/repomap/
```

**Migration**: `MigrateLegacyTilde(legacyBase string) error` — 첫 init 또는 첫 daemon 시작에서 호출. `~/.gil/` 존재 + XDG 디렉토리들 모두 비어 있으면 적절히 분산 이동:
- `~/.gil/sessions/` → Data/sessions/
- `~/.gil/gild.sock` → State/gild.sock
- `~/.gil/gild.pid` → State/gild.pid
- legacy base 비우고 `~/.gil/MIGRATED → State/migrated.txt` 마커 남김.

**환경변수 우선순위**: `GIL_HOME`이 설정되면 모든 layout이 그 단일 디렉토리 하위로 (테스트 및 sandbox 호환). 그 외에는 XDG.

Reference: `/home/ubuntu/research/goose/crates/goose/src/config/paths.rs` (etcetera-driven XDG + appname 기반 + GOOSE_PATH_ROOT override) — Go stdlib `os.UserConfigDir`/`UserCacheDir`로 lift.

Commit: `feat(core/paths): XDG-standard layout + GIL_HOME override + ~/.gil/ migration`

### T2: 모든 path 호출 사이트 갱신

**Files**: `server/cmd/gild/main.go`, `cli/cmd/gil/main.go`, `cli/internal/socket/`, `tui/internal/app/socket.go`, `mcp/cmd/gilmcp/main.go`

기존 `--base ~/.gil` flag → `--home <dir>` (단일 override) + 환경변수 `GIL_HOME` 인식. Default는 `paths.FromEnv()`.

`gild --user <name>` 은 그대로 유지하되 layout = `paths.FromEnv().WithUser(name)` (모든 디렉토리에 `users/<name>/` 추가).

Commit: `feat(gild+gil+giltui+gilmcp): consume core/paths layout`

---

## Track B — gil auth (credstore)

### T3: core/credstore 패키지

**Files**: `core/credstore/store.go`, `core/credstore/file.go`, `core/credstore/store_test.go`

```go
type Provider string  // "anthropic", "openai", "openrouter", "vllm", ...

type Credential struct {
    Type     string             `json:"type"`        // "api" | "oauth" | "wellknown"
    APIKey   string             `json:"api_key,omitempty"`
    OAuth    *OAuthCredential   `json:"oauth,omitempty"`
    BaseURL  string             `json:"base_url,omitempty"`  // for vllm/wellknown
    Updated  time.Time          `json:"updated"`
}

type Store interface {
    List(ctx) ([]Provider, error)
    Get(ctx, Provider) (*Credential, error)
    Set(ctx, Provider, Credential) error
    Remove(ctx, Provider) error
}

// FileStore writes JSON 0600 to layout.AuthFile().
type FileStore struct { Path string }
```

**Schema** (`auth.json`):
```json
{
  "providers": {
    "anthropic": {"type": "api", "api_key": "sk-ant-...", "updated": "..."},
    "openai":    {"type": "api", "api_key": "sk-...",     "updated": "..."},
    "openrouter":{"type": "api", "api_key": "sk-or-v1-...", "updated": "..."}
  }
}
```

Atomic write (temp + rename), 0600 perms (Linux/macOS), Windows fallback (NTFS ACL는 우선 skip — 경고 로그).

Reference: `/home/ubuntu/research/opencode/packages/opencode/src/auth/index.ts` (discriminated union + atomic write + 0600). lift.

Commit: `feat(core/credstore): file-based credential store (opencode auth.json pattern)`

### T4: gil auth subcommand

**Files**: `cli/cmd/gil/auth.go`, `cli/cmd/gil/auth_test.go`

Cobra subcommand 그룹:
```
gil auth login [<provider>]    # interactive: provider picker if missing, then key prompt
gil auth list                   # table: provider, type, masked-key, updated
gil auth logout <provider>      # remove
gil auth status                 # what's configured + which env vars are also set
```

Interactive flow (`gil auth login`):
1. Provider 미지정 시 picker (anthropic/openai/openrouter/vllm/cancel)
2. Type: 현재는 "api" 만 (oauth는 Phase 12+)
3. API key 입력 (terminal echo off — `golang.org/x/term.ReadPassword`)
4. (선택) BaseURL — vllm일 때만 prompt
5. Verify: 옵션 — 실제 ping은 skip (네트워크 의존), 형식만 검증 (sk-..., sk-ant-..., sk-or-v1-... prefix)
6. credstore에 저장, 성공 메시지

`auth list` 마스킹: `sk-ant-...3f2a` (앞 7자 + 끝 4자만, 나머지 ...).

env var fallback: gild factory가 credstore.Get() → 없으면 env var → 없으면 에러. Backwards compat 유지.

Reference: `/home/ubuntu/research/opencode/packages/opencode/src/cli/cmd/providers.ts` (login command 구조 + clack prompts). 우리는 prompt만 stdlib로.

Commit: `feat(cli): gil auth login/list/logout/status (credstore-backed)`

### T5: gild factory가 credstore 우선

**Files**: `server/cmd/gild/main.go`

Provider factory 변경:
```go
func resolveCredential(name string) (apiKey, baseURL string, err error) {
    if cred, _ := store.Get(ctx, name); cred != nil {
        return cred.APIKey, cred.BaseURL, nil
    }
    // env var fallback
    switch name {
    case "anthropic": return os.Getenv("ANTHROPIC_API_KEY"), "", nil
    case "openai":    return os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_BASE_URL"), nil
    ...
    }
}
```

Commit: `feat(gild): provider factory consults credstore before env vars`

---

## Track C — Error overhaul + completion + auto-spawn

### T6: core/cliutil error layer

**Files**: `core/cliutil/error.go`, 사용처 grep+갱신

```go
type Error struct {
    Msg  string
    Hint string  // user-actionable next step
    Code int     // exit code
}

func (e *Error) Error() string

// Print formats: "Error: <msg>\nHint: <hint>" then os.Exit(code).
func Exit(err error)
```

핵심 변경 사이트:
- `socket did not appear` → `daemon not running` + Hint: `run "gil init" to set up, or "gild --foreground &" to start manually`
- `ANTHROPIC_API_KEY not set` → `no credentials for anthropic` + Hint: `gil auth login anthropic`
- `session must be frozen` → 동일 메시지 + Hint: `gil interview <id>` 또는 `gil spec freeze <id>`
- `unknown provider X` → `unknown provider "X"` + Hint: `available: anthropic, openai, openrouter, vllm. configure with: gil auth login <provider>`

Reference: `/home/ubuntu/research/opencode/packages/opencode/src/error.ts` (typed error layer 패턴).

Commit: `feat(core/cliutil): typed error with Hint + remediation rewrites`

### T7: gil completion <shell>

**Files**: `cli/cmd/gil/completion.go`

cobra의 built-in `GenBashCompletionV2Cmd`/`GenZshCompletionCmd`/`GenFishCompletionCmd` 노출. 한 파일.

```go
func newCompletionCmd(root *cobra.Command) *cobra.Command {
    return &cobra.Command{
        Use: "completion <bash|zsh|fish|powershell>",
        Short: "generate shell completion script",
        ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
        Args: cobra.ExactValidArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            switch args[0] {
            case "bash":  return root.GenBashCompletionV2(os.Stdout, true)
            case "zsh":   return root.GenZshCompletion(os.Stdout)
            case "fish":  return root.GenFishCompletion(os.Stdout, true)
            case "powershell": return root.GenPowerShellCompletion(os.Stdout)
            }
            return nil
        },
    }
}
```

README 갱신: `gil completion bash > /etc/bash_completion.d/gil` 등.

Commit: `feat(cli): gil completion <shell> (cobra built-in)`

### T8: gild auto-spawn from gil

**Files**: `cli/internal/daemon/spawn.go`, 모든 daemon-needing 명령 수정

```go
// EnsureRunning checks UDS reachability; if not, fork+exec gild --foreground
// in background, write pid to State/gild.pid, wait up to 5s for sock to appear.
func EnsureRunning(layout paths.Layout) error
```

호출 사이트: `gil interview/run/events/status/restore/spec/resume`. `gil auth/init/doctor/completion`은 호출하지 않음.

5s 안에 sock이 안 나타나면 명확한 에러 + Hint (로그 위치, manually 시작 방법).

Reference: docker CLI의 자동-daemon 패턴 (개념). PID 파일 + sock polling은 stdlib만.

Commit: `feat(cli): auto-spawn gild for daemon-needing commands (PID file + sock polling)`

---

## Track D — gil init + doctor

### T9: gil init

**Files**: `cli/cmd/gil/init.go`, `cli/cmd/gil/init_test.go`

`gil init` 실행 시:
1. layout.Config/Data/State/Cache 생성 (mkdir -p, 0700/0755)
2. config.toml stub 생성 (있으면 skip):
   ```toml
   # gil global config
   [defaults]
   provider = "anthropic"
   model = ""              # use provider default
   workspace_backend = "LOCAL_NATIVE"
   autonomy = "ASK_DESTRUCTIVE_ONLY"
   ```
3. `~/.gil/` 발견 시 `paths.MigrateLegacyTilde` 호출
4. `gil auth login` 자동 호출 (--no-auth 플래그 시 skip)
5. 다음 단계 출력: `gild` 실행하지 않고 종료. `gil interview` 안내.

Commit: `feat(cli): gil init — first-run scaffolding + legacy migration`

### T10: gil doctor

**Files**: `cli/cmd/gil/doctor.go`, `cli/cmd/gil/doctor_test.go`

체크 리스트:
- [ ] go runtime version (build-time embedded)
- [ ] Layout 디렉토리 존재 + 권한
- [ ] gild 바이너리 PATH에 있음
- [ ] gild 실행 중인지 (sock + pid 검사)
- [ ] credstore: 어떤 provider 등록됐나
- [ ] env var fallback 활성화된 provider
- [ ] sandbox: bwrap (linux) / sandbox-exec (mac) 사용 가능
- [ ] git, rsync, docker, ssh PATH 검사 (각 backend별)
- [ ] 잠재 문제: legacy `~/.gil/` 잔류, sock + pid mismatch

각 체크는 OK/WARN/FAIL + 한 줄 hint. Exit code: FAIL이 하나라도 있으면 1.

Reference: `/home/ubuntu/research/goose/crates/goose-cli/src/commands/doctor.rs`.

Commit: `feat(cli): gil doctor — environment + setup diagnostics`

---

## Track E — e2e + docs

### T11: e2e11 fresh-install path

**Files**: `tests/e2e/phase11_freshinstall_test.sh`

GIL_HOME=tmpdir 으로 격리:
1. `gil` (no args) → onboarding 메시지 표시 + exit 0
2. `gil init --no-auth` → 디렉토리 생성 검증
3. `gil auth login anthropic --api-key sk-ant-fake` (non-interactive flag) → auth.json 0600 검증
4. `gil auth list` → masking 검증
5. `gil doctor` → 각 체크 결과 fixture와 비교
6. `gil completion bash | head -5` → bash completion script 출력 확인
7. `gil status` → daemon 자동 spawn 검증, 결과 표시
8. `gil auth logout anthropic` → 키 삭제

Commit: `test(e2e): phase 11 — fresh-install onboarding path`

### T12: docs (install.md + README + progress)

`docs/install.md`:
- "환경 설정" 섹션을 `gil auth login` 중심으로 재작성
- env var fallback은 "CI 용" 으로만 언급
- XDG 디렉토리 위치 표 추가
- migration: `~/.gil/` 사용자는 `gil init` 1회 실행

README banner: "Phase 11 완료 — fresh install 친화 (auth/init/doctor + XDG + 자동 daemon)"

`docs/progress.md` row.

Commit: `docs: harness UX foundations (Phase 11)`

---

## Phase 11 완료 체크리스트

- [ ] `make e2e-all` 13 phase 통과 (12 + phase11-freshinstall)
- [ ] XDG 디렉토리 사용 + GIL_HOME override + legacy 마이그레이션
- [ ] `gil auth login/list/logout/status` 작동 + auth.json 0600
- [ ] gild factory가 credstore 우선
- [ ] `gil init` first-run 시나리오
- [ ] `gil doctor` 진단
- [ ] `gil completion <shell>`
- [ ] gild 자동 spawn (필요한 명령만)
- [ ] Error 메시지 Hint 포함 (`socket did not appear` 등 사라짐)
- [ ] install.md + README + progress 갱신
