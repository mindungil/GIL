# Quickstart — 5분 첫 자율 실행

## 0. 사전 준비

[Install](Install) 완료 + provider 자격증명 등록.

## 1. Provider 등록

```bash
gil auth login anthropic       # 또는 openai / openrouter / vllm
# 키 입력 (echo off)
```

확인:
```bash
gil auth list
```

## 2. 그냥 `gil`

```bash
gil           # 채팅이 뜸. 자연어로 task 설명. 그게 전부.
```

채팅장이 뜨면 평소처럼 한국어/영어로 task를 말하면 됨. 예:

```
› I want to add hello.txt with today's date in ~/dogfood
```

gil이 알아서:
1. 새 세션 생성 (goal + workspace 자동 채움)
2. 인터뷰 — saturation까지 후속 질문
3. "Spec 동결하고 자율 실행할까요?" → Y → 백그라운드 실행

### 채팅은 LLM-주도 (Phase 24)

채팅 표면은 작은 LLM (haiku 등)에 매 turn 보내고, LLM이 tool call로
다음 행동을 결정하는 방식. 인사("안녕")는 그냥 인사로 받고, 항의
("아니 안녕이라니까")도 항의로 받음 — regex로 분류해서 강제로
세션을 만드는 짓을 하지 않음. 진짜 task 설명("fix lint in auth.go")
일 때만 `start_interview` tool call이 나가서 인터뷰가 시작됨.

LLM 자격증명이 없으면 limited mode로 떨어져 regex fast-path만 동작 —
`/quit`, `status`, `help` 정도는 동작하고 새 task는 확인 프롬프트
거쳐 진행 (`gil auth login`을 권장).

진행 모니터링:
```bash
gil watch <session-id>     # 라이브 진행률
gil events <session-id> --tail
```

## 3. (선택) Verb-mode — 스크립트/CI용

채팅이 아닌 명시적 명령으로도 가능. 채팅 안 띄우고 싶을 때:

```bash
gil --no-chat                       # 기존 mission-control 요약
SESSION=$(gil new --working-dir $(pwd) | awk '{print $NF}')
gil interview $SESSION --provider anthropic --model claude-sonnet-4-6
gil run $SESSION --detach
```

또는 비대화형(stdout이 파이프됨)이면 `gil`은 자동으로 요약 모드로 떨어짐 — 스크립트는 손댈 필요 없음.

## 4. 결과

```bash
gil status <session-id>          # 한 줄 요약
gil cost <session-id>            # 토큰 + USD
gil events <session-id> --tail   # 이벤트 로그
gil export <session-id> --format markdown > /tmp/run.md
```

## 5. 망친 경우 복원

Shadow Git checkpoint 매 iter 자동 저장:
```bash
gil restore <session-id> <step>
# step=1 (첫), -1 (마지막), 또는 명시 번호
```

## 6. 기존 세션 이어가기

채팅에서 자연어로 "continue yesterday's task" 또는 "resume" 등 입력 — gil이 picker를 띄움. 명시적으로:

```bash
gil resume <session-id>
```

## 첫 task 추천 — trivial

비용 < $0.10:

```yaml
goal:
  oneLiner: "Add hello.txt with today's date"
verification:
  checks:
    - name: file-exists
      kind: SHELL
      command: test -f hello.txt
budget:
  maxIterations: 5
  maxTotalTokens: 30000
```

성공 → multi-file refactor 시도. 자세한 단계는 [User Guide](https://github.com/mindungil/GIL/blob/main/docs/USER_GUIDE.md).

## TUI

대시보드를 원하면:
```bash
giltui                   # 4-pane mission control
```
