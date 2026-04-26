# Phase 10 — Cloud real impl, VS Code 확장, 패키징, OAuth, Atropos

> Phase 9 stub들을 진짜 코드로 교체. VS Code 확장을 Cline에서 lift한 패턴으로 scaffold. GoReleaser/homebrew/deb/rpm 패키징. mock OIDC 기반 multi-user 인증. Hermes Atropos RL 환경 어댑터.

**Goal**: ANTHROPIC_API_KEY 같은 외부 자원 없이 build + verify 가능한 모든 항목을 끝까지 구현. 실제 클라우드 계정/Anthropic 키가 필요한 항목은 코드 경로는 존재하되 env var 없으면 ErrNotConfigured.

**Skip (require external)**: 실제 Anthropic dogfood 실행, 실제 Modal/Daytona 배포 검증, 실제 OAuth 공급자 통합 검증.

---

## Track A — Modal 진짜 구현 (REST + Sandbox)

### T1: Modal Sandbox API 클라이언트

**Files**: `runtime/modal/client.go`, `runtime/modal/client_test.go`, replace `runtime/modal/modal.go`

Modal의 진짜 sandbox API는 Python SDK (`modal.Sandbox`) 가 표준이고 Go client는 없음. 두 가지 옵션:
1. **CLI shell-out** — `modal run` 으로 Python script을 임시 실행 (느림, 안정적)
2. **REST API 직접 호출** — `modal-client` Python의 endpoint reverse-engineering (빠름, 깨질 위험)

**선택**: CLI shell-out + manifest 패턴.

```go
type Provider struct {
    ModalBin string  // "modal"
    Manifest string  // path to gil-modal-app.py (generated)
}

// Provision:
//   1. Generate ephemeral Python file: gil-modal-<sessionID>.py
//      Defines: app = modal.App("gil"); image = modal.Image.debian_slim().pip_install(...)
//      Defines: sb = app.spawn_sandbox(image=image, mounts=[modal.Mount.from_local_dir(...)])
//   2. Run `modal run --detach gil-modal-<sessionID>.py::create_sandbox`
//   3. Capture sandbox ID from stdout
//   4. Wrapper.Wrap(cmd, args) → `modal run gil-modal-<sessionID>.py::exec_in -- <cmd>...`
//   5. Teardown: `modal app stop gil-<sessionID>`
```

Test: mock `modal` 바이너리 (shell script 으로 fake stdout 반환), Provision → Wrapper.Wrap → 예상 argv 검증.

Commit: `feat(runtime/modal): real Modal Sandbox driver (CLI shell-out)`

### T2: Modal e2e under stub

ANTHROPIC 없이 검증: `MODAL_TOKEN_ID=fake MODAL_TOKEN_SECRET=fake MODAL_BIN=$PWD/tests/fixtures/modal-stub.sh` 으로 e2e10_modal.sh

Commit: `test(e2e): Modal driver argv shape verification under fake CLI`

---

## Track B — Daytona 진짜 구현 (REST API)

### T3: Daytona REST 클라이언트

**Files**: `runtime/daytona/client.go`, `runtime/daytona/client_test.go`, replace `runtime/daytona/daytona.go`

Daytona는 진짜 OpenAPI / REST가 있음. https://api.daytona.io. 핵심 endpoint:
- `POST /workspaces` — create
- `GET /workspaces/{id}` — status
- `POST /workspaces/{id}/exec` — run command (returns stdout/stderr/exitcode)
- `DELETE /workspaces/{id}` — teardown

```go
type Client struct {
    BaseURL string  // "https://api.daytona.io"
    APIKey  string
    HTTP    *http.Client
}

func (c *Client) CreateWorkspace(ctx, image, sessionID) (*Workspace, error)
func (c *Client) Exec(ctx, wsID, cmd, args) (*ExecResult, error)
func (c *Client) Delete(ctx, wsID) error

// Wrapper for command execution that adapts ExecResult → exec.Cmd-like interface.
// Daytona returns full stdout/stderr/exitcode in single response, so this is
// a different shape than CommandWrapper which expects argv. We model it as a
// "RemoteExecutor" interface that runtime/cloud bridges.
```

