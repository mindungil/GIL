# 참조 하네스 7종 심층 분석 — Go 이식 매핑

**날짜**: 2026-04-25
**목적**: 자율 코딩 하네스 (Go, CLI, 며칠 무인) 설계의 근거 자료. 코드 라인 단위로 검증된 패턴만 기록.

---

## 1. Goose (`aaif-goose/goose`, Rust, Apache-2.0, 43K⭐)

### 1.1 Recipe DSL — `crates/goose/src/recipe/mod.rs:41-86`
- 필드: version/title/description/instructions/prompt/extensions/settings/parameters/response/sub_recipes/retry/activities
- Jinja-style `{{ var }}` 치환 (MiniJinja, `template_recipe.rs:92-115`)
- `recipe_dir` 변수 자동 주입

### 1.2 RetryConfig — `crates/goose/src/agents/types.rs:22-37` ⭐
```rust
pub struct RetryConfig {
    pub max_retries: u32,
    pub checks: Vec<SuccessCheck>,
    pub on_failure: Option<String>,
    pub timeout_seconds: Option<u64>,        // default 300
    pub on_failure_timeout_seconds: Option<u64>, // default 600
}
pub enum SuccessCheck { Shell { command: String } }
```
- `agents/retry.rs:196-223` 실행: 각 check 순차, exit code 0 ⇒ 통과
- ⚠️ retry 시 메시지 히스토리 초기화 (156-161) — 우리는 다르게 (히스토리 보존)

### 1.3 6 백엔드 ExtensionConfig — `agents/extension.rs:151-286`
- `Builtin / Stdio / StreamableHttp / Platform / Frontend / InlinePython` (Sse deprecated)
- **차단 환경변수 31개 화이트리스트**: `PATH/LD_PRELOAD/PYTHONPATH/CLASSPATH/NODE_OPTIONS/APPINIT_DLLS …`

### 1.4 Smart Context — `context_mgmt/mod.rs`
- `DEFAULT_COMPACTION_THRESHOLD = 0.8`, `GOOSE_AUTO_COMPACT_THRESHOLD` env

### 1.5 SQLite v11 — `session/session_manager.rs:22`
- 테이블: `sessions / messages / threads / thread_messages / schema_version`
- 스키마 전체 확보 (별도 인용)

### 1.6 GooseMode — `config/goose_mode.rs:24-34`
- `Auto / Approve / SmartApprove / Chat`
- SmartApprove = read-only 도구 주석 + LLM detect_candidates + 결정 캐싱

### 1.7 Adversary Reviewer — `security/adversary_inspector.rs` ⭐
- `~/.config/goose/adversary.md` 파일 기반 (없으면 비활성)
- 시스템 프롬프트: *"You are an adversarial security reviewer ... Respond with ALLOW or BLOCK on the first line, then a brief reason"*
- `MAX_RECENT_USER_MESSAGES = 4`, 사용자 작업은 500자 제한
- **Fail-open**: LLM 실패 시 ALLOW

---

## 2. Codex (`openai/codex`, Rust, Apache-2.0, 78K⭐)

### 2.1 Linux Sandbox — `linux-sandbox/src/bwrap.rs:144-158` ⭐
- 기본 인자: `--new-session --die-with-parent --bind / / --unshare-user --unshare-pid` (+`--unshare-net` 조건부)
- `BwrapNetworkMode { FullAccess, Isolated, ProxyOnly }`
- `--ro-bind / /` (전체 읽기 전용) vs `--tmpfs /` (제한 모드)
- 보호 경로 재바인드: `.git`, `.codex` 마스킹

### 2.2 TCP→UDS→TCP 프록시 — `proxy_routing.rs:419-473` ⭐
- fork → UnixListener → bidirectional proxy
- 14 env: HTTP_PROXY / HTTPS_PROXY / NPM_CONFIG_* / BUNDLE_* / PIP_PROXY / DOCKER_*

### 2.3 apply-patch DSL — `apply-patch/src/parser.rs:4-42` ⭐
```
start: begin_patch hunk+ end_patch
hunk: add_hunk | delete_hunk | update_hunk
update_hunk: "*** Update File: " filename LF change_move? change?
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.+)/ LF
```
- ParseMode: `Strict / Lenient(GPT-4.1 호환) / Streaming`
- Hunk: `AddFile { path, contents } / DeleteFile { path } / UpdateFile { path, move_path?, chunks }`

