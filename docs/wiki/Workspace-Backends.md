# Workspace Backends

`spec.workspace.backend` (인터뷰에서 결정):

| Backend | 무엇 | 요구사항 |
|---|---|---|
| `LOCAL_NATIVE` | 직접 실행 (default) | — |
| `LOCAL_SANDBOX` | bwrap (Linux) | bwrap installed |
| `DOCKER` | per-command `docker exec` | docker daemon |
| `SSH` | ssh + rsync | ssh + rsync |
| `MODAL` | Modal cloud sandbox (CLI shell-out) | `MODAL_TOKEN_*` + modal CLI |
| `DAYTONA` | Daytona workspace (REST API) | `DAYTONA_API_KEY` |
| `VM` | (planned) | — |

## LOCAL_NATIVE

직접 실행. agent가 사용자 권한으로 명령 실행. 가장 빠르지만 격리 없음.

권장: 신뢰하는 워크스페이스 + ASK_DESTRUCTIVE_ONLY 자율성.

## LOCAL_SANDBOX (bwrap, Linux)

```bash
sudo apt install bubblewrap
```

3 모드:
- `ReadOnly` — 워크스페이스 읽기만 (research)
- `WorkspaceWrite` — 워크스페이스 쓰기 OK, 외부 RO (default)
- `FullAccess` — 호스트 전체 (위험)

## DOCKER

```bash
docker --version  # 데몬 확인
```

인터뷰: `workspace.path = "alpine:latest"` (또는 본인 이미지). RunService 가 `docker run -d --rm --name gil-<id>` 후 per-command `docker exec`.

## SSH

```bash
sudo apt install rsync openssh-client
ssh user@host echo ok
```

Phase 8: per-command ssh exec (file ops local).
Phase 9: rsync push 전 / pull 후 (Phase 8 limitation 해소).

`workspace.path = user@host[:port][/keypath]` 형식.

## MODAL

```bash
export MODAL_TOKEN_ID=...
export MODAL_TOKEN_SECRET=...
pip install modal
```

Phase 10 실 driver:
- ephemeral Python manifest 생성 (`modal.App` + `Image.debian_slim` + `Mount.from_local_dir`)
- `modal run <manifest>::exec_in_sandbox` per command
- Teardown: `modal app stop`

## DAYTONA

```bash
export DAYTONA_API_KEY=...
```

Phase 10 실 driver:
- POST `/workspaces` → workspace 생성
- POST `/workspaces/{id}/exec` per command (RemoteExecutor interface)
- DELETE `/workspaces/{id}` teardown

## 선택 가이드

| 시나리오 | 권장 backend |
|---|---|
| 단일 사용자 dev workstation | `LOCAL_NATIVE` |
| 신뢰 안 되는 workspace | `LOCAL_SANDBOX` (Linux) / `DOCKER` |
| 원격 배포 환경 작업 | `SSH` |
| GPU 필요 | `MODAL` 또는 `DAYTONA` |
| 격리 + 단명 | `DOCKER` |
| 격리 + 영속 | `DAYTONA` |