이 부분이 까다로움 — 기존 CommandWrapper interface는 argv 기반이고, Daytona REST는 결과 한 번에 받음. 두 가지 길:
1. CommandWrapper에 ExecRemote(ctx, cmd, args) (stdout, stderr, exit, err) 추가
2. Daytona에 로컬 helper script을 mount하고 그게 SSH-like serve 하게

**선택**: (1) CommandWrapper interface 확장. core/tool/bash.Wrapper interface에 옵셔널 RemoteExecutor 메서드 추가. Bash tool이 RemoteExecutor 구현 있으면 그걸 사용, 없으면 기존 Wrap+exec.Cmd 사용.

Commit: `feat(core/tool): RemoteExecutor optional interface for HTTP-bound backends`
Commit: `feat(runtime/daytona): real REST API client + workspace lifecycle`

### T4: Daytona e2e under httptest

stdlib `httptest.Server` 으로 fake Daytona REST endpoint 띄우고, gild가 실제 Wrapper을 거쳐 명령을 보내고 결과 받는 path 검증. e2e10_daytona.sh (Go test으로).

Commit: `test(e2e): Daytona driver under httptest server`

---

## Track C — VS Code 확장 scaffold (Cline lift)

### T5: vscode/ TypeScript 프로젝트 초기화

**Files**: `vscode/package.json`, `vscode/tsconfig.json`, `vscode/esbuild.mjs`, `vscode/src/extension.ts`

Cline의 `package.json`/`tsconfig.json`/`esbuild.mjs` 구조를 lift하되 **gil 전용으로 슬림화**:
- 의존성: `vscode`, `@grpc/grpc-js`, `@grpc/proto-loader`만 (cline은 더 많음)
- activate(): gild socket 자동 감지 (default `~/.gil/gild.sock`), 연결 안 되면 "gild not running" 안내
- 명령 등록: `gil.startSession`, `gil.runSession`, `gil.tailEvents`
- 사이드바 webview: 세션 리스트 + tail (Cline의 ChatView 패턴 lift)

**의도적 deviation**: Cline의 React 기반 webview는 무거움 → gil은 단순 HTML+vanilla JS panel.

Commit: `feat(vscode): extension scaffold (Cline lift, slimmed)`

### T6: gRPC client wrapper for VS Code

**Files**: `vscode/src/gild_client.ts`

`@grpc/grpc-js` + `@grpc/proto-loader`로 `proto/gil/v1/*.proto` 로드, 4개 service stub 생성. `subscribeEvents` server-streaming → vscode webview로 forward.

Commit: `feat(vscode): gild gRPC client + event tail`

### T7: README + 설치 가이드

`vscode/README.md`. `vsce package` 명령으로 `.vsix` 생성 가능함을 명시. Marketplace 게시는 별도 단계.

Commit: `docs(vscode): scaffold README + packaging instructions`

---

## Track D — GoReleaser + homebrew + deb/rpm

### T8: .goreleaser.yaml

**Files**: `.goreleaser.yaml`, `.github/workflows/release.yml`

4 binary (gil, gild, giltui, gilmcp) × (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64) = 16 archives. Plus:
- nfpms: deb + rpm
- brews: homebrew formula (tap repo은 placeholder)
- archives: tarball + zip

GitHub Action: tag push → goreleaser run.

Commit: `chore(release): GoReleaser config (4 binaries × 4 platforms + deb/rpm/brew)`

### T9: Makefile release target

`make release` → goreleaser snapshot (no publish). 로컬에서 build matrix 검증용.

Commit: `chore(make): release target wrapping goreleaser snapshot`

### T10: Test under snapshot

`make release` 실행, dist/ 안에 binary 16개 + .deb + .rpm 생성 확인하는 스크립트.

Commit: `test(release): snapshot artifact existence check`

---

## Track E — Multi-user OAuth (mock OIDC)

### T11: OIDC middleware

