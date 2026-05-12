# Operations

운영 시 알아야 할 것 — workspace backends + verifier + stuck recovery.

## Workspace Backends

`spec.workspace.backend`:

| Backend | 무엇 | 요구사항 |
|---|---|---|
| `LOCAL_NATIVE` | 직접 실행 (default) | — |
| `LOCAL_SANDBOX` | bwrap (Linux) | bwrap installed |
| `DOCKER` | per-command `docker exec` | docker daemon |
| `SSH` | ssh + rsync | ssh + rsync |
| `MODAL` | Modal cloud sandbox (CLI shell-out) | `MODAL_TOKEN_*` + modal CLI |
| `DAYTONA` | Daytona workspace (REST API) | `DAYTONA_API_KEY` |

### 선택 가이드

| 시나리오 | 권장 |
|---|---|
| 단일 사용자 dev workstation | `LOCAL_NATIVE` |
| 신뢰 안 되는 workspace | `LOCAL_SANDBOX` (Linux) / `DOCKER` |
| 원격 배포 환경 작업 | `SSH` |
| GPU 필요 | `MODAL` 또는 `DAYTONA` |
| 격리 + 단명 | `DOCKER` |
| 격리 + 영속 | `DAYTONA` |

### LOCAL_SANDBOX 모드

bwrap 3 모드:
- `ReadOnly` — research only
- `WorkspaceWrite` — workspace 쓰기 OK, 외부 RO (default)
- `FullAccess` — 호스트 전체 (위험)

### DOCKER

`workspace.path = "alpine:latest"` 같은 이미지 지정. RunService가 `docker run -d --rm --name gil-<id>` 후 per-command `docker exec`.

### SSH (Phase 9 — file ops local 제한 해소)

`workspace.path = user@host[:port][/keypath]`. RunService가 push 전 / pull 후 rsync.

### MODAL (Phase 10 진짜 driver)

ephemeral Python manifest 생성 → `modal run <manifest>::exec_in_sandbox` per command → Teardown `modal app stop`.

### DAYTONA (Phase 10 진짜 driver)

REST API. `core/tool.RemoteExecutor` interface 통해 bash가 `exec.Cmd` 우회 + HTTP 직접 호출.

## Verifier

`spec.verification.checks` — shell assertion runner.

```yaml
verification:
  checks:
    - name: build
      kind: SHELL
      command: go build ./...
    - name: tests
      kind: SHELL
      command: go test ./...
    - name: lint
      kind: SHELL
      command: golangci-lint run --timeout 5m
```

각 check:
- exit code 기반 (0 = pass)
- per-check 60s timeout
- stdout/stderr 4KB 캡

### Phase 19.A — Reserve + always-final-verify

`budget.reserveTokens` (default `min(8000, maxTotalTokens/10)`) 가 verifier를 위해 예약. budget exhausted 직전에 reserve guard 트리거 → final verify 항상 실행 → 결과 정확 보고.

이전엔 budget hit 시 verifier 못 봄. 지금은 11/11 dogfood run 모두 verifier 결과 정확.

## Stuck Recovery

며칠 자율 실행의 핵심 안전망. **6 detector patterns + 4 recovery + 3-strike abort**.

### 6 patterns

| Pattern | 발화 조건 |
|---|---|
| `RepeatedAction` | 같은 tool 호출 연속 (OpenHands lift) |
| `RepeatedActionError` | 같은 tool + 같은 에러 |
| `Monologue` | 도구 안 쓰고 텍스트만 |
| `PingPong` | 두 행동 alternating |
| `ContextWindow` | context 한계 임박 |
| `NoProgress` | K iter 동안 verifier stalled + file churn or empty (Phase 21.A + 22.B, **gil 자체**) |

### NoProgress (가장 중요)

5 OpenHands pattern은 모두 REPEATED action 가정. 실제 stuck은 "다양한 행동인데 진전 0":
- threshold 4 iter
- verify_run 신호 있으면 점수 stalled 검사
- verify_run 없어도 successful edits 0 이면 fire (Phase 22.B fallback)

Self-dogfood Run 11 검증: 6 iter early abort / 54k tokens (Run 8 195k 대비 72% 절감).

### 4 recovery strategies

| Strategy | 전략 |
|---|---|
| `ModelEscalate` | 강한 모델로 1 turn 재시도 |
| `AltToolOrder` | system prompt에 "use different approach" 단발 hint |
| `ResetSection` | shadow git을 second-newest checkpoint으로 hard reset |
| `AdversaryConsult` | 별도 LLM 호출 → 1줄 제안 → next turn 시스템 노트로 |
| `SubagentBranch` | read-only sub-loop으로 정찰 |

각 한 번씩 시도. 모두 실패 → 3-strike abort → status=`stuck`.

### Diagnostic

```bash
gil events <id> --filter milestones,errors
# stuck_detected events: pattern + detail
gil restore <id> <step>   # 좋았던 시점으로 복원
```

## Checkpoint (Shadow Git)

매 tool-using iter + 최종 done 시점에 자동 commit. 워크스페이스 외부의 별도 .git (`<sessionsDir>/<id>/shadow/<hash>/.git`). 사용자 repo는 무오염.

```bash
gil restore <id> <step>
# step=1 (oldest), -1 (latest), 또는 번호
```

running 세션은 restore 거부 (FailedPrecondition).
