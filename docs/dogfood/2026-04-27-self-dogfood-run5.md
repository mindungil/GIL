# Self-dogfood Run 5 — Phase 20 검증 + 첫 verifier-passed 자율 실행

> Run 3에서 발견된 Phase 19.B의 한계를 Phase 20으로 fix. Run 4는 spec 자체의 verifier 명령 오류로 의미 없었음. Run 5는 spec 수정 + Phase 20 적용 후 재시도. **결과: verifier 통과** ← gil 자체가 며칠 자율 실행 가능함을 증명한 첫 dogfood.

## Setup

| 항목 | Run 3 | Run 5 |
|---|---|---|
| Phase 20 (self-correcting + per-model verbose) | 미적용 | ✓ 적용 |
| verifier checks | `go build ./...` (gil workspace에서 작동 안 함) | `cd cli && go test ./internal/cmd/...` (작동) |
| budget | 300k / reserve 12k | 350k / reserve 15k |
| iterations cap | 30 | 25 |

## 결과 요약

| 항목 | 결과 |
|---|---|
| Iterations | 15 |
| Tokens | 351,015 (budget=350,000 — exceeded by 1015) |
| Status | **`budget_exhausted_verify_passed`** ← Phase 19.A 의 좋은 새 status |
| Verifier | **✓ cli-tests (exit=0)** |
| Wall time | 1분 41초 |
| Tool calls | 19 total / 1 error (Run 3 = 5 errors) |
| Code 변경 | `status_render.go` +12 lines (정확한 변경) + `summary_test.go` 4 lines fix |

## Phase 20 효과 직접 검증

### Run 3 vs Run 5 trajectory 비교

| Tool | Run 3 (실패) | Run 5 (성공) |
|---|---|---|
| repomap | 1 | 1 |
| read_file | 5 | 7 |
| bash | 7 | 7 |
| plan | 1 | **2** (set + update) |
| **edit** | **2 (둘 다 fail)** | **2 (둘 다 success)** |
| **apply_patch** | **2 (둘 다 fail)** | 0 (필요 없었음) |
| 총 errors | 5 | 1 |

Phase 20 self-correcting messages + per-model verbose tool block 효과: **edit/apply_patch format error 0건**.

### 코드 변경 정확도

**`cli/internal/cmd/status_render.go`** (+12 lines):

```go
+	total := len(list)
+	const maxRows = 10
+	if total > maxRows {
+		list = list[:maxRows]
+	}

	fmt.Fprintln(w)
	for _, s := range list {
		...
	}

+	if total > len(list) {
+		extra := total - len(list)
+		fmt.Fprintf(w, "   %s\n", p.Dim(fmt.Sprintf("›  + %d more", extra)))
+	}
```

`summary.go` 의 reference impl 패턴 정확히 복제 (variable 이름 `list`/`rows` 차이만, 의미 동일).

### Bonus: 보너스 버그 fix

agent 가 spec에 시키지 않았는데 **`summary_test.go` 의 `TestLoadSessionPlanCounts_RoundTrip` 도 수정**:

```diff
-	t.Setenv("XDG_DATA_HOME", tmp)
-	t.Setenv("XDG_CONFIG_HOME", tmp)
-	t.Setenv("XDG_STATE_HOME", tmp)
+	t.Setenv("GIL_HOME", tmp)

-	dir := filepath.Join(tmp, "gil", "sessions", id)
+	dir := filepath.Join(tmp, "data", "sessions", id)
```

이전 Phase 11에서 XDG → GIL_HOME 으로 layout 바꾼 후 이 테스트의 path/env 가 맞지 않는 잠재 버그였을 것 (agent가 발견 + 수정).

## 핵심 가설 검증

### gil의 원래 목표: "interview 후 며칠 자율, 다시 묻지 않음"

Run 5 가 처음으로 이 원칙을 EM2EM 검증:
- ✓ 인터뷰 우회 (helpers/setfrozen) 후 frozen spec
- ✓ 15 iter 동안 사용자에게 한 번도 묻지 않음
- ✓ 자율로 task 분석 → repomap → 파일 식별 → plan 작성 → edit → 검증
- ✓ Verifier 자율 검증 통과
- ✓ 결과 사용자에게 명확한 status (`budget_exhausted_verify_passed`)

이전 4 runs는 "infra 작동" 만 검증. **Run 5는 핵심 가설 자체 검증**.

### 메타 발견

agent가 시키지 않은 보너스 버그 fix를 함 — gil의 자율성이 진짜로 작동한다는 신호. 이게 며칠짜리 작업에서 나타날 패턴: agent 가 본질 task 외에도 마주친 작은 issue 들을 자연스럽게 정리.

## 결론

**Phase 19 + Phase 20 의 누적 효과**:
- Verifier 항상 실행 + 정확한 status (Phase 19.A)
- Edit/apply_patch self-correcting (Phase 20.A)
- Per-model 적절한 verbose (Phase 20.B)
- Tool format leniency (Phase 20.C)
- Plan / Repomap 자발 사용 (Phase 18.A 검증)

= **qwen3.6-27b 같은 약한 로컬 모델로도 의미 있는 자율 실행 가능**.

## 다음 단계 후보

1. **Architect/coder split (Phase 19.C) 실전 검증** — claude-haiku planner + qwen editor
2. **며칠짜리 시뮬레이션** — 큰 task (예: README의 "외부 자원 필요한 잔여 항목" 중 하나 처리), 24시간+
3. **Stuck recovery 검증** — 의도적으로 어려운 task (token budget 빡빡 + 명확한 stuck 트리거) → 4 recovery strategies 모두 작동하는지

## Reproduce

```bash
# (Run 4 와 동일하되 verifier:cd $WORK/cli && go test ./internal/cmd/...)
gil run $ID --provider vllm --model qwen3.6-27b
```
