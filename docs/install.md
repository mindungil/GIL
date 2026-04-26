# Install

빌드 → 설치 → 첫 실행 가이드.

## 요구사항

- **Go 1.25+** (go.work 모듈)
- **git** (shadow checkpoint + 소스 클론)
- **buf CLI** (proto 재생성, 변경 시에만)

선택:
- **bwrap** (Linux sandbox, `LOCAL_SANDBOX` 백엔드)
- **/usr/bin/sandbox-exec** (macOS, 기본 설치됨)
- **docker** (`DOCKER` 백엔드)
- **ssh + rsync** (`SSH` 백엔드 + 원격 file sync)

## 빌드

```bash
git clone https://github.com/<user>/gil.git
cd gil
make build
```

생성 binary:
- `bin/gil` — CLI client
- `bin/gild` — 데몬 (gRPC server)
- `bin/giltui` — Bubbletea TUI
- `bin/gilmcp` — MCP server adapter (stdio)

빌드 시간: ~30s (cold), ~5s (incremental).

## 설치

```bash
make install
```

`/usr/local/bin/{gil,gild,giltui,gilmcp}`에 설치. 권한 없으면 `sudo`로 자동 fallback.

## 환경 설정

### 1. 첫 실행 (one-time setup)

```bash
gil init
```

이 명령은:
1. XDG 디렉토리 생성 (`$XDG_CONFIG_HOME/gil`, `$XDG_DATA_HOME/gil`, `$XDG_STATE_HOME/gil`, `$XDG_CACHE_HOME/gil`)
2. `config.toml` stub 작성 (provider/model/autonomy 기본값 + 주석)
3. legacy `~/.gil/` 가 있으면 자동 마이그레이션 (idempotent, MIGRATED stamp)
4. `gil auth login` 자동 호출 (대화형 — `--no-auth`로 건너뛸 수 있음)

CI/스크립트는 `gil init --no-auth --no-config` 로 디렉토리만 만들고 끝낼 수 있습니다.

### 2. Provider 등록

```bash
gil auth login                                 # 대화형 picker (anthropic/openai/openrouter/vllm)
gil auth login anthropic                       # 바로 anthropic, key는 echo off로 prompt
gil auth login --api-key sk-ant-... anthropic  # non-interactive (CI 용)
gil auth login vllm --base-url http://host:8000/v1 --api-key local
```

키는 `$XDG_CONFIG_HOME/gil/auth.json` (mode `0600`, parent dir `0700`) 에 저장됩니다.
파일 형식은 versioned envelope (`{"version":1,"providers":{...}}`), 쓰기는 atomic
(tmp + rename + parent fsync).

### 3. 검증

```bash
gil doctor                    # 5 그룹 (Layout / Daemon / Credentials / Sandboxes / Tools), 15 체크
gil auth list                 # 등록된 provider 목록 (마스킹된 키)
gil auth status               # credstore + 환경변수 fallback 둘 다 표시
```

`gil doctor`는 FAIL 이 하나라도 있으면 exit 1, 아니면 0. `--json` 으로 머신 가독 출력.

### 환경변수 fallback (CI 용)

credstore가 비어 있으면 환경변수 fallback이 적용됩니다 (gild factory가 credstore → env 순):

| Provider | 환경변수 |
|---|---|
| anthropic | `ANTHROPIC_API_KEY` |
| openai | `OPENAI_API_KEY` |
| openrouter | `OPENROUTER_API_KEY` |
| vllm | `VLLM_API_KEY` + `VLLM_BASE_URL` |

스크립트에서 키를 노출하기 싫으면 `gil auth login` 한 번 실행 후 환경변수는 unset.

### XDG 디렉토리 위치

| 디렉토리 | 기본 경로 (Linux) | 기본 경로 (macOS) | 환경변수 |
|---|---|---|---|
| Config | `~/.config/gil` | `~/Library/Application Support/gil` | `XDG_CONFIG_HOME` |
| Data | `~/.local/share/gil` | `~/Library/Application Support/gil` | `XDG_DATA_HOME` |
| State | `~/.local/state/gil` | `~/Library/Application Support/gil` | `XDG_STATE_HOME` |
| Cache | `~/.cache/gil` | `~/Library/Caches/gil` | `XDG_CACHE_HOME` |