### 2.4 Rollout — `rollout/src/lib.rs:21-22`
- `~/.codex/sessions/rollout-YYYY-MM-DDTHH-MM-SS-{uuid}.jsonl`
- `~/.codex/archived_sessions/`
- `RolloutCmd { AddItems, Persist, Flush, Shutdown }` 비동기 ack 채널

### 2.5 Compact — `core/src/compact.rs`
- `COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000` (line 45)
- `history.remove_first_item()` 재시도 루프 (line 222) — `ContextWindowExceeded` 처리
- 보존: 최근 user / ghost snapshots / initial context / last assistant
- 프롬프트: `templates/compact/prompt.md` — *"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary…"*

### 2.6 Approval × Sandbox 매트릭스
- Approval: `Untrusted / OnFailure(deprecated) / OnRequest / Never`
- Sandbox: `ReadOnly / WorkspaceWrite / DangerFullAccess`
- `--full-auto` = OnRequest + WorkspaceWrite
- `--yolo` = Bypass 모두

### 2.7 AGENTS.md — `config/src/config_toml.rs:202-206`
- `project_doc_max_bytes`, `project_doc_fallback_filenames`
- `developer_instructions`로 통합 → `build_developer_update_item`이 prepend

---

## 3. OpenHands (`All-Hands-AI/OpenHands`, Python, MIT, 72K⭐)

### 3.1 EventStream — `openhands/events/stream.py:43-292` ⭐
- ID 자동증가, 중복 검출
- `EventStreamSubscriber` enum: AGENT_CONTROLLER / RESOLVER / SERVER / RUNTIME / MEMORY / MAIN / TEST
- 각 subscriber × callback_id 마다 ThreadPoolExecutor + queue
- 시크릿 마스킹 (`_replace_secrets`) → `<secret_hidden>`
- 페이지 캐시: 25 이벤트/페이지

### 3.2 파일 레이아웃 — `events/event_store.py`
- `sessions/{sid}/events/{id}.json`
- `sessions/{sid}/events/event_cache/{start}-{end}.json`
- Event JSON: `{id, timestamp, source, action|observation, args|content, extras}`

### 3.3 Action/Observation 계층
- Action: `CmdRunAction / IPythonRunCellAction / FileEditAction / BrowseInteractiveAction / MCPAction / AgentFinishAction / AgentDelegateAction`
- Observation: `CmdOutputObservation (MAX_CMD_OUTPUT_SIZE=30000) / ErrorObservation / AgentCondensationObservation / RecallObservation / LoopDetectionObservation`
- `runnable: ClassVar[bool]` 로 dispatch 라우팅

### 3.4 LLMSummarizingCondenser — `condenser_config.py:68-92`
- `max_size = 100`, `keep_first = 1`, `max_event_length = 10_000`
- `len(history) > max_size` 트리거
- `AgentCondensationObservation` 발행 (원본 이벤트 보존, **비파괴**)

### 3.5 V1 SDK 마이그레이션 ⚠️
- StuckDetector 5패턴 코드는 `software-agent-sdk` 별도 repo로 이동
- 이번 분석에서는 V0 leftover만 확인
- 5패턴 사양은 docs로 확인됨:
  1. 동일 Action–Observation 4회 반복
  2. 동일 Action–Error 3회 반복
  3. 사용자 입력 없는 monologue 3+
  4. Action–Observation 페어 핑퐁 6+ 사이클
  5. 컨텍스트 윈도우 에러 반복

### 3.6 Microagents
- `RecallType { WORKSPACE_CONTEXT, KNOWLEDGE }`
- `MicroagentKnowledge { name, trigger, content }`
- YAML frontmatter + markdown body
- `.openhands/microagents/` 디렉터리

---

## 4. OpenCode (`anomalyco/opencode`, TS+Go TUI, MIT, 149K⭐)

### 4.1 Hono Server — `packages/opencode/src/server/server.ts:39-82`
- 라우트: `/project /session /permission /question /provider /mcp /tui /event`
- OpenAPI 3.1.1 스펙 자동 생성 → `/doc`
- TUI 연결: `/tui/next` (롱폴링) + `/tui/response` 큐

