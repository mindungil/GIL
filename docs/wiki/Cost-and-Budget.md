# Cost & Budget

## Budget hard caps

`spec.run.budget`:
```yaml
budget:
  maxIterations: 30          # turn 수 한도
  maxTotalTokens: 500000     # 누적 input + output
  maxTotalCostUSD: 5.00      # USD 한도
  reserveTokens: 20000       # 마지막 verifier 위한 reserve (Phase 19.A)
```

어느 한도라도 초과 시 `Result.Status = "budget_exhausted_*"` 로 의도적 중단.

## 새 status taxonomy (Phase 19.A)

| Status | 의미 |
|---|---|
| `done` | end_turn + verifier ✓ |
| `verify_failed` | end_turn but verifier ✗ |
| `budget_exhausted` | budget hit, verifier 결과 없음 |
| `budget_exhausted_verify_passed` | budget hit but verifier ✓ (best-effort 성공) |
| `budget_exhausted_verify_failed` | budget hit + verifier ✗ |
| `max_iterations` | iter cap 도달 |
| `stuck` | stuck recovery 4 strategies 모두 미회복 |

## 모델별 비용 카탈로그

`core/cost/default_catalog.json` (embedded):

| Model | input/M | output/M |
|---|---|---|
| claude-opus-4-7 | $15 | $75 |
| claude-opus-4-6 | $15 | $75 |
| claude-sonnet-4-6 | $3 | $15 |
| claude-haiku-4-5 | $1 | $5 |
| gpt-4o | $2.5 | $10 |
| gpt-4o-mini | $0.15 | $0.60 |
| qwen3.6-27b | $0 | $0 (로컬) |

## 모니터링

```bash
gil cost <id>              # 단일 세션 cost
gil cost <id> --output json
gil stats                  # 누적 (--days N)
gil stats --output json
```

## Architect/Coder 페어링 (Phase 19.C)

비용 절감:
```yaml
models:
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6     # 강한 모델 (계획 turn)
  editor:
    provider: anthropic
    modelId: claude-haiku-4-5      # 싼 모델 (실행 turn)
```

`gil cost` 가 per-role breakdown 표시. Phase 19.C 검증: Self-dogfood Run 8 + Run 10 에서 model_switched events 자율 작동.

## TUI 라이브 미터 (Phase 15.F)

TUI Spec & Progress pane:
- 75% 도달 시 amber 색상 + "approaching limit" warning
- 100% 도달 시 coral 색상 + "EXHAUSTED — run stopped"

`gil watch <id>` 도 `▲ +$0.04 / min` 비용 변화율 표시.