각 디렉토리의 역할:
- **Config** — `auth.json`, `config.toml`, `mcp.toml`, `AGENTS.md` (사용자 편집 대상, 백업 권장)
- **Data** — SQLite session DB (`sessions.db`), per-session 워크스페이스 (`sessions/<id>/`), shadow git (`shadow/`)
- **State** — gild Unix socket (`gild.sock`), PID 파일 (`gild.pid`), 로그 (`logs/`) — 재부팅 시 사라져도 무방
- **Cache** — model catalog snapshot, repomap 캐시 — 재생성 가능

전체를 한 디렉토리로 모으려면: `export GIL_HOME=/path/to/single-tree` →
`$GIL_HOME/{config,data,state,cache}` 로 모입니다 (테스트/sandbox/portable install용).

### 마이그레이션 (legacy `~/.gil/`)

기존에 `~/.gil/` 을 쓰던 사용자: `gil init` 1회 실행하면 자동으로 분산됩니다.
마이그레이션은 idempotent (이미 옮긴 파일은 건너뜀), `~/.gil/MIGRATED` stamp 가
남아 두 번째 실행은 no-op. cross-device (EXDEV) 케이스는 copy + remove로 폴백.
마이그레이션 후 `~/.gil/` 자체는 사용자가 수동 삭제 (gil은 안전상 그대로 둠).

### 다중 사용자 (1대 호스트에 여러 사용자)

```bash
gild --foreground --user alice   # → <Data>/users/alice/, <State>/users/alice/, ...
gild --foreground --user bob     # → <Data>/users/bob/,   <State>/users/bob/,   ...
```

각 사용자의 socket/sqlite/sessions/events 가 모두 분리됩니다 (4개 root 모두 `users/<name>/` 접미).

## 첫 실행

### 옵션 1 — 통상 실행

```bash
# 데몬은 자동 spawn — 수동으로 띄우려면:
# gild --foreground &

# 세션 생성 (gild가 떠있지 않으면 자동 spawn)
SESSION=$(gil new --working-dir /path/to/your/project | awk '{print $3}')

# 인터뷰 (대화형; 답변할 때까지 묻습니다)
gil interview $SESSION

# 자율 실행
gil run $SESSION

# 진행 상황은 별도 터미널에서:
gil events $SESSION --tail
# 또는:
giltui
```

### 옵션 2 — 비동기 + TUI 모니터링

```bash
SESSION=$(gil new --working-dir /path/to/your/project | awk '{print $3}')
gil interview $SESSION
# (인터뷰 완료 후)
gil run $SESSION --detach    # 즉시 반환

giltui                       # 라이브 모니터링
```

TUI에서:
- `j`/`k` — 세션 navigate
- `r` — 새로고침
- 권한 요청 모달 뜨면 `y`/`n`/`Esc`
- `q` — 종료

## 옵션 백엔드 활성화

### LOCAL_SANDBOX (bwrap, Linux)

```bash
sudo apt install bubblewrap        # Debian/Ubuntu
sudo dnf install bubblewrap        # Fedora
```

인터뷰에서 workspace.backend = LOCAL_SANDBOX 선택. RunService가 자동으로 bwrap-wrap.

### DOCKER

```bash
# docker installed and daemon running
docker --version  # verify
```

인터뷰에서 workspace.backend = DOCKER, workspace.path = "alpine:latest" (or your image). RunService가 `docker run -d --rm --name gil-<id>` 후 per-command `docker exec`.

### SSH (with rsync)

```bash
# Local side
sudo apt install rsync openssh-client

# SSH key works to the remote
ssh user@host echo ok
```

인터뷰에서 workspace.backend = SSH, workspace.path = `user@host[:port][/keypath]`. RunService가 push 전 / pull 후 rsync 실행.

### MCP server adapter (Claude Desktop 등 외부 클라이언트가 gil 사용)

socket 경로는 XDG State 디렉토리 안. 경로 확인:
```bash
gil doctor | grep -i 'gild daemon'   # "running at <path>" 또는 "not running at <path>"
```

`~/Library/Application Support/Claude/claude_desktop_config.json`에 추가
(아래 socket 경로는 사용자 환경에 맞게 — 보통 Linux=`~/.local/state/gil/gild.sock`,
macOS=`~/Library/Application Support/gil/gild.sock`):
```json
{
  "mcpServers": {
    "gil": {
      "command": "/usr/local/bin/gilmcp",
      "args": ["--socket", "/home/<user>/.local/state/gil/gild.sock"]
    }
  }
}
```

