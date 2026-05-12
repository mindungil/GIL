# Provider Setup

gil은 4 provider 지원 (모두 credstore 등록 후 사용):

| Provider | 강점 | 대표 비용 (1M 토큰) | 등록 |
|---|---|---|---|
| anthropic | 가장 강한 instruction-following + native tool use | haiku $1, sonnet $3, opus $15 | `gil auth login anthropic` |
| openai | gpt-4o tool use 좋음 | $2.5 (gpt-4o-mini) | `gil auth login openai` |
| openrouter | 다양한 모델 (Claude proxy, Llama, DeepSeek, Qwen) | varies | `gil auth login openrouter` |
| vllm/local | 로컬 GPU 무료 (자체 호스팅) | $0 (HW 비용 별도) | `gil auth login vllm --base-url http://...` |

## 등록

```bash
gil auth login <provider>
# Prompt: API key (echo off)
# vllm: --base-url 도 prompt
```

저장: `$XDG_CONFIG_HOME/gil/auth.json` (mode 0600). 외부 전송 없음.

## 사용

CLI flag으로 명시:
```bash
gil run <id> --provider anthropic --model claude-haiku-4-5
gil run <id> --provider vllm --model qwen3.6-27b
```

또는 spec.yaml:
```yaml
models:
  main:
    provider: anthropic
    modelId: claude-haiku-4-5
```

## env var fallback

credstore 비어 있으면 환경변수 사용:
- `ANTHROPIC_API_KEY`
- `OPENAI_API_KEY`
- `OPENROUTER_API_KEY`
- `OPENAI_BASE_URL` (vllm)

## Architect/Coder 페어링 (Phase 19.C)

```yaml
models:
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6   # 강한 모델 — 첫 turn + plan tool 호출 시
  editor:
    provider: anthropic
    modelId: claude-haiku-4-5    # 싼 모델 — bash/edit tool-heavy turn
  main:
    provider: anthropic
    modelId: claude-haiku-4-5    # default
```

자동 routing:
- iter 1 → planner
- plan tool 호출 직후 turn → planner
- exec tool (bash/edit/write_file) 만 호출하는 turn → editor
- ambiguous → main

`model_switched` 이벤트 발생. `gil cost` 가 per-role breakdown 표시.

## OIDC 인증 (Phase 10) — multi-user

```bash
gild --foreground --grpc-tcp :7070 \
  --auth-issuer https://accounts.google.com \
  --auth-audience gil
```

UDS 연결은 default bypass (local-trusted). TCP 연결만 인증 강제.

## 자세한 비용 통제

[Cost & Budget](Cost-and-Budget) 참조.
