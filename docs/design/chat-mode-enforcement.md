# chat-mode enforcement — closing the chat / run-mode gap

Status: design (2026-05-15) · 살아있는 문서

## 1. 동기

2026-05-15 [failure-floor stress](../research/2026-05-15-gil-failure-floor.md)
가 드러낸 사실: gil chat surface는 architecturally codex와 동일한 single-shot
agent다. gil이 차별점으로 내세운 verify-loop, MCP, plan_steps strict
enforcement는 모두 `RunService.executeRun` / `continueRun` 안에서만 발동한다.
`SessionService.Prompt` 경로 — 즉 사용자가 가장 자주 쓰는 chat — 는 이 세
mechanism의 강제력을 못 받는다.

직전 라운드의 N=18 100% PASS는 LLM 추론으로 PASS한 것이지, system이 강제해서
PASS한 게 아니다. 사용자 입장에서 보면 "gil의 가장 큰 가치 mechanism이
가장 자주 쓰는 surface에서 비활성".

이 문서는 그 gap을 닫는 단일 wave의 변경을 명세한다.

설계 원칙 (`docs/design.md` §2) 점검:
- **§2.1 가지치기 금지** — chat/run-mode 분기를 시스템 코드에 더 박는 게
  아니다. 오히려 chat 경로가 run 경로와 동일한 enforcement layer를 공유하도록
  통합한다. 가지가 줄어든다.
- **§2.3 에이전트 결정, 시스템 안전망** — 본 변경은 enforcement (verify
  before progression, readonly safety, verify quality floor)만 추가한다.
  agent가 어떤 verify를 어떻게 짤지, plan_steps를 만들지 말지의 결정은
  여전히 agent에게 있다.
- **§2.4 인터뷰 전환 게이트 (self-audit는 인터뷰 stage만)** — chat 경로의
  agent loop는 인터뷰가 아니다. 이 변경은 self-audit gate를 chat에 일반화하지
  않는다.

## 2. Scope

3 changes + 1 cleanup. 단일 wave에 묶인다.

| # | Change | 영역 | Severity |
|---|--------|------|----------|
| C1 | chat 경로 turn-내 verify 강제 | server agent loop | high |
| C3 | write_file / edit_file / apply_patch의 readonly target reject | server tools | medium |
| C4 | verify tool의 약한 command schema reject | server tools | medium |
| C5 | 5/12 부산물 cleanup | repo hygiene | low |

### 2.1 C2 drop — 정정

원래 spec 초안에 있던 "MCP launch를 chat 경로에서도" change는 **잘못된 진단이었다.**
`grep launchMCPServers`만 했을 때는 호출 위치가 `run.go`뿐으로 보였지만,
실제로는 `RunService.ensureSessionMCPTools` (`server/internal/service/run.go:158`)가
chat-mode bridge로 wire되어 있다:

- `appendChatMCPTools` (`session_prompt.go:554`)가 매 Prompt마다 호출
- 그 안에서 `ensureSessionMCPTools` → `launchMCPServers` (run.go:203)
- per-session cache로 chat ↔ run handoff에서 재사용

즉 chat MCP는 production-wired다. 다만 `ensureSessionMCPTools`는 spec이
frozen이고 `Tools.McpServers` allowlist 있을 때만 작동 (run.go:173). 이건
의도된 design — MCP allowlist는 spec의 일부, freeze가 prerequisite. failure-floor
stress가 spec freeze 없는 chat session으로만 돌았기 때문에 MCP 발동을
못 본 것.

따라서 C2는 drop. 만약 *pre-freeze* chat에서도 MCP 필요한 use case가
나오면 그건 별도 spec (config 신설 — §2.1 가지치기 금지 검토 필요)으로
다룬다.

## 3. C1 — chat 경로 turn-내 verify 강제

### 3.1 현 상태

`server/internal/service/agent_tools_plan_verify.go:367` 의 `toolVerify`
description은 "**the system enforces verify-before-progression**" 이라고
적혀있다. 그러나 실제 enforcement는 `step_id` 가 제공되어 plan_steps와
binding될 때만 작동한다 (line 380). chat은 plan_steps를 거의 만들지 않으므로
verify는 사실상 informational tool로 동작한다.

f1-f8 stress run에서 모든 8 task는 다음 패턴으로 끝났다:
```
read_file → write_file → verify(weak command) → done
```
verify command가 `cat main.go` 같은 grep 수준이어도 system은 그대로 통과시켰다.

### 3.2 제안

