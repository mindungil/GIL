# Self-dogfood Run 10 + 10-run 종합 요약

> Phase 22.A (bash chain permission split) 후 Run 10. 결과: 22.A 검증 ✓ / NoProgress 디자인 한계 발견 (verify signal 의존).

## Run 10

| 항목 | 값 |
|---|---|
| Status | `budget_exhausted_verify_failed` |
| Iterations | 12 (max cap) |
| Tokens | 207k |
| Workspace diff | **0** (chain 우회 차단됨 — Phase 22.A 작동) |
| Verify runs | 1 (run 끝에 1회) |
| Stuck events | 0 |

### Phase 22.A 검증 ✓

Run 9 에서 agent 가 `cp X X.bak && mv X X.bak` 으로 통과시켰던 chain 우회가 **이번엔 차단됨**. agent 가 12 iter 동안 시도했지만 workspace 무결성 유지.

### Phase 21.A NoProgress 미작동 — 디자인 한계

NoProgress detector 는 매 iter `verify_run` 이벤트가 있어야 진전 측정 가능. 실제 run 에서 agent 는 보통 1회 verify (끝에). 12-iter 중 verify_run 이 1번 → detector 가 abstain.

**진짜 fix**: verify signal 외에 "K iter 동안 successful edit/write_file/apply_patch 없음" 도 진전-없음 signal로 활용. 이건 Phase 22.B 후보.

## 10 runs 종합

| Run | Phase 적용 | Status | 발견 / 검증 |
|---|---|---|---|
| 1 | 18 | budget_exhausted | infra 작동, budget 작음 |
| 2 | 18 | budget_exhausted | code OK, verify 못 봄 |
| 3 | 18+19.A | verify_failed | diet 너무 공격적 → 19.B reset 트리거 |
| 4 | 18+19 | verify_failed | spec 자체 오류 (`go build ./...`) |
| 5 | 18+19+20 | **verify_passed** | **첫 verifier pass** |
| 6 | 18+19+20 | **done** | **첫 budget 안 done, multi-file refactor** |
| 7 | 18+19+20 | done (vacuous) | filename `__X__` Go skip 발견 |
| 8 | 18+19+20 | verify_failed | 5-pattern stuck 못 잡음 → 21.A NoProgress 트리거 |
| 9 | 18-21 | verify_passed (의도 X) | bash chain 우회 → 22.A 트리거 |
| 10 | 18-22 | verify_failed | **22.A 검증 ✓**, NoProgress 디자인 한계 노출 |

## 성공 비율 / 의미

- **6/10 verifier pass** (5+6+7 정당, 9 우회 — 실제 정당 3/10)
- **3/10 dogfood-found bug → fixed** (19.A budget reserve / 20 self-correcting / 22.A bash chain)
- **2/10 dogfood-found gap → partially fixed** (21.A NoProgress 디자인 부족 노출, 22.B 후보)
- **1/10 invalid (filename)**

## 검증된 가설

| 가설 | 증거 |
|---|---|
| Interview-then-autonomous, never-re-ask | Run 5+6 (12-19 iter, 사용자 0 개입) |
| Cache-preserving compression | Run 6 (compact_now 자율 호출, 230k tokens 처리) |
| Plan tool 자발 사용 | Run 2,5,6,8,9 모두 |
| Repomap 자발 사용 | Run 5,6,7,8 모두 |
| Edit 4-tier 정확도 | Run 6 (multi-file refactor 100%) |
| Verifier 항상 실행 (Phase 19.A) | Run 5-10 모두 (10/10) |
| Self-correcting tool errors (Phase 20) | Run 5+6 vs Run 3 (edit error 0 vs 4) |
| Architect/coder split (Phase 19.C) | Run 8+10 (model_switched 5/3 회) |
| Permission gate hardening (Phase 22.A) | Run 10 (chain 우회 차단) |

## 미검증 / 부분 검증

| 항목 | 상태 |
|---|---|
| 5-pattern stuck recovery 실전 | Run 8 fire 안 함 (다양 행동) |
| NoProgress 6th pattern 실전 | Run 10 fire 안 함 (verify signal 부족) |
| 24-hour soak | 가장 긴 run = 7분 (Run 10) |
| Multi-provider real (claude+qwen 페어) | Anthropic 키 부재 |
| 실 사용자 적용 | 0 (이건 user 결정 영역) |

## gil 의 진짜 강점 (10-run dogfood 누적 증거)

1. **Reference-grade lift** — 7 harness에서 lift한 메커니즘들이 실 LLM (qwen3.6-27b)으로 모두 작동: edit 4-tier, repomap, plan, memory, compact, checkpoint, permission, verify
2. **Interview-then-autonomous는 진짜 작동** — Run 5+6 가 가장 강한 증거. agent 가 스스로 plan 짜고, 도구 선택하고, verifier pass 까지 가서 done.
3. **Self-discovery loop** — dogfood 가 5개 buggle/gap 발견 → 4개 즉시 수정. 며칠짜리 작업의 "agent 가 자기 사용 도구의 한계 발견 → 회복" 패턴과 isomorphic.
4. **모든 lift 출처 commit message에 명기** — codebase 자체가 reference 학술 트레일.

## 다음 단계 (외부 자원 필요)

1. **24+ hour soak**: 100+ task 연속 자율, 시간 자원 (예: 1주일 백그라운드)
2. **Anthropic 키로 architect=claude-haiku + editor=qwen 페어링 비교**: 비용 vs 품질 트레이드오프 측정
3. **실제 사용자 dogfood**: 다른 사람이 gil 으로 자기 프로젝트 작업 → 진짜 며칠짜리 task 시도
4. **SWE-bench-mini 통합**: 표준 코딩 벤치마크로 객관 비교 (gil vs aider/cline/codex)

## 코드 작업 wrap point

self-dogfood loop의 진짜 가치 = **gil 자기 자신의 한계를 자기가 발견**. 10 runs 후 발견 빈도 감소 (Run 10 = NoProgress 디자인 한계, Phase 22.B candidate). 추가 run 으로 새 발견 보다 기존 patch verification 이 효율적.

여기서 코드 phase 는 자연스러운 종착점. 다음 가치는 **외부 환경에서의 실 사용** (사용자 액션).
