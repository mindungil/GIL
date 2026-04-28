# gil — user guide

> 처음 gil 을 자기 프로젝트에 적용하려는 사용자를 위한 안내서. README 의 quickstart 가 "5 분 안에 돌리기" 목표라면 이 문서는 "내 프로젝트에서 며칠짜리 자율 실행을 켜놓기" 목표.

## 누구를 위한 문서인가

- **자기 프로젝트의 backlog 작업을 LLM 으로 자율 실행하고 싶은 개발자**
- 기존에 Claude Code / Cursor / Cline / aider 같은 도구를 써봤지만 "도중에 사용자에게 묻거나 미완성으로 끝나는" 패턴에 지친 사용자
- 며칠짜리 작업 (예: large refactor, multi-package upgrade, test infrastructure 정비) 을 백그라운드로 돌리고 싶은 사용자

## gil 의 핵심 약속

1. **인터뷰는 길지만 한 번** — saturation 까지 모든 슬롯을 채움. 며칠짜리 작업의 spec 이 freeze 되기 전 모든 ambiguity 해소.
2. **시작 후 다시 묻지 않음** — `clarify` 도구는 안전 valve로만 (드물게). 일반적으로 agent 가 모든 결정 자율.
3. **객관적 종료 조건** — verifier 통과 + stuck 회복 끝 + budget exhausted = 종료. agent 자기 자신이 "끝"이라고 선언 못 함.
4. **캐시 보존 며칠 지속** — Hermes pattern compaction. prompt cache prefix 가 깨지지 않음.

## 5분 셋업

```bash
# 1. Install
git clone https://github.com/mindungil/GIL.git && cd GIL
make install   # → /usr/local/bin/{gil,gild,giltui,gilmcp}

# 2. First-run scaffolding (XDG dirs + auth login)
gil init

# 3. Verify
gil doctor   # 모든 row OK 면 준비 완료

# 4. Start
SESSION=$(gil new --working-dir ~/myproject | awk '{print $NF}')
gil interview $SESSION   # 인터뷰 시작
gil run $SESSION --detach
gil watch $SESSION        # 또는: giltui
```

## Provider 선택

| Provider | 강점 | 비용 (참고) | gil 셋업 |
|---|---|---|---|
| anthropic | 가장 강한 instruction-following + tool use | claude-haiku $1/M, sonnet $3/M, opus $15/M | `gil auth login anthropic` |
| openai | gpt-4o tool use 좋음 | $2.5/M (gpt-4o-mini) | `gil auth login openai` |
| openrouter | 다양한 모델 (Llama, DeepSeek, Qwen, Claude proxy) | varies | `gil auth login openrouter` |
| vllm/local | 로컬 GPU 무료 (or 자체 호스팅) | $0 (HW 비용 별도) | `gil auth login vllm --base-url http://...` |

**처음 시도** 권장:
- **anthropic claude-haiku-4-5** — small task ~$0.10, 안정적
- 또는 **vllm + qwen3.6-27b** (자체 GPU 있을 때)

## 첫 task 추천 (작은 → 큰)

### Step 1 — Trivial (5분, 비용 < $0.10)

목적: gil pipeline 정상 작동 확인.

```yaml
goal: "Add a hello.txt file containing today's date"
verification:
  - test -f hello.txt && grep -q "$(date +%Y)" hello.txt
budget: { maxIterations: 5, maxTotalTokens: 30000 }
```

기대: agent 가 `bash` 또는 `write_file` 도구로 1-2 turn 만에 끝. verifier ✓ → status=done.

### Step 2 — Single file edit (15분, 비용 < $0.50)

목적: edit 도구의 4-tier matching + verify integration.

```yaml
goal: "Add a --json flag to <my-cli-tool> that outputs JSON instead of text"
verification:
  - go test ./... (또는 적절한 test runner)
budget: { maxIterations: 15, maxTotalTokens: 150000 }
```

