# Stuck Recovery

며칠짜리 자율 실행에서 agent가 막혔을 때의 안전망. **5+1 detector + 4 recovery strategies + 3-strike abort**.

## 6 detector patterns

| Pattern | 발화 조건 | 출처 |
|---|---|---|
| `RepeatedAction` | 같은 tool 호출 연속 | OpenHands |
| `RepeatedActionError` | 같은 tool + 같은 에러 | OpenHands |
| `Monologue` | 도구 안 쓰고 텍스트만 | OpenHands |
| `PingPong` | 두 행동 alternating | OpenHands |
| `ContextWindow` | context 한계 임박 | OpenHands |
| `NoProgress` | K iter 동안 verifier 점수 stalled + file churn or empty | **gil 자체 (Phase 21.A + 22.B)** |

## NoProgress 6번째 패턴 (가장 중요)

5 OpenHands pattern은 모두 **REPEATED action** 가정. 자율 실행에서 진짜 위험한 stuck 은 "다양한 행동인데 진전 0":
- agent가 다양한 시도
- verifier 점수 동일
- 코드 변경 없거나 churning

Phase 21.A 도입 + 22.B 보완:
- threshold 4 iter (configurable)
- verify_run 신호 있으면 점수 stalled 검사
- verify_run 없어도 successful edits 0 이면 fire (Phase 22.B fallback)

self-dogfood Run 11 검증: 6 iter (early abort) / 54k tokens (Run 8 195k 대비 72% 절감).

## 4 recovery strategies

| Strategy | 전략 |
|---|---|
| `ModelEscalate` | 강한 모델로 1 turn 재시도 (e.g. main → opus) |
| `AltToolOrder` | system prompt 에 "use different approach" 단발 hint |
| `ResetSection` | shadow git을 second-newest checkpoint로 hard reset |
| `AdversaryConsult` | 별도 LLM 호출 → 1줄 제안 → next turn 시스템 노트로 |
| `SubagentBranch` | read-only sub-loop으로 정찰 |

각각 한 번씩 시도. 모두 실패하면 3-strike abort → status=`stuck`.

## 3-strike 정책

```
stuck pattern 발화 → recovery strategy 선택 → 시도
↓
stuck 또 발화 (다른 또는 같은 pattern) → 다음 strategy 시도
↓
3번째 stuck → ALL strategies exhausted → abort
```

`stuck_unrecovered` event emit + status=`stuck`.

## 출력

```
$ gil run abc123 --provider vllm
Status:     stuck
Iterations: 6
Tokens:     54249
Error:      aborted: 3 unrecovered stuck signals
Verify results:
  ✗ cli-tests (exit=1)
```

verifier 도 마지막에 1회 실행 (Phase 19.A always-final-verify).

## Diagnostic

```bash
gil events <id> --filter milestones,errors
# stuck_detected events 확인
# pattern, detail 별 무엇이 막혔는지
```

stuck pattern 별 detail:
```
stuck_detected: pattern=NoProgress, detail="no verify run + no edits over 4 iters; no files modified"
stuck_detected: pattern=RepeatedActionError, detail="same tool 'edit' with same error 3 times"
```

## Recovery 시도 후 회복 가능

stuck 발화 → recovery 후 진전 → 다음 stuck 까지 strikes count 리셋.

## 자세한 검증

[Self-dogfood Reports](Self-dogfood-Reports) Run 11 참조.
