# Self-dogfood Runs 7+8 — Stuck recovery 검증 시도

> Run 6 (DRY refactor 순수 done) 후, **stuck 인프라가 실제 LLM 으로 작동하는지** 검증 시도. Run 7은 file naming 실수로 무효 (`__file__test.go` 패턴 Go가 스킵), Run 8은 **stuck 검출이 안 됨** — 새 발견.

## Run 7 — 무효 (테스트 컴파일 안 됨)

작성: `cli/internal/cmd/__stuck__test.go` 에 contradictory test (`x != 1 && x != 2`).
**문제**: 파일명이 `__` 시작 → Go test runner 스킵.
결과: 5 iter / 70k tokens / status=`done` / verifier ✓ — agent는 정상 코드베이스로 인식, 정확히 동작 (memory_update + compact_now 자율).

dogfood 무효지만 부산물:
- agent 가 task 없을 때 compact_now + memory_update 자율 호출 → 며칠짜리 작업의 idle behavior 검증

## Run 8 — Stuck 인프라 작동 안 함 (메타 발견)

작성: `cli/internal/cmd/intentional_stuck_test.go` 에 `t.Fatalf("...")` 무조건 실패. spec: "modify the test file 금지".

결과:
- Status: `budget_exhausted_verify_failed`
- 12 iterations (max cap에 정확히 도달)
- 195k tokens / 200k budget
- **stuck events: 0건**
- 4 permission_denied (test 파일 삭제 시도?)
- 5 model_switched (Phase 19.C architect/coder split 작동)

### 왜 stuck detector가 안 잡았나

gil의 stuck detector는 5 pattern 인식:
1. `RepeatedAction` — 같은 tool 호출 연속
2. `RepeatedActionError` — 같은 tool + 같은 에러
3. `Monologue` — 도구 안 쓰고 텍스트만
4. `PingPong` — 두 행동 alternating
5. `ContextWindow` — 컨텍스트 한계

Run 8에서 agent는 **다양한 시도** (read_file, bash, permission-denied delete, build, test) — 모두 다른 행동. 5 pattern 어느 것도 매치 안 됨.

### 진짜 발견: 새 stuck pattern 필요

며칠짜리 자율 실행의 *진짜* 위험은 "다양한 행동인데 진전 0":
- Agent 가 매번 다른 시도
- Verifier 점수는 동일 (같은 check 계속 실패)
- 코드베이스에 의미 있는 변경 없음 (혹은 churning 변경)

**Phase 21 candidate — `NoProgress` stuck pattern**:
```go
// Tracks (verifier_pass_count, files_modified) across iterations.
// If for K consecutive iterations:
//   - verifier_pass_count never improves
//   - AND files_modified is empty OR oscillating (same files re-read/re-edited)
// Then trigger stuck recovery.
```

Recovery 전략은 기존 4종 그대로 사용 가능 (ModelEscalate / AltToolOrder / ResetSection / AdversaryConsult / SubagentBranch).

## 누적 dogfood 결과 (8 runs)

| Run | Phase 적용 | Status | Verifier | Tokens | 메시지 |
|---|---|---|---|---|---|
| 1 | 18 only | budget_exhausted | 못 돔 | 169K | budget 너무 작음 |
| 2 | 18 only | budget_exhausted | 못 돔 | 404K | 코드 OK but verifier 못 봄 |
| 3 | 18 + 19.A only | budget_exhausted_verify_failed | ✗ | 306K | diet 너무 공격적 |
| 4 | 18 + 19 | budget_exhausted_verify_failed | ✗ | 360K | spec 자체 오류 |
| 5 | 18 + 19 + 20 | budget_exhausted_verify_passed | ✓ | 351K | **첫 verify pass** |
| 6 | 18 + 19 + 20 | **done** | ✓ | 230K | **첫 budget 안 done** |
| 7 | 18 + 19 + 20 | done | ✓ (vacuous) | 70K | filename bug |
| 8 | 18 + 19 + 20 | budget_exhausted_verify_failed | ✗ | 195K | stuck 안 잡힘 |

### 검증된 가설

- ✓ 인터뷰 후 며칠 자율, 사용자 0 개입 (Run 5+6)
- ✓ Verifier 항상 실행 (Run 3-8 모두)
- ✓ Phase 18 도구들 자발 사용 (plan, repomap, edit)
- ✓ Phase 20 self-correcting (Run 5+6 vs Run 3 대비 edit error 0)
- ✓ Phase 19.C 작동 (Run 8 model_switched 5회)

### 미검증 가설

- ✗ Stuck recovery (5 strategies) — 인위적 stuck task가 stuck pattern 매치 안 됨
- ✗ Multi-day soak — 가장 긴 run 이 5분
- ✗ Architect/coder real benefit — 둘 다 qwen 으로만 split 검증 (다른 모델 페어링 미시도)

## 다음 진짜 deeper 단계

1. **Phase 21.A — NoProgress stuck pattern** — 위 메타 발견 fix
2. **Phase 21.B — 의도적 stuck-trigger task로 5 strategies 직접 검증** — task에 명시적으로 같은 행동 반복하게 유도
3. **Multi-provider real run** — anthropic key 있을 때 architect=claude-haiku + editor=qwen 비교
4. **24-hour soak** — 작은 task 100개 연속 (이건 시간 자원 필요)
