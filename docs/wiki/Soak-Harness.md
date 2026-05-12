# Soak Harness

`scripts/soak.sh` (Phase 23.B) — multi-task 연속 자율 실행으로 안정성 검증.

## 목적

- 24+ hour 연속 실행에서 누수 / panic / state corruption 발견
- 다양한 task (file write / refactor / verify) 로 도구 모두 사용
- 결과 CSV 으로 통계 분석

## 사용

```bash
# 합성 5-task rotation, 20번
./scripts/soak.sh --num 20

# 100 task with anthropic
./scripts/soak.sh --provider anthropic --model claude-haiku-4-5 --num 100

# 사용자 task list
./scripts/soak.sh --tasks-file scripts/my-tasks.txt --num 50
```

## 환경변수

| Var | 의미 |
|---|---|
| `SOAK_HOME` | 결과 저장 base dir (기본: `/tmp/gil-soak-<timestamp>`) |
| `GIL_BIN` | gil 바이너리 경로 (기본: `./bin/gil`) |
| `HELPER_DIR` | setfrozen helper 경로 |

## Tasks file format

```
<task-name>|<spec.yaml-path>
```

spec.yaml 안에서 `__SESSION__` / `__WORKSPACE__` 가 substitute됨.

## 결과 디렉토리

```
$SOAK_HOME/run-<timestamp>/
├── gil-1/
│   ├── data/sessions/<sid>/spec.yaml
│   ├── events.jsonl
│   ├── cost.json
│   └── run.log
├── gil-2/
│   └── ...
└── summary.csv
```

`summary.csv`:
```
i,task,status,iters,tokens,cost_usd,wall_seconds
1,task-1,done,3,12500,0.05,15
2,task-2,done,5,18000,0.07,22
3,task-3,budget_exhausted_verify_passed,12,80000,0.32,68
...
```

## 분석

```bash
# Status 분포
awk -F, 'NR>1{c[$3]++}END{for(s in c)printf "%s: %d\n",s,c[s]}' $SOAK_HOME/summary.csv

# 평균 token / task
awk -F, 'NR>1{t+=$5;n++}END{printf "avg tokens: %d\n",t/n}' $SOAK_HOME/summary.csv

# 총 비용
awk -F, 'NR>1{c+=$6}END{printf "total: $%.2f\n",c}' $SOAK_HOME/summary.csv
```

## 합성 task rotation

`--tasks-file` 미지정 시:
- T1 — write hello.txt
- T2 — fibonacci.go
- T3 — reverse string
- T4 — simple grep
- T5 — multi-file refactor (small)

각각 ~50k token budget / 8 iter cap. qwen3.6-27b 로 1 task ~30s.

## 24h 추정

```bash
# 작업 1개당 평균 30s (qwen) → 24h 안에 ~2880 task
# vllm 무료 → 비용 0
./scripts/soak.sh --num 500   # 안전한 시작점
```

## Anthropic 사용 시

```bash
./scripts/soak.sh --provider anthropic --model claude-haiku-4-5 --num 100
# ~ 5 hours wall, ~$10 cost (haiku)
```

## 실패 시

각 task 가 독립 GIL_HOME 사용 — 한 task 실패가 다른 것 영향 없음. summary.csv 의 status 컬럼으로 분류.

## Phase 23.B 출처

self-dogfood loop 누적 결과 — agent 가 며칠짜리 작업에서 안전하게 abort/recover 함을 검증할 도구.
