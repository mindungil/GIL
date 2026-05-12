# Self-dogfood plan — gil-on-gil with qwen3.6-27b

> 가장 가치 있는 검증: gil이 gil 자기 자신의 코드를 자율로 수정할 수 있는가. Phase 1-18 의 모든 인프라(interview, plan, web_fetch, lsp, edit, verify, checkpoint, stuck recovery, memory bank)가 실 LLM 으로 통합되어 작동하는지 단일 시나리오로 검증.

## Task 선정 기준

- 작고 분명함 (한두 파일, 검증 명확)
- 사용자가 이미 인지한 실제 UX 부족
- gil의 모든 lift된 도구를 사용할 가능성 (read_file, edit, bash, verify 최소)
- 검증이 binary (pass/fail 명확)
- 망쳐도 git restore 가능 (이미 push 된 origin 으로 회복)

## 선정된 task

**"`gil` no-arg 출력에 세션이 N개 이상이면 최근 10개만 보여주고 '+ X more' 안내 추가"**

### Why

- 사용자가 직접 본 문제: 50+ 세션이 한 화면에 뜸
- 단일 파일 (`cli/internal/cmd/summary.go`)
- 명확한 검증: 50개 fixture 세션 만든 후 `gil` 실행 → 출력 라인 수 ≤ 12 (header + 10 rows + "+ 40 more" + 빈 줄)
- 작은 코드 변경 (~15-30 lines)
- 미적 일관성도 신경 써야 함 → aesthetic spec 적용 능력 시연

### Spec (인터뷰 결과 시뮬레이션)

```yaml
goal:
  one_liner: "Cap gil no-arg session listing at 10 rows with overflow hint"
  detailed: |
    The `gil` command (no arguments) lists every session in $XDG_DATA_HOME/gil/sessions/.
    With many e2e fixture sessions, output is overwhelming.
    
    Acceptance criteria:
    1. If session count > 10, show only the 10 most-recently-updated sessions.
    2. Below the row list, append a single dim line: "  ›  + N more   gil status   show all"
       where N = total - 10. The hint matches the existing footer arrows in summary.go.
    3. If session count <= 10, behavior unchanged (show all).
    4. The full list is still available via `gil status` (which has its own --plain / --output json modes).

verification:
  checks:
    - id: build
      command: "go build ./..."
      kind: shell
    - id: tests
      command: "go test ./cli/internal/cmd/..."
      kind: shell
    - id: tabletop
      command: |
        # Tabletop test: fixture 15 sessions, run gil, count rows
        TMPDIR=$(mktemp -d) && export GIL_HOME=$TMPDIR
        # Note: this is cheap — just check the rendered output
        for i in $(seq 1 15); do
          mkdir -p $TMPDIR/data/sessions/sess_$(printf %02d $i)
        done
        # ... (actual fixture would need DB rows; agent should figure out via existing summary_test.go patterns)

workspace:
  backend: LOCAL_NATIVE
  path: /tmp/gil-self-dogfood-workspace

risk:
  autonomy: ASK_DESTRUCTIVE_ONLY

run:
  budget:
    max_iterations: 20
    max_total_tokens: 120000
```

## 환경 setup

```bash
# Isolated GIL_HOME
export GIL_HOME=$(mktemp -d -t gil-self-XXXXX)

# Workspace = git archive snapshot of gil at HEAD (no .git, no risk to dev tree)
WORKSPACE=$(mktemp -d -t gil-self-ws-XXXXX)
git -C /home/ubuntu/gil archive --format=tar HEAD | tar -x -C $WORKSPACE
cd $WORKSPACE && git init -q && git add -A && git commit -q -m "baseline"  # so shadow git has parent

# Auth (key NOT in this doc — see ~/.config/gil/auth.json after login)
./bin/gil auth login vllm --api-key '<from .env>' --base-url '<from .env>'

# gild auto-spawn handled by gil run
# Or manually: ./bin/gild --foreground &
```

## Run

```bash
SESSION=$(./bin/gil new --working-dir $WORKSPACE | awk '{print $NF}')

# Option A: full interview (test if qwen handles Q&A saturation)
./bin/gil interview $SESSION --provider vllm

# Option B (fallback if interview times out / qwen weak at structured Q&A):
# Use a helper to inject the spec directly via gRPC FreezeSpec
# (needs a tiny Go program — write if needed)

./bin/gil run $SESSION --provider vllm
```

## Observation points

What to record in the post-mortem report:
1. **Interview duration** — how many turns until saturation
2. **Spec quality** — did qwen extract all 4 acceptance criteria?
3. **Tool usage** — which gil tools did the agent call? (repomap, read_file, edit, bash, verify)
4. **Iterations to verify pass** — did stuck detection trip?
5. **Token cost** — total in/out, qwen is local = $0
6. **Latency** — wall clock total
7. **Final diff** — what code changes did agent produce? quality assessment
8. **Verifier outcome** — did `go test ./...` actually pass?
9. **Plan tool usage** — did agent use the new plan tool?
10. **Bugs surfaced** — anything in gil that broke during real LLM run

## Risk + safeguards

- Workspace is isolated (git archive copy, separate $GIL_HOME)
- Shadow Git checkpoints preserve every state
- Budget caps (20 iter, 120k tokens) hard-stop runaway
- If anything goes wrong: `rm -rf $GIL_HOME $WORKSPACE` and dev tree is untouched

## Success criteria

**Minimum**: agent makes ≥1 syntactically-valid edit to summary.go.
**Target**: verifier passes (build + tests green) + tabletop test confirms 10-row cap.
**Stretch**: agent uses plan tool to track its 4 acceptance criteria + ends with all 4 marked completed.

## Failure modes (and what they teach us)

- **Interview timeout** → qwen weak at structured Q&A. Future: Architect/Coder split (use claude-sonnet for interview, qwen for run).
- **Edit tool 4-tier all miss** → repomap suggested wrong location. Surfaces gap in repomap precision.
- **Stuck detection trips early** → 5-pattern detector too aggressive for legitimate exploration.
- **Verifier pass but human says "no"** → verifier checks insufficient (need broader assertions).
- **Run completes but diff is wrong direction** → spec ambiguous, interview should have caught.

Each failure mode → next phase plan item.
