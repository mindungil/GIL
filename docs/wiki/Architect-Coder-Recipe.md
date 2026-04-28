# Architect / Coder Recipe

Phase 19.C 추가. 강한 모델 + 싼 모델 페어링으로 비용 vs 품질 트레이드오프.

## 동기

며칠짜리 자율 실행에서:
- **Plan turn** (1번째 + plan tool 호출 직후) — 강한 사고 필요
- **Edit turn** (bash/edit/write_file 만 호출) — 싼 모델로 충분
- **Ambiguous turn** — default

## spec.yaml

```yaml
models:
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6     # 강한 — plan turn
  editor:
    provider: anthropic
    modelId: claude-haiku-4-5      # 싼 — edit turn
  main:
    provider: anthropic
    modelId: claude-haiku-4-5      # default fallback
```

## Routing 자동

```go
classifyTurn(iterIdx, lastResponse) -> role:
  iterIdx == 0:                       -> "planner"  // 첫 turn
  hasPlanToolCall(lastResponse):      -> "planner"  // plan 다듬는 turn
  hasOnlyExecTools(lastResponse):     -> "editor"   // bash/edit/write_file/apply_patch만
  default:                            -> "main"
```

`exec` tool names: `bash`, `edit`, `write_file`, `apply_patch`, `read_file`, `memory_update`.
`subagent`, `repomap`, `web_search`, `lsp`, `plan` 은 `main` 으로 (investigation turn).

## 이벤트

`model_switched` event 매 role 변경 시 발생:
```json
{"from":"planner","iter":2,"model":"claude-haiku-4-5","reason":"tool_heavy","to":"editor"}
{"from":"editor","iter":14,"model":"claude-sonnet-4-6","reason":"ambiguous_turn","to":"main"}
```

## 비용 비교 (이론)

가정: 30 turn, 5 plan turn / 20 edit turn / 5 main turn, turn당 average ~10k input + 3k output.

| 시나리오 | 토큰 분포 | 비용 (USD) |
|---|---|---|
| All sonnet | 30×13k = 390k | ~$1.20 |
| All haiku | 30×13k = 390k | ~$0.40 |
| Architect+coder | 5×13k sonnet + 25×13k haiku | ~$0.60 |

Architect+coder 는 sonnet 단독 50% 절감 + haiku 단독 50% 비싸지만 plan 품질 ↑.

## per-role 비용 보고

```bash
gil cost <id> --output json
# {
#   "by_role": [
#     {"role": "planner", "calls": 5, "tokens": 65000, "cost_usd": 0.40},
#     {"role": "editor",  "calls": 20, "tokens": 260000, "cost_usd": 0.18},
#     {"role": "main",    "calls": 5, "tokens": 65000, "cost_usd": 0.05}
#   ],
#   "total": { ... }
# }
```

## 검증

Self-dogfood Run 8: model_switched 5회 자율 발화. Run 10: 3회. Architect/coder split 자체 작동 확인. 진짜 비용 비교는 Anthropic 키 dogfood 후 측정 (Phase 16+).

## Multi-provider 페어링

```yaml
models:
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6
  editor:
    provider: vllm
    modelId: qwen3.6-27b           # 자체 GPU = $0
  main:
    provider: openrouter
    modelId: google/gemini-2.5-pro  # 변형
```

Provider 다르더라도 OK (자동 connection caching).