**Files**: `server/internal/auth/oidc.go`, `server/internal/auth/middleware.go`, `server/internal/auth/middleware_test.go`

Reference lift from `/home/ubuntu/research/hermes-agent/agent/google_oauth.py` (auth-code flow shape). gRPC interceptor:
- Bearer token in metadata → validate via OIDC provider's JWKS
- Extract `sub` (user ID) → enforce `--user` directory namespace
- Without auth metadata: only allow on UDS (local), reject on TCP

Test: mock JWKS server (httptest), valid/invalid/expired token cases.

Commit: `feat(server/auth): OIDC bearer-token middleware (gRPC interceptor)`

### T12: gild --auth flag

`gild --auth-issuer https://auth.example.com --auth-audience gil` 형태로 OIDC 활성화. 없으면 기존처럼 인증 없음.

Commit: `feat(gild): --auth-issuer/--auth-audience flags for OIDC enforcement`

### T13: e2e auth path under mock OIDC

httptest 기반 OIDC issuer + JWKS, gild가 그걸 신뢰하고 valid bearer token으로 RPC 호출 → 200, expired token → Unauthenticated.

Commit: `test(e2e): mock OIDC auth path`

---

## Track F — Atropos RL 환경 어댑터

### T14: Python wrapper for gil

**Files**: `python/gil_atropos/__init__.py`, `python/gil_atropos/env.py`, `python/gil_atropos/README.md`

Reference lift: `/home/ubuntu/research/hermes-agent/optional-skills/mlops/hermes-atropos-environments/SKILL.md` interface.

```python
class GilCodingEnv(HermesAgentBaseEnv):
    """Atropos RL environment that uses gil as the coding agent backend.
    
    Each rollout:
      1. Sample a coding task from dataset.
      2. Spawn fresh gil session.
      3. Run interview (scripted) → spec.
      4. Run autonomous loop with given model.
      5. Score: verifier pass/fail + step count + resource use.
    """
```

Implementation: gRPC-Python client (`grpcio-tools` 으로 `proto/gil/v1/*.proto` → Python). Calls SessionService/RunService. Stdout result fed to Atropos reward fn.

이 트랙은 Python 코드 — Go 빌드와 무관. 순수 reference scaffold.

Commit: `feat(python): gil_atropos environment adapter (Python wrapper)`

### T15: Python README + 사용 예

How to install (`pip install -e python/gil_atropos`), how to register with Atropos (`atropos register gil_coding`).

Commit: `docs(python): Atropos environment usage guide`

---

## Track G — Docs + Phase 10 마무리

### T16: progress 업데이트 + 결정 row

Commit: `docs(progress): Phase 10 — cloud real + VS Code + packaging + OAuth + Atropos`

### T17: README "What's next" 업데이트

Phase 10 후의 잔여항목: 실제 cloud 계정 검증, Marketplace 배포, OAuth 실제 IdP 통합, Atropos training run.

Commit: `docs(README): Phase 10 status + remaining external-dependent items`

---

## Phase 10 완료 체크리스트

- [ ] `make e2e-all` 9 phase 통과 (regression-free)
- [ ] Modal 진짜 driver (CLI shell-out, env-var-gated, fake CLI 으로 e2e)
- [ ] Daytona 진짜 driver (REST, httptest 으로 e2e)
- [ ] VS Code 확장 scaffold (vsce package 가능)
- [ ] GoReleaser snapshot 으로 16 binary + deb + rpm 생성
- [ ] OIDC 인증 미들웨어 (mock JWKS 으로 e2e)
- [ ] Python Atropos 환경 어댑터 (gRPC client + base env subclass)
- [ ] progress + README 갱신

## Phase 10 이후 (외부 자원 필요)

- 실제 Anthropic-driven dogfood (`ANTHROPIC_API_KEY`)
- Modal / Daytona 실제 cloud 계정 으로 배포 검증
- 실제 OIDC 공급자 (Google, Auth0) 통합 테스트
- Atropos 실제 training run (Hermes Atropos server + RLHF)
- VS Code Marketplace 게시
- Homebrew tap repo 생성 + 등록
