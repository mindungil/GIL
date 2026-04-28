# Anthropic dogfood runbook

> 첫 진짜 Anthropic-driven dogfood 실행 절차. mock + qwen 으로 11 dogfood run 검증 완료된 시점 (2026-04-28). 다음은 Anthropic 키로 실 모델 (Claude Opus / Sonnet / Haiku) 검증.

## 사전 조건

- `gil` 빌드 완료 (`make build` 또는 `make install`)
- ANTHROPIC_API_KEY 발급 (https://console.anthropic.com/)
- 네트워크 접근
- 작업할 워크스페이스 (별도 디렉토리 권장 — 격리)

## 비용 추정 (2026-04 기준 list price)

| 모델 | 입력 / 백만 | 출력 / 백만 | 1 dogfood 추정 |
|---|---|---|---|
| `claude-opus-4-7` | $15 | $75 | $0.50 - $5 (task 크기) |
| `claude-sonnet-4-6` | $3 | $15 | $0.10 - $1 |
| `claude-haiku-4-5` | $1 | $5 | $0.04 - $0.40 |

**권장 시작점**: claude-haiku-4-5. 작은 task 1개 ≈ $0.10. Phase 19.A reserve + Phase 22.A bash chain 적용 후 안전.

## 절차

### 1. 신규 GIL_HOME 으로 시작 (격리)

기존 sessions 데이터와 섞이지 않도록:

```bash
export GIL_HOME=$HOME/.config/gil-anthropic-test
mkdir -p $GIL_HOME
gil init --no-auth --no-config
```

### 2. 자격증명 등록

```bash
gil auth login anthropic
# Prompt: API key (echo off, terminal 특수문자 OK)
# 결과: $GIL_HOME/config/auth.json (mode 0600)
```

검증:
```bash
gil auth list   # anthropic | api | sk-ant-...XXXX | ...
```

### 3. 워크스페이스 + spec 준비

**옵션 A — 인터뷰 통해 spec 작성** (권장 첫 사용자):
```bash
mkdir -p ~/dogfood-task && cd ~/dogfood-task
SESSION=$(gil new --working-dir $(pwd) | awk '{print $NF}')
gil interview $SESSION --provider anthropic --model claude-sonnet-4-6
# Sonnet으로 인터뷰 (Q&A는 Sonnet이 Haiku보다 나음)
# saturation까지 대화. 마지막에 spec freeze 확인
```

**옵션 B — spec.yaml 직접 작성** (e2e 테스트 패턴):
참고: `tests/e2e/phase18_plan_test.sh` 의 spec.yaml 형식.

### 4. 자율 실행

```bash
gil run $SESSION --provider anthropic --model claude-haiku-4-5 --detach
gil watch $SESSION   # 라이브 진행률
# 또는
giltui   # 4-pane mission control
```

### 5. 결과 검증

```bash
gil status $SESSION   # 최종 status
gil cost $SESSION     # 토큰 + USD
gil events $SESSION --tail  # 이벤트 로그
gil export $SESSION --format markdown > /tmp/run-report.md
```

### 6. Architect/coder split (Phase 19.C)

비용 vs 품질 트레이드오프:

```yaml
# spec.yaml
models:
  main:
    provider: anthropic
    modelId: claude-haiku-4-5    # default 모델 (싸다)
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6   # 계획 turn에만 (Sonnet)
  editor:
    provider: anthropic
    modelId: claude-haiku-4-5    # edit turn (싸다)
```

→ 1 task 비용: ~$0.30 (Sonnet planning ~5 turns × $0.05 + Haiku editing ~15 turns × $0.02).

### 7. 종료 처리

```bash
gil session list   # 완료된 세션 확인
gil cost --json > /tmp/dogfood-costs.json   # 비용 추적
gil session rm $SESSION --yes   # cleanup (선택)
```

## 첫 dogfood task 후보 (단순 → 복잡)

1. **Trivial 검증** — `echo hello > out.txt` + verify `test -f out.txt`. 10 turn 안에 끝남, $0.05 미만.
2. **Single file edit** — 작은 Go/Python 함수 추가 + `go test` / `pytest` verify. ~$0.10.
3. **Multi-file refactor** — gil 자기 자신의 self-dogfood Run 6 같은 task. ~$0.30.
4. **실 사용자 task** — 본인 프로젝트의 작은 backlog 항목. 비용은 task 크기 따라.

## Phase 19/20/22 안전망 동작 확인

처음 dogfood 시 다음을 확인:
- ✓ Run start 시 system prompt diet 적용 (anthropic = compact)
- ✓ Verifier reserve 8000 토큰 자동 적용 (`gil run --budget-tokens 0` 미설정 시 무한)
- ✓ ASK_DESTRUCTIVE_ONLY 가 bash chain (`mv && rm` 등)도 정확히 차단

이상 발생 시 세션 ID + events.jsonl 첨부 후 GitHub issue.

## 비용 폭주 방지

```yaml
# spec.yaml — hard cap
budget:
  maxIterations: 20
  maxTotalTokens: 100000        # = $1.50 max with Sonnet
  maxTotalCostUSD: 2.00
  reserveTokens: 8000
```

`max_total_cost_usd` 도달 시 `budget_exhausted` 정확 보고 (Phase 19.A).

## 알려진 제약

- 첫 인터뷰 turn은 saturation 도달까지 5-10 questions 일반적 — Sonnet 가장 자연스러움
- `--detach` 후 `gil watch` 로 모니터 권장 (며칠짜리 작업은 백그라운드)
- 실패 시 Shadow Git checkpoint 으로 항상 복원 가능: `gil restore $SESSION <step>`

## 첫 dogfood 후 보고

다음 dogfood 결과 파일 작성 권장: `docs/dogfood/2026-XX-anthropic-first-real-run.md`
- task 한 줄 요약
- 사용 모델 + 비용
- 토큰 사용량
- Verifier 통과 여부
- 발견된 gil 자체 issue (있으면)
- 다음 phase candidates

## 참고

- 11 self-dogfood run (qwen3.6-27b) 결과: `docs/dogfood/2026-04-28-self-dogfood-run11-stuck-validated.md` 및 sibling files
- gil 의 모든 안전망 (verifier reserve, stuck recovery, bash chain hardening) 검증 완료된 시점에서의 첫 Anthropic 시도
