# gil chat architecture — agent-tool routed, no client dispatch

Status: design (2026-05-06)

## 1. Principle

gil chat surface는 **100% 자연어**. 클라이언트에는 입력 parse / verb classify /
slash dispatch / 휴리스틱 라우팅 — 어떤 결정 logic도 없다.

| 사용자 입력 | 처리 |
|---|---|
| `안녕` | daemon → agent → 모델 응답 |
| `show me the diff` | daemon → agent → `show_diff` tool 호출 → 결과 |
| `apply it` | daemon → agent → `apply_diff` tool 호출 → 결과 |
| `freeze and run` | daemon → agent → `freeze_spec` + `start_run` tool 호출 |
| `tests/foo.py 좀 봐줘` | daemon → agent → `read_file` tool 호출 |
| 어떤 다른 phrasing이든 | daemon → agent가 결정 |

슬래시는 **escape hatch조차 아니다**. claude-code/codex/goose/opencode가
슬래시를 단축키로 둘 때조차 gil은 두지 않는다 — agent가 자연어를 이해하면 충분.

비교 reference:
- opencode `session.prompt({sessionID, agent, parts})` 한 줄 dispatch
  (`packages/opencode/src/cli/cmd/tui/component/prompt/index.tsx:813`)
- goose `agent.reply(user_message)` 한 줄
  (`crates/goose-cli/src/session/mod.rs:1067`)

gil은 같은 shape를 채택하고 한 발 더 나아가 슬래시조차 빼낸다.

## 2. Architecture

### 2.1 Single RPC

```protobuf
service SessionService {
  rpc Prompt(PromptRequest) returns (stream Part);
}

message PromptRequest {
  string session_id = 1;          // empty → daemon auto-creates and returns id in first Part
  repeated Part parts = 2;        // text + (future) image / file / @-mention parts
  string agent = 3;               // optional: "default" / "spec" / "plan" — empty falls back to default
  ModelChoice model = 4;          // optional override; empty → workspace.Resolve
}

message Part {
  oneof body {
    TextDelta text = 1;             // assistant text chunk
    ToolCall tool_call = 2;         // agent decided to call a tool
    ToolResult tool_result = 3;     // tool returned (success or error)
    SessionRef session_created = 4; // first Part when session_id was empty
    Metrics metrics = 5;            // tokens / latency / cost snapshot
    Done done = 6;                  // turn end
  }
}
```

### 2.2 Server agent loop

```
SessionService.Prompt(req):
  session = lookup_or_create(req.session_id)
  agent = pick_agent(req.agent or "default", session)
  llm = pick_provider(req.model or workspace.Resolve)

  loop:
    response = llm.complete({
      system: agent.system_prompt(session),
      messages: session.history + [user_message_from(req.parts)],
      tools: agent.tools,            # show_diff, apply_diff, freeze_spec, ...
    })

    stream response.text_chunks → TextDelta parts
    if response.tool_calls:
      for call in response.tool_calls:
        emit ToolCall part
        result = tool_registry[call.name].execute(call.args, session)
        emit ToolResult part
        session.history.append(call, result)
      continue           # next iteration: model sees results, may chain
    break                # no more tool calls; turn done
  emit Done
```

### 2.3 Tool registry (verb → tool surface)

기존 verb-mode CLI subcommand가 그대로 agent tool로 노출된다:

| Tool name | Effect |
|---|---|
| `show_diff` | unified diff between latest checkpoint and workspace |
| `apply_diff` | accept current workspace state (or commit to user git) |
| `freeze_spec` | mark spec frozen; required before `start_run` |
| `start_run` | RunService.Start internally → returns session id (existing RPC) |
| `request_compact` | RunService.RequestCompact (existing RPC) |
| `list_sessions` | session list (newest first) |
| `switch_session` | flip active session id (chat client receives new ID via session_created Part) |
| `show_status` | session metadata + current iter / cost |
| `show_spec` | current spec JSON |
| `add_to_workingset` | (future) WorkingSet slot mutation |
| `read_file` / `repomap` / `bash` / `edit` / `write_file` | existing core/tool primitives, exposed to chat agent loop |

각 tool의 input/output schema는 LLM이 결정에 쓸 수 있도록 JSON schema로 등록.

### 2.4 Default agent system prompt (요약 shape)

```
You are gil, an autonomous coding harness assistant. The user types naturally;
you decide whether to:
  - reply conversationally (greetings, meta-questions about gil)
  - ask clarifying questions to build a spec for the requested task
  - call tools to inspect the workspace, show diffs, or freeze + run agent

Tools available: { show_diff, apply_diff, freeze_spec, start_run, ... }

Spec elicitation: when the user describes a task, drive a few clarifying
questions covering goal / scope / constraints / success criteria before
freeze_spec. Don't enforce a fixed question order — judge what's needed.

When the workspace already has a frozen spec and the user says "run" / "go" /
"start" / "let's do it" / 어떤 phrasing이든, call start_run.

When the user asks to see changes ("diff", "show me what changed", "변경사항"),
call show_diff. When they say "apply" / "merge" / "approve" / "accept" /
"커밋해", call apply_diff.

Don't enumerate slash commands — there are none. Don't offer "type /help" —
just answer what they asked.
```

