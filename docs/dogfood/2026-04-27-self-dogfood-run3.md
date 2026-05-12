# Self-dogfood Run 3 — Phase 19 효과 검증

> Run 1+2에서 발견한 3가지 이슈 (verifier reserve, system prompt diet, architect/coder split) 를 Phase 19로 fix한 후 더 큰 task로 재시도. 결과: **Phase 19.A 정확히 작동, Phase 19.B 너무 공격적이었음 발견**.

## Setup

| 항목 | Run 2 | Run 3 |
|---|---|---|
| budget | 400k tokens, no reserve | 300k tokens, reserve=12k |
| Task | summary.go 한 파일 | status.go + status_render.go (multi-file) |
| diet | 적용 안 됨 (Phase 19 전) | 기본 (50% cut) |
| Provider | qwen3.6-27b | qwen3.6-27b (동일) |

## 결과 요약

| 항목 | 결과 |
|---|---|
| Iterations | 14 |
| Tokens | 306,169 (budget=300,000 — exceeded) |
| Status | **`budget_exhausted_verify_failed`** ← Phase 19.A 새 status |
| Verifier | **실행됨** (둘 다 fail로 보고) |
| Wall time | 2분 11초 |
| Code 변경 | 의도하지 않은 `tests/dogfood/qwen_smoke/go.mod` 만 수정 (target file 미변경) |

## Phase 19.A 검증 ✓

**Reserve mechanism이 정확히 작동했음**:
1. budget exhaust 직전에 reserve 12k 가드가 트리거
2. final verify 실행됨 (이전 dogfood에서는 verify 못 돌고 그냥 종료)
3. 새 status `budget_exhausted_verify_failed` 가 정확히 보고됨
4. `Verify results: ✗ build (exit=1) / ✗ cli-tests (exit=1)` 사용자가 정확한 상태 알 수 있음

이전 Run 2에서 "코드 변경은 됐는데 verify 결과 모름" 케이스 사라짐.

## Phase 19.B (system prompt diet) 한계 발견 ✗

Agent의 trajectory를 보면 ([events 분석 18 tool_calls]):

| # | Tool | 정확함? |
|---|---|---|
| 1 | repomap | ✓ Phase 18.A 자발적 사용 |
| 2 | bash find | ✓ |
| 3-8 | read_file × 6 (status.go, status_render.go, summary.go, summary_test.go, status_test.go) | ✓ 정확한 파일 식별 |
| 9 | bash grep | ✓ "more" 패턴 검색 |
| 10-12 | bash (build + test 미리 검증) | ✓ |
| 13 | plan (1 item) | ✓ |
| **14** | **edit attempt 1** | **✗ "missing filename for SEARCH block"** |
| **15** | **edit attempt 2** | **✗ "no such file: /tmp/.../path: cli/.../status_render.go" — path: 가 잘못된 위치** |
| **17** | **apply_patch attempt 1** | **✗ "first line must be '*** Begin Patch'"** |
| **18** | **apply_patch attempt 2** | **✗ "invalid hunk at line 28"** |

**원인**: Phase 19.B에서 system prompt의 `Available tools:` 블록을 제거. 이게 13 tools × ~50 tokens = ~650 tokens 절감했지만, qwen3.6-27b 가 edit / apply_patch 형식 nuance (path: 위치, *** Begin Patch 헤더) 를 못 외움. Anthropic Claude 같은 강한 모델은 schema description으로 충분하지만 약한 모델은 system prompt 의 명시적 가이드가 도움이 됨.

## 잘 한 것 ✓

1. **Verifier reserve + 항상 실행** — 사용자가 "어디까지 갔나" 정확히 앎
2. **새 status taxonomy** (`done` / `budget_exhausted_verify_passed` / `budget_exhausted_verify_failed`) — 며칠 작업 결과 분류 정확
3. **repomap 자발적 사용** — Phase 18.A 검증
4. **Plan 도구 자발적 사용** — Phase 18.A 검증 (Run 2에 이어 두 번째)
5. **정확한 task 이해** — read_file 6번으로 정확한 파일들 식별

## 부족한 것 ✗

1. **diet 너무 공격적** — qwen 같은 약한 모델에 edit/apply_patch 도구 형식 가이드 부족
2. **Edit tool error 메시지 비-self-correcting** — `"missing filename"` 만으론 어떻게 고치는지 불명확
3. **반복 실패 후 대안 도구 시도 못함** — edit 2번 fail → apply_patch 시도했지만 둘 다 형식 불일치 → 다른 우회 (write_file로 통째 교체) 시도 안 함

## 발견된 gil 자체 issue (Phase 20 candidate)

### A. Diet 재조정 (Phase 19.B partial revert)

`Available tools:` 블록은 약한 모델에게 critical. 옵션:
- 기본은 verbose 유지, `minimal: true` 옵션에서만 제거
- 또는 tool 이름만 + 도구별 1줄 hint (예: `edit: SEARCH/REPLACE blocks with 'path: <file>' prefix`)

### B. Edit tool error 메시지 self-correcting

현재 `"missing filename for SEARCH block"` →
새: `"missing filename for SEARCH block. Format: '<filename>\n<<<<<<< SEARCH\n<old>\n=======\n<new>\n>>>>>>>>'. The 'path:' word goes BEFORE the SEARCH marker, not as a label."`

이러면 같은 실수 반복 안 함.

### C. Edit tool more lenient

`path: <file>` 도 허용 (codex apply_patch 와 같은 prefix 패턴).

## 의미 있는 결론

**Phase 19.A는 실전 검증 통과** — verifier reserve + always-verify가 정확히 작동.

**Phase 19.B는 모델별 균형 필요** — 강한 모델 (Claude, GPT-4) 에는 적절하지만, 약한 로컬 모델 (qwen 27B) 에는 verbose 가이드가 필요. spec.run.systemPrompt 옵션을 모델별로 다르게 설정 가능하게 함이 적합.

**Phase 19.C (architect/coder split)** 는 Run 3에서 미사용 (단일 모델 setup) — 다음 dogfood에서 검증 예정 (claude-haiku planner + qwen editor 같은 페어).

## 다음 단계 (Phase 20 candidate)

1. Edit tool error 메시지 self-correcting (B)
2. Diet 모델별 자동 적용 — `core/runner` 가 provider 이름 보고 default `minimal:false` (anthropic) vs `false` + verbose tool block (vllm/local) 결정
3. claude-haiku planner + qwen editor 페어 dogfood (Phase 19.C 검증)

## Reproduce

```bash
# (same as Run 1+2 setup, with budget=300k + reserveTokens=12000)
gil run $ID --provider vllm --model qwen3.6-27b
```
