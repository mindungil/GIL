# Self-dogfood result — gil-on-gil with qwen3.6-27b

> 첫 진짜 self-dogfood. gil이 gil 자기 코드를 자율로 수정. 결과: **성공** (with caveats).

## Setup

- **Workspace**: `git archive HEAD` 스냅샷을 격리 디렉토리에 펼쳐서 사용 (dev tree 무손상)
- **GIL_HOME**: tmpdir, fresh credstore + sessions
- **Provider**: vllm (qwen3.6-27b on 사용자 제공 OpenAI-호환 endpoint)
- **Autonomy**: ASK_DESTRUCTIVE_ONLY
- **Spec freeze**: `tests/e2e/helpers/setfrozen.go` 으로 직접 frozen (interview 우회 — qwen이 Q&A saturation에 약함을 가정)

## Task

> "Cap gil no-arg session listing at 10 rows with overflow hint"

자기 자신의 UX 부족(50+ 세션이 한 화면에 뜨는 문제)을 자기가 고치는 task.

## Run 1 — budget 너무 빡빡

| | |
|---|---|
| Iterations | 10 |
| Tokens | 169,334 (budget=150,000 — exceeded) |
| Status | budget_exhausted |
| Wall time | ~70s |
| Code changed | 4 lines (summaryEnv struct에 TotalSessions 필드만 추가) |
| Verifier | 실행 안 됨 (budget 먼저 hit) |

**원인**: 150k token budget이 qwen + gil의 큰 system prompt(~17k tokens/turn) 에 부족. 10 turn = 169k.

## Run 2 — budget 충분 (400k)

| | |
|---|---|
| Iterations | 19 |
| Tokens | 404,407 (budget=400,000 — exceeded) |
| Status | budget_exhausted |
| Wall time | ~140s |
| Code changed | summary.go +29/-8, summary_test.go +44/-0 |
| **Verifier (manual rerun)** | **✓ build OK + tests OK** |

verifier check가 budget exhausted 직전에 실행 못 됐지만, 코드 자체는 manual rerun 시 모두 green.

## qwen이 한 일 (turn-by-turn 요약)

### Tool 사용 분포 (17 tool_call 총합)

| 도구 | 횟수 | 용도 |
|---|---|---|
| read_file | 7 | root.go, session.go, session_test.go (절대 + 상대 경로 두 번), summary.go, status.go, status_render.go, summary_test.go |
| bash | 4 | ls, find, grep × 2 |
| **plan** | **2** | **set 한 번 (4 items 등록), update 한 번 (1 item completed)** |
| edit | 1 | summary.go 정확한 구간 수정 |
| ... | ... | (나머지 budget hit 전까지 추가 edit + test 작성에 사용) |

### Plan 도구 자발적 사용 (Phase 18.A 검증)

qwen이 task 시작 직후 Phase 18 에서 추가한 plan 도구를 **시키지 않아도** 사용:

```json
{
  "operation": "set",
  "items": [
    {"text": "Add TotalSessions field to summaryEnv"},
    {"text": "Modify renderSummary to emit overflow hint"},
    {"text": "Update caller to set TotalSessions"},
    {"text": "Add tests for overflow + no-overflow cases"}
  ]
}
```

이후 1번 update_item으로 첫 항목 completed 표시. 시스템 프롬프트의 plan 요약 prepend가 "you have a plan, follow it" 신호 역할을 한 것으로 추정.

### 최종 코드 변경 (Run 2)

`cli/internal/cmd/summary.go`:
```go
+	total := len(rows)
+	const maxRows = 10
+	if len(rows) > maxRows {
+		rows = rows[:maxRows]
+	}

	renderSummary(out, summaryEnv{
		...
		Sessions:      rows,
+		TotalSessions: total,
	})
```

```go
+	if e.TotalSessions > len(e.Sessions) {
+		extra := e.TotalSessions - len(e.Sessions)
+		fmt.Fprintf(out, "   %s\n", p.Dim(fmt.Sprintf("›  + %d more", extra)))
+	}
```

