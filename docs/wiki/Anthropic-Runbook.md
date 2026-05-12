# Anthropic Runbook

`docs/dogfood/anthropic-runbook.md` 의 wiki 미러. 첫 진짜 Anthropic-driven dogfood 절차.

## 사전 조건

- gil 빌드 완료 ([Install](Install))
- ANTHROPIC_API_KEY 발급 (https://console.anthropic.com/)
- 작업할 워크스페이스 (격리 권장)

## 비용 추정 (2026-04 list price)

| 모델 | input/M | output/M | 1 dogfood 추정 |
|---|---|---|---|
| claude-opus-4-7 | $15 | $75 | $0.50–$5 |
| claude-sonnet-4-6 | $3 | $15 | $0.10–$1 |
| claude-haiku-4-5 | $1 | $5 | $0.04–$0.40 |

**권장 시작점**: claude-haiku-4-5. 작은 task ~$0.10.

## 절차 요약

```bash
# 1. 격리 GIL_HOME
export GIL_HOME=$HOME/.config/gil-anthropic-test
gil init --no-auth --no-config

# 2. 자격증명
gil auth login anthropic

# 3. 인터뷰 + 실행
mkdir ~/dogfood-task && cd ~/dogfood-task
SESSION=$(gil new --working-dir $(pwd) | awk '{print $NF}')
gil interview $SESSION --provider anthropic --model claude-sonnet-4-6
gil run $SESSION --provider anthropic --model claude-haiku-4-5 --detach
gil watch $SESSION

# 4. 결과
gil status $SESSION
gil cost $SESSION
gil export $SESSION --format markdown > /tmp/run-report.md
```

## Architect/Coder 페어링

```yaml
models:
  planner:
    provider: anthropic
    modelId: claude-sonnet-4-6
  editor:
    provider: anthropic
    modelId: claude-haiku-4-5
```

→ 1 task 비용: ~$0.30 (Sonnet planning ~5 turns + Haiku editing ~15 turns).

## 비용 폭주 방지

```yaml
budget:
  maxIterations: 20
  maxTotalTokens: 100000        # = $1.50 max with Sonnet
  maxTotalCostUSD: 2.00
  reserveTokens: 8000
```

자세한 절차: `docs/dogfood/anthropic-runbook.md`.

[Self-dogfood Reports](Self-dogfood-Reports) — 11 qwen run 결과 참조 (Anthropic 비교 baseline).
