# Configuration

Provider / AGENTS.md / autonomy / cost — 모든 설정 통합.

## Provider Setup

`gil auth login` (no args) 은 **multi-step wizard** — provider 선택 → API 키 → default 모델 → optional connection test. 일반 사용자가 docs를 안 봐도 끝까지 통과 가능하도록 설계.

### Supported providers

| Provider | 강점 | 비용/1M | 추천 default |
|---|---|---|---|
| `anthropic` | 가장 강한 instruction-following + native tool use | haiku $1, sonnet $3, opus $15 | `claude-haiku-4-5` |
| `openai` | gpt-4o tool use 좋음 | $2.5 (gpt-4o-mini) | `gpt-4o-mini` |
| `openrouter` | 100+ 모델 proxy (Claude, Llama, Qwen, Gemini, …) | varies | `anthropic/claude-haiku-4-5` |
| `vllm` (self-hosted) | 로컬 GPU 무료 | $0 (HW 비용 별도) | (사용자가 endpoint 모델 입력) |

### Interactive wizard

```text
$ gil auth login

  G I L  ─  Provider Setup
  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  1. Pick a provider:

     [1]  Anthropic                 claude-opus / sonnet / haiku  ·  best tool use
     [2]  OpenAI                    gpt-4o / gpt-4o-mini / o1
     [3]  OpenRouter                proxy for 100+ models (anthropic, llama, qwen, …)
     [4]  vLLM (self-hosted, OpenAI-compatible)   your endpoint  ·  any open-weights model
     [c]  cancel

  Your choice [1-4]: 3

  2. OpenRouter API key:

     Get one at  https://openrouter.ai/keys
     Looks like  sk-or-v1-…

  API key (input hidden): ••••••••••••••••

  3. Default model:

     Recommended for openrouter:

     [1]  anthropic/claude-haiku-4-5     cheap, fast — default
     [2]  anthropic/claude-sonnet-4-6    strong, balanced
     [3]  meta-llama/llama-3.3-70b       open
     [4]  google/gemini-2.5-flash        cheap
     [5]  qwen/qwen3-32b                 open + capable
     [6]  Other (type model id)

  Your choice [1-6, default 1]:

  ✓ Saved to: ~/.config/gil/auth.json (mode 0600)

  Test connection? [Y/n]: y
  ⠋ testing…
  ✓ Connection OK · ok · model anthropic/claude-haiku-4-5 · 412ms (in:7 out:1)

  Next:  gil  (start chat)
         gil auth list / edit / test / logout
```

### Non-interactive (scripts / CI)

기존 one-shot flag 들은 그대로 동작 — wizard 안 띄우고 바로 저장:

```bash
gil auth login anthropic --api-key sk-ant-... --model claude-haiku-4-5 --no-test
gil auth login vllm --base-url http://localhost:8000/v1 --api-key local --model qwen3-32b --no-test
```

### Subcommands

| Command | Purpose |
|---|---|
| `gil auth login [provider]` | wizard (or non-interactive when --api-key set) |
| `gil auth list` | 등록된 provider + masked key + model |
| `gil auth edit <provider>` | 키/base-url/model 변경 (existing 값 유지 가능) |
| `gil auth test <provider>` | 저장된 자격증명으로 1-token smoke test |
| `gil auth status` | credstore + env-var fallback 동시 표시 |
| `gil auth logout <provider>` | 자격증명 제거 |

저장: `$XDG_CONFIG_HOME/gil/auth.json` (mode 0600). env var fallback (`ANTHROPIC_API_KEY` 등) 도 지원.

### CLI flag으로 명시

```bash
gil run <id> --provider anthropic --model claude-haiku-4-5
```

또는 spec.yaml `models.main`. **resolution order**: `--model` flag > `credstore.Credential.Model` (wizard에서 저장됨) > 환경변수 (`GIL_VLLM_MODEL`) > provider별 hardcoded default (vllm은 default 없음 — wizard 강제).

## AGENTS.md (영구 instructions)

`<workspace>/AGENTS.md` 자동 트리워크 (Phase 12). agent 매 turn system prompt 에 prepend.

발견 순서 (priority low → high):
1. `$HOME/AGENTS.md`
2. `$XDG_CONFIG_HOME/gil/AGENTS.md`
3. ancestors → workspace: AGENTS.md / CLAUDE.md / `.cursor/rules/*.mdc`

예시:
```markdown
# Project conventions
- Go 1.25+, gofmt + goimports
- _test.go in same package
- Errors via cliutil.UserError pattern
- DO NOT add new third-party deps without justification
```

→ 인터뷰 시간 단축 (이미 알고 있는 건 안 묻음) + 매 turn 일관된 컨벤션.

## Autonomy Dial (`spec.risk.autonomy`)

| Level | 의미 | 권장 |
|---|---|---|
| `FULL` | 모든 도구 무제한 | 격리된 sandbox |
| `ASK_DESTRUCTIVE_ONLY` | rm/mv/chmod/chown/dd/mkfs/sudo 만 ask | **기본** |
| `ASK_PER_ACTION` | 모든 도구 ask | TUI + interactive supervision |
| `PLAN_ONLY` | 실행 차단, plan tool만 | "agent 가 어떻게 하려는지" 미리 보기 |

### Phase 22.A bash chain hardening

`cp X.bak && mv X X.bak` 같은 chain 명령에서 **각 sub-command 별 평가**. `mv`가 deny 리스트에 있으면 chain 통과 안 됨.

### 영속 always_allow / always_deny

TUI permission ask 6 옵션:
- `[a]` allow once / `[s]` allow session / `[A]` always allow (디스크 저장)
- `[d]` deny once / `[D]` always deny (디스크 저장)
- `[esc]` cancel

저장: `$XDG_STATE_HOME/gil/permissions.toml` (project absolute path 별, mode 0600).

```bash
gil permissions list
gil permissions remove "rm *" --deny --project /abs/path
gil permissions clear --yes
```

평가 우선순위: persistent_deny > persistent_allow > session > spec rules > default.

## Cost & Budget

`spec.run.budget`:
```yaml
budget:
  maxIterations: 30
  maxTotalTokens: 500000
  maxTotalCostUSD: 5.00
  reserveTokens: 20000      # 마지막 verifier 위한 reserve (Phase 19.A)
```

### Status taxonomy (Phase 19.A)

| Status | 의미 |
|---|---|
| `done` | end_turn + verifier ✓ |
| `verify_failed` | end_turn but verifier ✗ |
| `budget_exhausted` | budget hit |
| `budget_exhausted_verify_passed` | budget hit but verifier ✓ |
| `budget_exhausted_verify_failed` | budget hit + verifier ✗ |
| `max_iterations` | iter cap |
| `stuck` | 4 recovery strategies 모두 미회복 |

### 모델 가격 카탈로그 (`core/cost/default_catalog.json`)

| Model | input/M | output/M |
|---|---|---|
| claude-opus-4-7 | $15 | $75 |
| claude-sonnet-4-6 | $3 | $15 |
| claude-haiku-4-5 | $1 | $5 |
| gpt-4o | $2.5 | $10 |
| gpt-4o-mini | $0.15 | $0.60 |
| qwen3.6-27b | $0 | $0 |

### 모니터링

```bash
gil cost <id>                    # 단일 세션 — 토큰 + USD
gil cost <id> --output json
gil stats [--days N]             # 누적
```

TUI 라이브 미터: 75% 도달 시 amber + "approaching limit", 100% 시 coral + "EXHAUSTED".

`gil watch <id>` 도 `▲ +$0.04 / min` 비용 변화율 표시.
