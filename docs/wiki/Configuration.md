# Configuration

Provider / AGENTS.md / autonomy / cost — 모든 설정 통합.

## Provider Setup

| Provider | 강점 | 비용/1M | 등록 |
|---|---|---|---|
| anthropic | 가장 강한 instruction-following + native tool use | haiku $1, sonnet $3, opus $15 | `gil auth login anthropic` |
| openai | gpt-4o tool use 좋음 | $2.5 (gpt-4o-mini) | `gil auth login openai` |
| openrouter | 다양한 모델 (Claude proxy, Llama, DeepSeek, Qwen) | varies | `gil auth login openrouter` |
| vllm/local | 로컬 GPU 무료 | $0 (HW 비용 별도) | `gil auth login vllm --base-url http://...` |

```bash
gil auth login <provider>   # 키 입력 (echo off)
gil auth list               # 등록된 provider + masked key
gil auth logout <provider>
```

저장: `$XDG_CONFIG_HOME/gil/auth.json` (mode 0600). env var fallback (`ANTHROPIC_API_KEY` 등) 도 지원.

CLI flag으로 명시:
```bash
gil run <id> --provider anthropic --model claude-haiku-4-5
```

또는 spec.yaml `models.main`.

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