### 2.5 Interview engine 폐기

기존 `InterviewService.Start/Reply` + `core/interview/{slotfill,audit,adversary,engine,sensing}` 모듈들은 본 architecture로의 마이그레이션 후 **삭제**:

- sensing classifier → agent system prompt에 "domain은 LLM이 알아서 추론"
- slot fill → agent가 clarifying question을 자연어로 묻고 spec proto 채움 (서버 tool `update_spec`)
- adversary critique → agent가 자기 검토 단계로 (system prompt에 "내 spec 가정을 한 번 비판해봐") OR 별도 `adversary_review` tool
- audit gate → agent가 `freeze_spec` 호출 전 self-check (system prompt 지시)

이게 메모리에 박힌 "에이전트 결정 vs 시스템 안전망" 원칙의 정직한 실현. 시스템은 schema (FrozenSpec proto), limit (max_iter / budget), termination (run_done event), persistence (sessions DB) — 4개만 잡고 나머지 다 agent.

### 2.6 Multi-agent 확장 (opencode plan/build 패턴)

장기적으로 agent 종류를 늘려도 같은 RPC 사용:

```
gil chat                       # default agent (chat-with-spec-tools)
gil chat --agent plan          # plan agent (탐색 + 계획 + 사용자 확인 prompt)
gil chat --agent build         # build agent (full toolset, 즉시 실행)
gil chat --agent <custom>      # ~/.config/gil/agents/<name>.toml 정의
```

PromptRequest.agent 필드가 이 분기를 표현. spec build / interview-then-run UX를
원하는 사용자는 `--agent spec` 또는 자연어로 "spec 먼저 짜줘" 하면 default agent가
spec agent를 sub-agent로 위임.

## 3. Client surface

### 3.1 chat client (cli/internal/chat/repl + tui/internal/app)

코드 책임:
1. textinput / textarea 운영
2. 입력 enter 시 `SessionService.Prompt(parts: [text(...)])` 호출
3. stream `Part` 받아 transcript에 렌더링 (text는 chunk 합치고, tool_call은 ⚒ 라인, tool_result는 결과 라인)
4. quit 단축키 (Ctrl+C / Ctrl+D)
5. history scrollback (textinput history)
6. resize handling

코드에 없을 것:
- ParseInput / IsKnownSlash / SlashRequiresSession
- intent.Router / intent.Classify / verb patterns
- dispatchSlash / verbToSlashArgs / dispatchVerb (TUI 측도)
- 사용자 입력에 대한 어떤 if/switch 분류

### 3.2 verb-mode subcommands

`gil status <id>`, `gil diff <id>`, `gil run <id>` 등은 **headless/script**
용으로 보존. 사람용 surface는 chat 단일.

`gil status` 류는 직접 SessionService.List / RunService.Diff / RunService.Start
RPC 호출 (chat agent 우회).

## 4. Migration plan

phase 단위, 각 단계 배포 가능:

### M1 — daemon side (`SessionService.Prompt` 신설)

- proto: `Prompt` RPC + `Part` oneof + `Tool*` 메시지 추가
- server: agent loop 구현 (system prompt + tool registry + provider call + parts streaming)
- tool registry: 기존 RunService 기능 + spec mutation tool들을 등록
- 기존 InterviewService / RunService / SessionService 모두 호환 유지 (병행)

### M2 — chat client 단일 path 전환

- cli/internal/chat/repl: ParseInput / dispatchSlash 제거, SessionService.Prompt 단일
- tui/internal/app: 동일
- intent router stub 완전 제거 (호환 alias 유지 → 다음 phase에서 삭제)

### M3 — interview engine 폐기

- core/interview/ 디렉토리 삭제 (sensing / slotfill / audit / adversary / engine)
- InterviewService RPC 삭제
- SDK에서 StartInterview/ReplyInterview 제거
- 폐기 commit은 단독으로 — diff가 크고 review 가치 큼

### M4 — agent abstraction

- Agent struct + tool registry 명시화
- default / spec / plan / build agent 변종
- `gil chat --agent <name>` 옵션
- `~/.config/gil/agents/` user-defined agent 로딩

### M5 — verb-mode subcommand 정리

- `gil status` 등 headless 전용 표기 (man page / --help 분리)
- bare `gil` → chat surface 진입만

## 5. Open questions

- spec proto가 그대로 가? agent가 자유롭게 갱신해도 schema 보장은?
- multi-step agent (plan agent가 build agent 호출) 시 session/sub-session 분리?
- workspace 상태 변화 (외부 git pull 등)을 agent가 인지하는 방법 — tool 호출 시
  매번 stat? 또는 ChangeStream subscriber?
- `apply_diff`의 정확한 시맨틱 — checkpoint 갱신만? 사용자 git에 commit?
  (이건 #258 followup 도메인)
- agent의 tool 사용 budget — system이 얼마나 enforce하나?
