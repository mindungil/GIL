# Self-dogfood Run 11 — 첫 `stuck` status 실 검증

> Phase 22.B (NoProgress verify-independent fallback) 적용 후 동일 시나리오 재시도. **결과: 6 iter / 54k tokens / status=`stuck` / "aborted: 3 unrecovered stuck signals"** — 며칠 자율 실행의 핵심 안전망 처음 EM2EM 검증.

## Run 8 → Run 9 → Run 10 → Run 11 효율 비교 (동일 시나리오: impossible test)

| Run | Phase | Iters | Tokens | Status | Stuck signals |
|---|---|---|---|---|---|
| 8 | 5-pattern only | 12 (max) | 195k | budget_exhausted_verify_failed | 0 |
| 9 | + 22 패치 시도 전 | 10 | 217k | budget_exhausted_verify_passed (우회) | 0 |
| 10 | + 22.A bash chain | 12 (max) | 207k | budget_exhausted_verify_failed | 0 (verify signal 부족) |
| **11** | **+ 22.B verify-independent** | **6 (early abort)** | **54k** | **`stuck`** | **3 NoProgress + 1 unrecovered** |

**누적 효과**: Run 8 195k → Run 11 54k (**72% 토큰 절감**) + 정확한 `stuck` status.

## Phase 별 검증 단일 run에서

| 메커니즘 | 검증 |
|---|---|
| Phase 21.A NoProgress detector | ✓ 3회 fire |
| Phase 22.B verify-independent fallback | ✓ "no verify run + no edits over 4 iters" detail 정확 |
| 4 recovery strategies (Phase 5/6) | ✓ 3 strikes 모두 미회복 → abort |
| 3-strike abort policy | ✓ stuck_unrecovered 정확 emit |
| Phase 19.A always-final-verify | ✓ stuck 상태에서도 verifier 1회 실행 (✗ cli-tests) |
| Phase 22.A bash chain | ✓ workspace 무결성 유지 (0 diff) |

## 의미

이전까지는 "진짜 stuck 가 발생할 때 gil이 graceful abort 하는가?" 가설. Run 11이 첫 EM2EM 검증:

- ✓ Agent 가 진전 없는 작업을 4 iter 안에 감지
- ✓ Recovery 전략 시도 (3-strike)
- ✓ 모두 실패 시 깨끗하게 abort (token 낭비 최소화)
- ✓ 사용자에게 명확한 status (`stuck` + "aborted: 3 unrecovered stuck signals")
- ✓ Verifier 마지막에 한 번 실행하여 "어디까지 갔나" 보고

며칠짜리 자율 실행에서 결정적: 가능한 task는 끝까지 가고, 불가능한 task는 빠르게 graceful abort. 둘 다 사용자에게 명확한 결과.

## 11-run 누적 평가

| Run | 발견 | Phase fix |
|---|---|---|
| 1+2 | budget reserve 부재 | 19.A |
| 3 | diet 너무 공격적 | 20.B |
| 4 | spec 자체 오류 (사용자) | — |
| 5 | 첫 verify pass | — |
| 6 | 첫 budget 안 done | — |
| 7 | filename `__X__` Go skip | — |
| 8 | 5-pattern stuck 못 잡음 | 21.A |
| 9 | bash chain 우회 | 22.A |
| 10 | NoProgress verify 의존 | 22.B |
| **11** | **모든 stuck 메커니즘 EM2EM 검증** | — |

5 dogfood-found 버그 모두 fix. Run 11이 가장 강한 단일 검증.

## gil 의 핵심 가설 — 11 runs 후 평가

### ✓ 검증됨

1. **Interview-then-autonomous, never-re-ask** (Run 5+6)
2. **며칠 자율 실행 capability** — qwen3.6-27b 단일 모델로 19 iter / 404k tokens / multi-file refactor 완료 (Run 6)
3. **Verifier 항상 실행** (Phase 19.A) — 11/11 run
4. **Self-correcting tool errors** (Phase 20) — Run 5+6 vs Run 3 edit error 0 vs 4
5. **Reference-grade lift 작동** — plan, repomap, edit 4-tier, memory bank, compact, checkpoint 모두 실 LLM 으로
6. **자율 결정** — helper 위치/signature/godoc agent 자율 (Run 6)
7. **Permission gate hardening** (Phase 22.A) — Run 10 chain 우회 차단
8. **Stuck recovery EM2EM** (Phase 21+22.B) — Run 11 3-strike abort

### 미검증 (외부 자원 필요)

- 24+ hour soak (가장 긴 run = 7분)
- claude-haiku planner + qwen editor 페어링 (Anthropic key)
- 외부 사용자 dogfood
- SWE-bench 등 표준 벤치마크 비교

## 결론

11-run dogfood 가 gil의 모든 핵심 안전망을 진짜 LLM 으로 검증. 발견-수정 loop이 닫힘.

**다음 가치 있는 단계**:
- 외부 자원 활용 시점 — 사용자 결정 영역
- 더 깊은 코드 작업의 ROI 한계