### 4.2 Git Snapshot — `snapshot/index.ts:82-225` ⭐
- 별도 `--git-dir` = `~/.opencode/data/snapshot/{projectID}/{hash}`
- 사용자 `.git` *전혀* 미접근
- `write-tree → read-tree → checkout-index` 패턴
- Effect-TS로 작성 (Go에선 일반 함수로)

### 4.3 Permission — `permission/index.ts`
- `Rule { permission, pattern, action: "allow"|"deny"|"ask" }`
- `findLast(rules, ...)` (명시적 규칙 우선)
- Glob: `@opencode-ai/shared/util/glob` (minimatch 호환)
- `disabled()`: tool 단위 차단 Set 반환 (Plan 모드용)

### 4.4 Plan Mode — `session/prompt.ts:275-310`
- `plan.txt` 텍스트 프래그먼트를 user message에 prepend
- `plan_exit` 도구로 명시적 build 전환 (사용자 승인 필요)
- 모드는 message의 `agent` 필드로 추적

### 4.5 압축 요약 템플릿 — `session/compaction.ts:40-75` ⭐
```markdown
## Goal
- [single-sentence task summary]

## Constraints & Preferences

## Progress
### Done
### In Progress
### Blocked
```

### 4.6 Subagent (`task` tool) — `tool/task.ts:37-97`
- `parentID` 관계
- 자식에서 `task` 도구 비활성화 (무한 재귀 방지)
- `taskID` 있으면 재개, 없으면 신규 세션 생성

### 4.7 Revert — `session/revert.ts:42-100`
- 세션 메시지마다 `revert: { messageID, partID, snapshot, diff }`
- `unrevert`로 redo 가능

---

## 5. Aider (`Aider-AI/aider`, Python, Apache-2.0, 44K⭐)

### 5.1 RepoMap 이진검색 — `repomap.py:676-706` ⭐
```python
middle = min(int(max_map_tokens // 25), num_tags)
ok_err = 0.15  # 15% 허용오차

while lower_bound <= upper_bound:
    tree = self.to_tree(ranked_tags[:middle], chat_rel_fnames)
    num_tokens = self.token_count(tree)
    pct_err = abs(num_tokens - max_map_tokens) / max_map_tokens
    if (num_tokens <= max_map_tokens and num_tokens > best) or pct_err < ok_err:
        best_tree = tree
        if pct_err < ok_err: break
    if num_tokens < max_map_tokens: lower_bound = middle + 1
    else: upper_bound = middle - 1
    middle = (lower_bound + upper_bound) // 2
```

### 5.2 PageRank 가중치 — `repomap.py:365-514`
- chat referencer × **50**
- mentioned ident × **10**, snake_case ≥8자 × **10**
- `_private` × 0.1, 공통 정의 × 0.1
- Edge: `sqrt(num_refs) * use_mul`

### 5.3 Tree-sitter 통합
- 56언어 지원, `.scm` 쿼리 파일
- `name.definition.*` (정의) / `name.reference.*` (참조)
- 캐시: `.aider.tags.cache.v3` (mtime 무효화)

### 5.4 EditBlock 4단 매칭 — `editblock_coder.py:146-329` ⭐
- Regex: `r"^<{5,9} SEARCH>?\s*$"` / `r"^={5,9}\s*$"` / `r"^>{5,9} REPLACE\s*$"`
- 1단 perfect / 2단 whitespace-flex / 3단 `...` ellipsis / 4단 fuzzy (SequenceMatcher ≥0.8, ±10% 길이)
- 실패 시 `find_similar_lines()` "Did you mean…" 힌트 + LLM에 reflected_message
- `max_reflections = 3`

### 5.5 Architect/Editor 분리 — `architect_coder.py`
- editor_coder가 `total_cost`, `aider_commit_hashes` 부모로 핸드오프

### 5.6 Auto-lint/test — `base_coder.py:1599-1623`
- 에러 → `reflected_message` → 다음 reflection 진입
- 사용자 confirm 후 자가 수정 시도