**turn-boundary verify enforcement**를 chat agent loop에 도입한다.
plan_steps 결합과 무관하게:

> 한 turn 안에서 `write_file`, `edit_file`, `apply_patch` 중 하나라도
> 호출된 경우, 그 turn은 **`verify` tool 성공 호출 1회 이상** 없이는
> 종료되지 못한다 (model이 textual "done"을 표시해도 system이 turn을
> 종료 안 함, agent가 verify를 호출하도록 한 사이클 더 돌린다).

세부:
- "turn" = 한 user prompt → 모델의 `stop_reason=end_turn` 사이의 모든
  tool-call cycle.
- code-changing tool 호출 직후 `read_file` / `list_files` / `run_bash`
  같은 inspection tool은 허용 — 이게 자연스러운 verification 워크플로다
  (write → 다시 읽어보기 → verify).
- verify가 실패하면 turn은 종료될 수 있다 — 강제하는 건 "verify를 했다",
  성공이 아니다. 실패 자체는 agent의 다음 turn에서 다룬다.
- verify가 한 번도 안 호출되었다면 system은 turn을 종료하지 않고
  대신 ephemeral system message를 inject ("Code-changing tools were
  called but no verify was run. Call verify before completing this turn.")
  하고 한 사이클 더 돌린다. 최대 2 사이클까지 시도, 그래도 verify 없으면
  turn은 error로 종료 + event 발행.

### 3.3 구현 위치 추정

- `server/internal/service/session_prompt.go` — Prompt RPC handler 안의
  agent loop. tool dispatch 후 turn-종료 직전에 enforcement check.
- 새 helper: `turnVerifyTracker` — 한 turn 동안 어떤 tool이 불렸는지 set
  유지. 기존 `turnDiffTracker` (agent_tools_plan_verify.go:362) 옆에 둠.
- agent_tools_plan_verify의 `verifyToolNames` set을 export하거나 같은
  파일에 turn enforcement 함수 추가.

### 3.4 §2.3 (시스템 안전망) 부합

agent가 "어떤 verify를 짤지"는 그대로 agent decision (C4 schema 가드는
이 자유의 일부를 줄이지만 *내용*은 여전히 agent가 결정). 시스템은 "verify
없이 진행 못함"이라는 **객관적 종료 조건**만 강제 — 정확히 §2.3이 시스템에
허락한 책임 영역이다.

## 5. C3 — readonly target file reject

### 5.1 현 상태

f8 (`chmod 444 main.go` → "Greeting을 hi로 바꿔")가 5.9초 PASS. 그러나
file mode가 444 → 644로 silent 변경됨. user의 readonly 의도 무력화.

### 5.2 제안

`write_file` / `edit_file` / `apply_patch` 가 target file을 열기 전에
`os.Stat` 확인. **owner write bit (0o200) 미설정 시 reject**:

```
ToolResult{
  Content: "target file is read-only (mode 0%o). The user has marked this
    file as protected. If modification is genuinely required, surface the
    intent to the user — do not chmod to bypass.",
  IsError: true,
}
```

create인 경우 (file이 아직 없음)는 적용 안 됨 — parent directory write
가능하면 진행.

### 5.3 §2.3 부합

system이 file mode를 *해석*하는 것 (owner write = "user 의도")은 객관적
신호다 — agent의 결정 영역이 아니라 시스템 안전망. agent는 chmod tool을
호출할 자유가 여전히 있다 (별도 run_bash로 `chmod +w`); 다만 그 결정은
explicit 해진다.

### 5.4 구현 위치

- `server/internal/service/agent_tools_write.go` — 두 tool 모두 stat-check
  헬퍼 함수 공유.
- `apply_patch`는 별도 파일이면 같이 적용.

## 6. C4 — verify command quality scaffold

### 6.1 현 상태

verify tool은 임의 shell command를 받는다. agent가 약한 command (`cat`,
`ls`, `pwd`, `echo`, `true` 같은 read-only/no-op inspection)로 verify로
포장하면 그대로 PASS. f8의 `cat main.go`가 그 예.

### 6.2 제안

두 layer:

**Layer A — schema-level reject**: `toolVerify.run` 진입 시 command를 단순
파싱해서 `^\s*(cat|ls|pwd|echo|true|stat|head|tail|file)\b` 같은 명백한
read-only/no-op이 single command면 IsError로 reject:

```
ToolResult{
  Content: "verify command is too weak — `cat`/`ls`/`echo` only inspect
    state, they don't verify behavior. Use build, test, lint, type-check,
    or a custom assertion script.",
  IsError: true,
}
```

복합 명령 (`cat foo.go && go build`)이나 `&&`/`||` 체인의 마지막에 build/test가
있으면 통과. 정규식 휴리스틱이라 false positive 위험 있지만 — schema에서
*명백히* 약한 케이스만 잡는 게 목표. 미묘한 케이스는 다음 layer가 처리.

**Layer B — agent system prompt 가이드**: `defaultChatSystemPrompt`
(`session_prompt.go:101`) line 143-146에 이미 "After every code-changing
tool call ... you MUST run verify before progressing" 한 줄이 있다. 거기에
verify *내용*에 대한 한 줄 더 추가:

```
verify commands must exercise behavior, not just inspect state. Prefer
build, test, lint, type-check, or assertion scripts. Standalone `cat`,
`ls`, `echo`, `pwd` are not valid verify checks.
```

Layer A는 deterministic, Layer B는 가이드. 둘 다 있어야 강함.

### 6.3 구현 위치

- `server/internal/service/agent_tools_plan_verify.go` — `toolVerify.run`
  앞에 헬퍼 함수 `isWeakVerifyCommand(cmd string) bool`.
- 시스템 프롬프트 상수 `defaultChatSystemPrompt`는 실제로는
  `server/internal/service/session_prompt.go:101` 에 있다 (`agent.go`에는
  `exploreChatSystemPrompt` / `planChatSystemPrompt` 만). 거기 verify
  quality 한 줄 추가.

## 7. C5 — repo root cleanup

5/12 working_dir 버그 시기 (commit `ad9274b` 이전) 의 부산물:

- `email.go`, `email_test.go`, `go.mod` (repo root, May 12 timestamps)
- `go.work`의 `use ( . ... )` 항목 — 본래 repo root는 module이 아니다,
  추가된 `.` 항목 제거
- `.gocache/` — Go build cache 디렉토리, `.gitignore`에 추가

이 wave의 첫 commit으로 처리. design 변경과 분리 (atomic, easy review).

## 8. Non-goals

- chat session 내에서 multiple freeze_spec 허용 — spec immutability는
  의도된 design property (`docs/design.md` §2.2, freeze_spec line 181).
  drift 처리 UX 개선은 본 wave 밖.
- MCP server 의 cross-prompt 영속화 — 본 wave는 prompt-scoped launch만.
- subagent depth cap 완화 — V1 = 1 layer 그대로.
- chat 경로에 plan_steps 자동 inject — agent decision 영역 침해 위험.
  C1은 verify 강제만, plan_steps 강제는 안 함.

## 9. Test strategy

각 change에 대한 server-level integration test (`server/internal/service/*_test.go`)
+ end-to-end probe via `gil chat`:

- C1: `TestSessionPrompt_WriteWithoutVerify_TurnNotClosed` — write_file 후
  verify 없이 model이 end_turn 시그널 → system이 추가 사이클 inject.
- C2: `TestSessionPrompt_FrozenSpecWithMCP_LaunchesMCP` — frozen spec에
  mcp_servers allowlist 있는 session에 prompt 보내면 launchMCPServers 호출됨.
- C3: `TestEditFile_ReadonlyTarget_IsError` — chmod 444 후 edit_file 호출 →
  IsError + 변경 없음.
- C4: `TestVerify_CatCommand_Reject` — verify("cat foo.go") → IsError.
  Plus `TestVerify_CompoundBuild_OK` — verify("cat foo.go && go build") OK.
- C5: file/dir 삭제, gitignore update — 별도 test 불필요, git status로 검증.

추가로 failure-floor stress 8 task를 재실행해서 regression 확인 — pre-change
와 post-change 모두 PASS 유지하되, log를 검사해서 verify 호출 패턴이 강해졌는지
관찰.

## 10. Open followups (이 wave 후)

- per-session persistent MCP cache (vs prompt-scoped launch)
- chat 경로의 plan_steps inject (조건부)
- verify command quality scaffold의 LLM-based judge alternative (Layer A의
  regex 휴리스틱이 false-positive 많아지면)
- subagent depth cap 완화 (V2 deferred)

---

**관련 메모리**:
- [[feedback_check_production_wiring]] — chat-mode와 run-mode wiring 격차는
  이 가이드의 직접 적용.
- [[feedback_agent_drives_system_safeguards]] — 본 변경의 §2.3 부합 논리.
- [[feedback_natural_language_single_surface]] — chat은 100% 자연어 유지,
  enforcement는 system layer에서만.