`cli/internal/cmd/summary_test.go`: 새 테스트 2개 (overflow / no-overflow), 기존 패턴 (`t.Setenv("NO_COLOR", "1")`, `summaryRow` 생성, `renderSummary` 직접 호출, `require.Contains` 검증) 정확히 모방.

## 평가

### 잘 한 것 ✓

1. **Plan 도구 자발적 사용** — 시키지 않아도 task 시작 시 4-step plan 작성. Phase 18.A의 핵심 가설 (system prompt prepend가 plan tool 사용을 유도) 검증.
2. **정확한 위치 식별** — repomap 없이 grep + read_file 조합으로 `runSummary`/`renderSummary` 정확히 찾아냄.
3. **Aesthetic 준수** — 코드 안에서 `›` 글리프 + `p.Dim()` 호출 패턴 발견 후 자기 변경에도 동일 적용. terminal-aesthetic.md 참조 안 했음에도 일관성 유지.
4. **테스트 작성** — 기존 테스트 스타일 (positive + negative 페어, t.Setenv, require.Contains) 정확히 따라함.
5. **Edit 도구 4-tier 매칭 첫 시도 통과** — Aider lift 정확도 검증.

### 부족한 것 ✗

1. **Token 비효율** — 17k tokens/turn 평균은 너무 높음. 시스템 프롬프트 + 18 도구 schema + AGENTS.md + memory bank prepend 합산.
2. **Stuck 직전까지 미도달** — 예산 부족으로 verifier 실행 직전에 종료. 진짜 며칠짜리 작업에서 stuck 회복 메커니즘 검증 못함.
3. **반복 read_file** — 절대 vs 상대 경로 차이로 같은 파일 두 번 읽음. 효율 손실.
4. **caller propagation 일부 누락** — `gil status` (visual mode)는 동일한 truncation 안 들어감 — spec이 "gil no-arg"만 요구해서 정확하지만 사람이 봤다면 "둘 다 해야 한다"고 판단.

### 발견된 gil 자체 버그

- **Verifier가 budget exhausted 시 실행 안 됨** — verify는 종료 직전에 한 번이라도 시도해야 함. budget을 verify check 전에 reserve 하는 게 맞음.

이건 Phase 19 또는 hot-fix candidate.

## 결론

**핵심 가설 검증**: ✓ gil의 자율 실행 인프라가 진짜 LLM (qwen3.6-27b) 으로 작동. plan + edit + read + bash 도구 자발적 사용. mission control aesthetic 자연스럽게 모방. 수정된 코드가 build + tests pass.

**조정 필요**:
1. System prompt 토큰 다이어트 (현재 17k/turn → 목표 8k/turn)
2. Verifier reserve token 메커니즘
3. Repomap 강제 prepend로 read_file 반복 줄이기

**다음 dogfood 후보**: 더 큰 task (예: gil 자체 README의 "외부 자원 필요한 잔여 항목" 중 하나 처리), claude-haiku 같은 Anthropic 모델로 시도, 며칠짜리 시뮬레이션.

## Reproduce

```bash
DOG_HOME=$(mktemp -d) && export GIL_HOME=$DOG_HOME
WORK=$(mktemp -d) && git -C /home/ubuntu/gil archive HEAD | tar -x -C $WORK
cd $WORK && git init -q && \
  git -c user.email=mindungil@gil -c user.name=mindungil commit -q --allow-empty -m baseline
gil auth login vllm --api-key '<key>' --base-url '<endpoint>'
ID=$(gil new --working-dir $WORK | awk '{print $NF}')
mkdir -p $GIL_HOME/data/sessions/$ID
# write spec.yaml as in this report
go run /home/ubuntu/gil/tests/e2e/helpers/setfrozen.go $GIL_HOME/data/sessions.db $ID
gil run $ID --provider vllm --model qwen3.6-27b
```