### 5.7 ChatSummary
- ❌ 진정한 요약 아님 — 그룹화 + 꼬리 버림 (recursive)
- 우리는 OpenCode식 구조화 요약 채택

---

## 6. Cline (`cline/cline`, TS, Apache-2.0, 61K⭐)

### 6.1 Shadow Git — `integrations/checkpoints/CheckpointUtils.ts:20-24` ⭐
- 경로: `globalStorage/checkpoints/{cwdHash}/.git`
- `core.worktree`로 사용자 워크스페이스 가리킴
- Identity: `user.name="Cline Checkpoint" / user.email="checkpoint@cline.bot"`
- 커밋 메시지: `checkpoint-{cwdHash}-{taskId}`
- `--allow-empty --no-verify`
- 폴더 락 (동시성 방지)

### 6.2 Exclusions — `CheckpointExclusions.ts:42-70`
- `node_modules / build / dist / 미디어 / 캐시 / .env / 데이터베이스 / 로그`
- 중첩 git 임시 비활성 (`.git_disabled` 접미사)

### 6.3 3-way 복원 — `index.ts:238-375`
- `task` (대화만) / `workspace` (파일만, `resetHead()`) / `taskAndWorkspace` (둘 다)

### 6.4 READ_ONLY_TOOLS — `shared/tools.ts:56-67`
- `list_files / file_read / search / list_code_def / browser / ask / web_search / web_fetch / use_skill / use_subagents`

### 6.5 Loop Detection — `core/task/loop-detection.ts`
- `SOFT_THRESHOLD = 3` (경고 주입)
- `HARD_THRESHOLD = 5` (에스컬레이션)
- 단순 동일 tool+params 카운트 (의미적 루프 못 잡음)

### 6.6 Context Truncation — `context-window-utils.ts:10-35`
```ts
64_000:  contextWindow - 27_000  // DeepSeek
128_000: contextWindow - 30_000
200_000: contextWindow - 40_000  // Claude
default: max(contextWindow - 40_000, contextWindow * 0.8)
```

### 6.7 9-카테고리 Auto-approve
- read-project / read-external / edit-project / edit-external / safe-cmd / all-cmd / browser / MCP / notifications
- `isLocatedInWorkspace()` 경계 검증

### 6.8 PromptBuilder Variants
- `generic / next-gen / native-gpt-5 / xs / gemini-3 / gpt-5 / hermes / glm`
- 모델 family별 최적화 프롬프트

### 6.9 .clinerules
- YAML frontmatter + markdown body
- `evaluateRuleConditionals()` 조건부 활성화

### 6.10 Memory Bank — ❌ 코드로 구현 안 됨
- 시스템 프롬프트 패턴(model에게 마크다운 유지하라 지시)일 뿐
- **우리는 진짜 구조화된 메모리로 직접 구현 필요**

---

## 7. Hermes Agent (`NousResearch/hermes-agent`, Python, MIT, 116K⭐)

### 7.1 execute_code 다단계 압축 — `tools/code_execution_tool.py` ⭐
- UDS RPC (로컬) + 파일 기반 RPC (Modal 등)
- 7개 sandbox tools만: `web_search/web_extract/read_file/write_file/search_files/patch/terminal`
- DEFAULT_TIMEOUT=300s, MAX_TOOL_CALLS=50, MAX_STDOUT_BYTES=50KB
- **중간 결과 컨텍스트 미진입 — 최종 stdout만 LLM에 반환**

### 7.2 캐시 보존 압축 불변식 — `agent/context_compressor.py:1136-1306` ⭐⭐⭐
- **Head 보호** (시스템 + 초기 N), **Tail 보호** (최근 토큰 예산)
- **Middle만 요약**
- `copy.deepcopy()` 로 원본 절대 미변경
- System prompt → session DB 저장 → 압축 후 재사용 (prefix cache 유지)
- **Anti-thrashing**: 최근 2회 절감 <10%면 skip
- 도구 결과 가지치기 (LLM 호출 없이 한 줄 요약 치환)

### 7.3 Anthropic 캐시 "system-and-3" — `agent/prompt_caching.py:41-72`
- `messages[0]` (system) + 마지막 3개 non-system에 cache_control
- 최대 4 breakpoints (Anthropic 제한)

