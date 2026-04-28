# Self-dogfood Reports

11 self-dogfood runs (qwen3.6-27b on user-provided OpenAI-compat endpoint).

## Summary 테이블

| Run | 작업 | Status | Iters | Tokens | 메시지 |
|---|---|---|---|---|---|
| 1 | "Cap gil no-arg session list at 10" (1차) | budget_exhausted | 10 | 169K | budget 너무 작음 |
| 2 | 같은 task | budget_exhausted | 19 | 404K | 코드 OK but verifier 못 봄 → Phase 19.A 트리거 |
| 3 | "Same cap to gil status visual mode" | budget_exhausted_verify_failed | 14 | 306K | diet 너무 공격적 → Phase 20 트리거 |
| 4 | 같은 task | budget_exhausted_verify_failed | 19 | 360K | spec 자체 오류 (사용자 작성) |
| 5 | 같은 task (Phase 20 후) | **budget_exhausted_verify_passed** ✓ | 15 | 351K | **첫 verifier pass** |
| 6 | DRY refactor (overflow helper) | **`done`** ✓ | 11 | 230K | **첫 budget 안 done** |
| 7 | invalid (filename `__X__test.go` Go skip) | done (vacuous) | 5 | 70K | filename bug 발견 |
| 8 | impossible test (stuck validation) | budget_exhausted_verify_failed | 12 | 195K | 5-pattern stuck 못 잡음 → Phase 21.A 트리거 |
| 9 | 같은 impossible task (Phase 21.A 후) | budget_exhausted_verify_passed (우회) | 10 | 217K | bash chain 우회 발견 → Phase 22.A 트리거 |
| 10 | 같은 task (Phase 22.A 후) | budget_exhausted_verify_failed | 12 | 207K | NoProgress verify 의존 → Phase 22.B 트리거 |
| **11** | **같은 task (Phase 22.B 후)** | **`stuck`** | **6** | **54K** | **모든 안전망 EM2EM 검증** |

## 검증된 가설

| 가설 | 증거 |
|---|---|
| Interview-then-autonomous, never re-ask | Run 5+6 (12-19 iter, 사용자 0 개입) |
| Cache-preserving compression | Run 6 (compact_now 자율 호출) |
| Plan tool 자발 사용 (Phase 18.A) | Run 2,5,6,8,9 모두 |
| Repomap 자발 사용 (Phase 18.B/A) | Run 5,6,7,8 모두 |
| Edit 4-tier 정확도 (Aider lift) | Run 6 multi-file refactor 100% |
| Verifier 항상 실행 (Phase 19.A) | 11/11 run |
| Self-correcting tool errors (Phase 20) | Run 5+6 vs Run 3 edit error 0 vs 4 |
| Architect/coder split (Phase 19.C) | Run 8+10 (model_switched 5/3 회) |
| Bash chain hardening (Phase 22.A) | Run 10 (chain 우회 차단) |
| **Stuck recovery EM2EM (Phase 21+22.B)** | **Run 11 (3-strike abort, status=stuck)** |

## Dogfood-found bugs (모두 fix)

1. **Budget reserve 부재** (Run 1+2) → Phase 19.A
2. **System prompt diet 약한 모델 부족** (Run 3) → Phase 20.B
3. **Edit/apply_patch error 비-self-correcting** (Run 3) → Phase 20.A
4. **5-pattern stuck 못 잡음** (Run 8) → Phase 21.A NoProgress
5. **bash chain permission 우회** (Run 9) → Phase 22.A
6. **NoProgress verify-signal 의존** (Run 10) → Phase 22.B fallback

## Run 11 핵심 검증 단일 run

| 메커니즘 | 결과 |
|---|---|
| Phase 21.A NoProgress detector | 3 fires |
| Phase 22.B verify-independent fallback | "no verify run + no edits over 4 iters" 정확 |
| 4 Recovery strategies | 3-strike 모두 미회복 |
| 3-strike abort policy | `stuck_unrecovered` emit |
| Phase 19.A always-final-verify | abort 후도 1회 verify (✗ 정직) |
| Phase 22.A bash chain | 0 workspace diff (우회 차단) |

**효율 누적**: Run 8 (195k tokens, 12 iter, status 모호) → Run 11 (**54k tokens, 6 iter, status=`stuck` 정확**) = **72% 토큰 절감**.

## 자세한 보고서 (각 run 별)

`docs/dogfood/` 디렉토리:
- `2026-04-27-first-real-run-qwen.md` — Run 1+2 (어댑터 검증)
- `2026-04-27-self-dogfood-result.md` — Run 1+2 분석
- `2026-04-27-second-run-end-to-end.md` — mock end-to-end
- `2026-04-27-self-dogfood-run3.md`
- `2026-04-27-self-dogfood-run5.md` — 첫 verify pass
- `2026-04-27-self-dogfood-run6.md` — 첫 budget 안 done
- `2026-04-27-self-dogfood-runs-7-8.md` — stuck 패턴 발견
- `2026-04-27-self-dogfood-run9.md` — bash chain 우회 발견
- `2026-04-27-self-dogfood-run10-and-summary.md` — Phase 22.A 검증
- `2026-04-28-self-dogfood-run11-stuck-validated.md` — 모든 safety net 검증

## 미검증 (외부 자원 필요)

- 24+ hour soak — 가장 긴 run = 7분
- Anthropic 키 페어링 (claude-haiku planner + qwen editor)
- 외부 사용자 dogfood
- SWE-bench 등 표준 벤치마크