### Step 3 — Multi-file refactor (1시간, 비용 ~$1)

목적: AGENTS.md/CLAUDE.md 트리워크 + plan tool + LSP rename + multi-file edit 통합 검증.

`<your-project>/AGENTS.md` 작성 (gil 이 자동 인식):
```markdown
# Project conventions
- Use Go 1.25+
- Tests in same package, _test.go suffix
- Errors wrapped via fmt.Errorf("...: %w", err)
- ...
```

```yaml
goal: "Extract the duplicated retry logic in handlers/{a,b,c}.go into a shared retry.go helper"
verification:
  - go build ./... && go test ./...
budget: { maxIterations: 30, maxTotalTokens: 500000 }
```

### Step 4 — 며칠짜리 (24h+ 권장, 비용 $5-50)

목적: gil 의 진짜 약속 — "켜놓고 떠나기".

작업 후보:
- 큰 의존성 업그레이드 (예: x.0 → y.0 major version)
- Test infrastructure 정비 (e.g. unit → integration test 도입)
- Code quality cleanup (lint warnings 0 만들기)

```yaml
goal: "..."
verification:
  - go build ./... && go test ./... && golangci-lint run --timeout 5m
budget:
  maxIterations: 200
  maxTotalTokens: 5000000
  maxTotalCostUSD: 30.00
  reserveTokens: 30000
risk:
  autonomy: ASK_DESTRUCTIVE_ONLY  # 또는 FULL (자기 책임)
```

`gil run --detach` 후 `gil watch` 로 가끔 들여다봄. `giltui` 띄워두면 더 명확.

## 자율성 (autonomy) 다이얼

| Level | 의미 | 권장 시나리오 |
|---|---|---|
| `FULL` | 모든 도구 무제한 | 격리된 sandbox / dispose-able workspace |
| `ASK_DESTRUCTIVE_ONLY` | rm/mv/chmod/dd/sudo 만 ask | **기본 권장 — 일반 dev 환경** |
| `ASK_PER_ACTION` | 모든 도구 ask | TUI 와 함께 사용 (interactive supervision) |
| `PLAN_ONLY` | 실행 차단, plan tool만 | "agent가 어떻게 하려는지" 미리 보기 |

**Phase 22.A 적용** (이 시점 안전): bash chain 명령 (`cp x && mv y`) 도 각 sub-command 별로 정확히 평가.

## 며칠짜리 실행 패턴

### 패턴 1 — 격리 워크스페이스

```bash
git clone <my-repo> ~/gil-task-worktree
gil run --working-dir ~/gil-task-worktree
# 작업 끝나고 diff 확인 후 main 으로 cherry-pick
```

### 패턴 2 — git worktree

```bash
git -C ~/myrepo worktree add ~/myrepo-gil-task feature/long-refactor
gil run --working-dir ~/myrepo-gil-task
# 끝나면 worktree 검토 후 PR
```

### 패턴 3 — 원격 호스트 (며칠 백그라운드)

```bash
ssh dev-server
tmux new -d -s gil-dogfood "gil run $SESSION --detach"
# 로컬에서:
ssh dev-server gil watch $SESSION
```

### 패턴 4 — Architect/coder 모델 분리 (Phase 19.C)

비용 절감 + 품질:

```yaml
models:
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6   # 강한 모델로 plan
  editor:
    provider: anthropic
    modelId: claude-haiku-4-5    # 싼 모델로 edit
```

## 진행 모니터링

| 도구 | 용도 |
|---|---|
| `gil status` | 한 줄 요약 |
| `gil watch <id>` | 라이브 진행률 (in-place 업데이트) |
| `gil events <id> --tail --filter milestones` | 핵심 이벤트만 |
| `giltui` | 4-pane mission control TUI |
| `gil cost <id>` | 토큰 + USD |
| `gil status --output json` | JSON 으로 외부 dashboard |

## 문제 발생 시

### budget_exhausted