### 7.4 IterationBudget + grace call — `run_agent.py:9652-9677`
```python
while (api_call_count < self.max_iterations and 
       self.iteration_budget.remaining > 0) or self._budget_grace_call:
    if self._budget_grace_call:
        self._budget_grace_call = False
    elif not self.iteration_budget.consume():
        _turn_exit_reason = "budget_exhausted"
        break
```

### 7.5 Memory Manager — `agent/memory_manager.py:84-150`
- 1 builtin + max 1 external provider
- `<memory-context>` + `[System note: ...]` 펜스
- API 호출 시만 inject (영속화 X)
- Turn-based nudge interval

### 7.6 delegate_task — `tools/delegate_tool.py:1235-1380`
- 부모 `_last_resolved_tool_names` save/restore
- Heartbeat + stale detection (in_tool vs idle 임계값 분리)
- `_delegate_depth` 다계층 (grandchild OK)

### 7.7 Atropos reward — `environments/agentic_opd_env.py:551-637`
- `correctness*0.7 + efficiency*0.15 + tool_usage*0.15`
- 동일 agent loop, 외부 평가 환경만 추가

### 7.8 6 터미널 백엔드 — `tools/terminal_tool.py:820-1038`
- local / docker / singularity / modal / daytona / ssh
- 통일 인터페이스: `execute(command)`, `get_temp_dir()` 등

### 7.9 Skills agentskills.io 표준 — `tools/skills_tool.py`
- `SKILL.md`: YAML frontmatter (name/description/license/compatibility/metadata) + markdown body
- `assets/` 디렉터리 (보조 파일)
- `/skills/` (내장) / `/optional-skills/` / `/plugins/`
- 자동 스킬 생성 코드 ❌ (블로그 주장과 달리 명시적 코드 없음)

---

## 종합: Go 하네스 모듈 매핑 (잠정)

| Go 패키지 | 책임 | 차용 출처 |
|---|---|---|
| `pkg/event` | 이벤트 스트림 + 영속화 + pub/sub | OpenHands `events/stream.py` (Go 채널로 변환) |
| `pkg/spec` | Frozen spec DSL (인터뷰 산출물) | Goose Recipe + 자체 확장 |
| `pkg/verify` | 검증 실행기 (`SuccessCheck::Shell`) | Goose `agents/retry.rs` |
| `pkg/sandbox` | OS/Docker 샌드박스 (정책-as-data) | Codex `linux-sandbox` |
| `pkg/patch` | apply_patch DSL 파서 | Codex `apply-patch` |
| `pkg/edit` | SEARCH/REPLACE 4단 매칭 | Aider `editblock_coder.py` |
| `pkg/repomap` | Tree-sitter + PageRank + 이진검색 | Aider `repomap.py` |
| `pkg/checkpoint` | Shadow git 비파괴 스냅샷 | OpenCode `snapshot/` + Cline `CheckpointTracker` 결합 |
| `pkg/compact` | 캐시 보존 압축 (Head/Middle/Tail) | Hermes `context_compressor.py` + OpenCode 구조화 템플릿 |
| `pkg/memory` | 영속 메모리 (markdown 6종 + 검색) | Cline Memory Bank *진짜* 구현 |
| `pkg/permission` | Allow/Ask/Deny + glob | OpenCode `permission/` |
| `pkg/stuck` | 5패턴 시맨틱 + **자가 회복** | OpenHands StuckDetector + 재시도 전략 (우리 추가) |
| `pkg/adversary` | LLM 안전 검수 | Goose `adversary_inspector.rs` |
| `pkg/exec` | 다단계 도구 압축 (UDS RPC) | Hermes `execute_code` |
| `pkg/runtime` | 멀티 백엔드 (local/docker/vm/ssh) | Hermes 6 백엔드 + Codex sandbox |
| `pkg/provider` | LLM 프로바이더 추상화 | LiteLLM 패턴 (Aider) |
| `pkg/rollout` | JSONL 세션 + resume by UUID | Codex `rollout/` |
| `pkg/interview` | 적응형 saturation 인터뷰 | 자체 (어떤 도구도 안 함) |
| `pkg/server` | Hono식 HTTP+SSE | OpenCode `server/` |
| `cmd/harness` | CLI 진입점 | 통합 |