Claude Desktop 재시작. `list_sessions`/`get_session`/`create_session` 도구 사용 가능.

### HTTP/JSON gateway (curl, browser)

```bash
gild --foreground --http :8080 &

curl http://127.0.0.1:8080/v1/sessions
curl http://127.0.0.1:8080/v1/sessions/$SESSION
curl -X POST http://127.0.0.1:8080/v1/sessions/$SESSION/run \
  -H 'Content-Type: application/json' \
  -d '{"provider":"anthropic","model":""}'
```

라이브 events는 SSE로 stream:
```bash
curl -N http://127.0.0.1:8080/v1/sessions/$SESSION/events
```

### Prometheus metrics

```bash
gild --foreground --metrics :9090 &

curl http://127.0.0.1:9090/metrics | grep gil_
```

Prometheus 서버에서 scrape:
```yaml
scrape_configs:
  - job_name: gil
    static_configs:
      - targets: ['localhost:9090']
```

## 문제 해결

먼저 `gil doctor` 부터. 5 그룹의 체크가 OK/INFO/WARN/FAIL 중 어디에 걸렸는지가 가장 빠른 단서.

### "no credentials for anthropic"

credstore에도 환경변수에도 키가 없는 상태. 둘 중 하나로 해결:
```bash
gil auth login anthropic    # credstore 등록 (권장)
# 또는
export ANTHROPIC_API_KEY=sk-ant-...
```
`gil auth status` 로 어느 쪽이 들어가 있는지 확인 가능.

### "daemon not running"

gild가 안 떠있음. 거의 모든 `gil` 명령은 자동 spawn 하므로 이 에러가 보이면 gild
바이너리가 PATH에 없거나 socket 경로가 mismatch:
```bash
which gild                            # gild 있나?
gil doctor | grep -E "Daemon|gild"    # gild binary + socket 상태
ls -la "$(gil doctor --json 2>/dev/null | jq -r '.checks[] | select(.name=="gild daemon").message' | sed -E 's/.*at (.*) .*/\1/')" 2>/dev/null
```

수동 기동 (디버깅용):
```bash
gild --foreground           # foreground로 띄워서 에러 메시지 직접 확인
```

### "session must be frozen before run"

인터뷰가 confirm까지 안 갔거나 spec freeze 실패. 확인:
```bash
gil status                  # 세션 status 확인
gil spec $SESSION           # 현재 spec 확인
gil spec freeze $SESSION    # 수동 freeze (모든 required slot 채워져야 함)
```

### LOCAL_SANDBOX 거부

bwrap 미설치. `apt install bubblewrap` 후 재시도. 또는 spec.workspace.backend = LOCAL_NATIVE로 변경.

### 권한 deny가 반복됨

spec.risk.autonomy = ASK_PER_ACTION이면 거의 모든 tool이 차단됩니다. TUI 안 떠있으면 60초 후 자동 deny. 두 옵션:
- TUI 띄우기 (`giltui`)
- spec.risk.autonomy를 ASK_DESTRUCTIVE_ONLY 또는 FULL로 변경

## 검증

전체 e2e (13 phase):
```bash
make e2e-all
```

각 phase 개별:
```bash
make e2e                  # phase 1: core skeleton
make e2e2                 # phase 2: interview
make e2e3                 # phase 3: slot fill + adversary + audit
make e2e4                 # phase 4: run engine
make e2e5                 # phase 5: async + checkpoint + restore
make e2e6                 # phase 6: memory + repomap + compact
make e2e7                 # phase 7: edit + patch + permission
make e2e8                 # phase 8: exec + HTTP + MCP
make e2e9                 # phase 9: soak
make e2e10-modal          # phase 10: Modal cloud sandbox
make e2e10-daytona        # phase 10: Daytona REST workspace
make e2e10-oidc           # phase 10: OIDC bearer-token auth on TCP
make e2e11-freshinstall   # phase 11: fresh-install onboarding (init/auth/doctor)
```

unit tests:
```bash
make test
```

## 다음 단계

- 첫 dogfood 실행: [docs/dogfood/2026-04-26-first-run-procedure.md](dogfood/2026-04-26-first-run-procedure.md)
- 설계 문서: [docs/design.md](design.md)
- 진행 이력: [docs/progress.md](progress.md)