예산 한도 도달. Phase 19.A reserve 덕분에 verifier는 항상 실행되고 결과 보고됨:
- `budget_exhausted_verify_passed` — 결과는 OK, 다음번 budget 늘리면 됨
- `budget_exhausted_verify_failed` — 부분 진전, 코드는 일부 변경, verifier 안 통과

```bash
gil restore <id> <step>   # 시작 시점 또는 중간 checkpoint 로 복원
gil run <id> --resume     # spec 재freeze 후 더 큰 budget 으로
```

### stuck

3-strike abort. 진짜 막힘. Phase 21.A NoProgress + Phase 22.B verify-independent 검증 끝남:
- agent 가 4 iter 동안 진전 없음 + recovery 4 strategies 모두 실패
- `gil events <id>` 로 stuck pattern 확인 (NoProgress / RepeatedAction / etc)
- 보통: spec 재검토 → 더 명확한 verifier checks 또는 task 분할

### 코드가 망가짐

Shadow Git checkpoint 가 매 iter 자동 저장:
```bash
gil restore <id> <step>   # step=1 (첫), -1 (마지막), 또는 명시적 step 번호
```

## AGENTS.md 활용

`<workspace>/AGENTS.md` 자동 트리워크 (Phase 12). 며칠짜리 작업 전에 작성 권장:

```markdown
# Project conventions for gil

## Code style
- Go 1.25+
- gofmt + goimports
- Errors via cliutil.UserError pattern at user-facing boundaries

## Testing
- _test.go in same package
- Use t.TempDir() for isolation
- Mock external services (no real network in tests)

## Commit style
- feat(scope): ...
- fix(scope): ...
- docs(scope): ...

## Forbidden
- DO NOT add new third-party deps without justification
- DO NOT modify generated proto files manually
```

→ Agent 가 매 turn system prompt 에 자동 prepend. 인터뷰 시간 단축, 코드 일관성 향상.

## 비용 통제

```yaml
budget:
  maxTotalTokens: 1000000        # = ~$3 with claude-sonnet
  maxTotalCostUSD: 5.00          # 절대 한도 (비용 도달 시 즉시 abort)
  reserveTokens: 20000           # 마지막 verifier 위한 reserve
```

`gil cost --json` 으로 실시간 모니터링. 외부 dashboard 와 통합 가능.

## 다중 사용자

```bash
gild --foreground --user alice --base /var/lib/gil
gild --foreground --user bob   --base /var/lib/gil
# 각자 별도 socket / sessions
```

OIDC 인증 (Phase 10):
```bash
gild --foreground --auth-issuer https://auth.example.com --auth-audience gil
```

## 다음 읽기

- `README.md` — 5분 quickstart + 아키텍처
- `docs/design.md` — 전체 설계 narrative
- `docs/dogfood/` — 11 self-dogfood run 결과 (qwen3.6-27b)
- `docs/dogfood/anthropic-runbook.md` — 첫 Anthropic dogfood 절차
- `docs/research/2026-04-26-harness-ux-audit.md` — 다른 7 harness 와 비교
- `docs/research/2026-04-27-harness-capability-audit.md` — capability layer 비교
- `docs/recipes/architect-coder.md` — strong + cheap 모델 페어링
- `SECURITY.md` — threat model + best practices
- `CONTRIBUTING.md` — 개발 흐름 (gitflow)

## 도움 받기

- `gil --help` — 명령어 리스트
- `gil <cmd> --help` — 각 명령어 상세
- `gil doctor` — 환경 진단
- GitHub Issues: https://github.com/mindungil/GIL/issues

## 주의사항

- **pre-1.0**: API 와 on-disk schema 변경 가능. Migration 은 best-effort.
- **dogfood 권장**: 작은 task → 며칠짜리. 갑자기 24h 작업 시도 금지.
- **격리 워크스페이스**: 첫 며칠 작업은 별도 디렉토리 / worktree / sandbox 에서.
