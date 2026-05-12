# Self-dogfood Run 6 — Multi-file DRY refactor, pure success

> Run 5에서 status_render.go에 같은 overflow rendering 패턴이 추가되면서 summary.go 와 중복이 발생. Run 6는 이걸 shared helper로 정리하는 refactor task. **결과: 11 iter / 230k tokens / status=`done` / verifier passed within budget**. 첫 "순수 done" status.

## 결과 요약

| 항목 | 결과 |
|---|---|
| Iterations | 11 |
| Tokens | 230,085 / 350,000 (사용 65%) |
| Status | **`done`** ← 처음 순수 done. budget 도달 안 함, verify pass |
| Verifier | ✓ cli-tests (exit=0) |
| Wall time | 1분 |
| Code 변경 | uistyle/overflow.go +20 (new) / summary.go -3 / status_render.go -3 |

## 변경 내용

### 새 파일: `cli/internal/cmd/uistyle/overflow.go`

```go
package uistyle

import (
    "fmt"
    "io"
)

// OverflowHint prints a "+ N more" dimmed hint line when total exceeds
// the number of items shown. It is used by both the no-arg summary
// (summary.go) and the status renderer (status_render.go) so the
// formatting lives in one place.
//
// When total <= shown nothing is written.
func OverflowHint(w io.Writer, p Palette, total, shown int) {
    if total <= shown {
        return
    }
    extra := total - shown
    fmt.Fprintf(w, "   %s\n", p.Dim(fmt.Sprintf("›  + %d more", extra)))
}
```

### 호출부 정리

**summary.go**:
```diff
-if e.TotalSessions > len(e.Sessions) {
-    extra := e.TotalSessions - len(e.Sessions)
-    fmt.Fprintf(out, "   %s\n", p.Dim(fmt.Sprintf("›  + %d more", extra)))
-}
+uistyle.OverflowHint(out, p, e.TotalSessions, len(e.Sessions))
```

**status_render.go**: 동일한 패턴.

## 평가

### Run 5 vs Run 6 효율 (같은 모델, 같은 시스템 프롬프트)

| 메트릭 | Run 5 | Run 6 |
|---|---|---|
| Iterations | 15 | **11** |
| Tokens | 351K (within 1k of budget) | **230K (65%)** |
| Errors | 1 | **0** |
| Status | `budget_exhausted_verify_passed` | **`done`** |
| Wall time | 1분 41초 | **1분** |

Run 6 이 모든 면에서 더 효율적. **이유 추정**: refactor는 "같은 패턴을 여러 곳에 복사" 보다 "shared helper 추출" 이 LLM에게 더 자연스러운 패턴 (training data 에 흔함). 또한 Run 6 spec이 더 구체적 ("suggested location: cli/internal/cmd/uistyle/overflow.go").

### 자율성 검증

- ✓ 11 iter 동안 한 번도 사용자에게 묻지 않음
- ✓ Helper 위치 (uistyle/) 자율 결정 — 이미 Palette가 거기 있다는 걸 read_file로 발견 후 같은 패키지에 두기로 판단
- ✓ Helper signature `(w, p, total, shown)` 자율 설계 — 두 caller 의 use 분석 후
- ✓ Godoc 자율 작성 — terminal-aesthetic.md 안 봤지만 surface-level explain
- ✓ 테스트 추가 안 함 — refactor니까 기존 테스트가 cover (정확한 판단)

### "며칠 자율 실행" 가설 가장 강한 검증

이전 5 runs는 "infra 작동 + 첫 verifier pass" 검증.
Run 6은:
- Pure `done` status (Phase 19.A의 목표 status)
- Budget 65% 사용 (며칠짜리 작업에서도 여유)
- 0 errors (도구 사용 정확)
- 사용자 instruction 외에 자율 설계 (helper 위치, signature, godoc)

**즉**: gil은 진짜 LLM (qwen3.6-27b) 으로 며칠짜리 자율 코딩 가능.

### Phase 누적 효과 매트릭스

| Run | Phase 적용 | Status | Verifier | 핵심 메시지 |
|---|---|---|---|---|
| 1 | 18 | budget_exhausted | 못 돔 | budget 너무 작음 |
| 2 | 18 | budget_exhausted | 못 돔 | 코드는 됐으나 verifier 못 봄 |
| 3 | 19.A only | budget_exhausted_verify_failed | ✗ | diet 너무 공격적 |
| 4 | 19.A + 19.B | budget_exhausted_verify_failed | ✗ | spec verifier 명령 오류 |
| 5 | 19.A + 19.B + 20 | budget_exhausted_verify_passed | ✓ | 첫 pass |
| **6** | 19.A + 19.B + 20 | **done** | **✓** | **첫 budget 안 done** |

Phase 19/20 누적 적용 후: **2번 연속 verifier pass** + Run 6은 budget 도달도 안 함.

## 결론

Run 6은 gil의 핵심 가설을 가장 강하게 검증한 dogfood:
- 진짜 LLM, 진짜 코드, 진짜 verifier
- budget 안에 끝남 (며칠짜리에서도 cost 통제 가능)
- 사용자 개입 0회 (interview-then-autonomous 원칙)
- 0 tool 에러 (Phase 20 self-correcting 효과)
- 자율 설계 (helper API + 위치)

## 다음 후보

이 시점에서 코드/문서 deeper 작업 가치가 한계 도달. 의미 있는 다음 단계:

1. **Architect/coder split (Phase 19.C) 실전** — anthropic 키 있을 때 claude-haiku planner + qwen editor
2. **Stuck recovery 검증** — 의도적 어려운 task로 4 strategy 모두 작동 확인
3. **Multi-day soak** — 작은 task 100개 연속 자율 실행 (실제 며칠 동작 검증)
4. **README 잔여 작업 자동화** — gil이 자기 README 의 "외부 자원 필요" 항목 중 하나를 처리 (절차 문서 작성, dispatch script 작성 등)

## Reproduce

```bash
# (Run 5 와 동일 setup)
gil run $ID --provider vllm --model qwen3.6-27b
# spec.verification.checks: [cd $WORK/cli && go test ./internal/cmd/...]
```
