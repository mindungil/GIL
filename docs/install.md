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

### Anthropic API key

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

`.bashrc` / `.zshrc`에 추가하는 것 권장.

### 데이터 디렉토리

기본: `~/.gil/`. 변경하려면 `gild --base /custom/path`.

다중 사용자 (1대 호스트에 여러 사용자):
```bash
gild --foreground --user alice --base /var/lib/gil   # → /var/lib/gil/users/alice
gild --foreground --user bob   --base /var/lib/gil   # → /var/lib/gil/users/bob
```

각 사용자의 socket/sqlite/sessions/events가 분리됩니다.

## 첫 실행

### 옵션 1 — 통상 실행

```bash
# 데몬 시작 (foreground; 다른 터미널 사용 권장)
gild --foreground &

# 세션 생성
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
gild --foreground &
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

`~/Library/Application Support/Claude/claude_desktop_config.json`에 추가:
```json
{
  "mcpServers": {
    "gil": {
      "command": "/usr/local/bin/gilmcp",
      "args": ["--socket", "/home/<user>/.gil/gild.sock"]
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

### "ANTHROPIC_API_KEY not set"

```bash
echo $ANTHROPIC_API_KEY    # 비어있으면 키가 없음
export ANTHROPIC_API_KEY=sk-ant-...
```

### "socket did not appear"

gild가 안 떴거나 socket 경로 mismatch. 확인:
```bash
pgrep -af gild              # gild 떠있나?
ls -la ~/.gil/gild.sock     # socket 있나?
```

수동 시작:
```bash
gild --foreground           # foreground로 띄워서 에러 메시지 확인
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

전체 e2e (9 phase):
```bash
make e2e-all
```

각 phase 개별:
```bash
make e2e   # phase 1: core skeleton
make e2e2  # phase 2: interview
make e2e3  # phase 3: slot fill + adversary + audit
make e2e4  # phase 4: run engine
make e2e5  # phase 5: async + checkpoint + restore
make e2e6  # phase 6: memory + repomap + compact
make e2e7  # phase 7: edit + patch + permission
make e2e8  # phase 8: exec + HTTP + MCP
make e2e9  # phase 9: soak
```

unit tests:
```bash
make test
```

## 다음 단계

- 첫 dogfood 실행: [docs/dogfood/2026-04-26-first-run-procedure.md](dogfood/2026-04-26-first-run-procedure.md)
- 설계 문서: [docs/design.md](design.md)
- 진행 이력: [docs/progress.md](progress.md)
