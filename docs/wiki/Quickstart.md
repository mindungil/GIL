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

## 2. 세션 생성

```bash
mkdir -p ~/dogfood-test && cd ~/dogfood-test
SESSION=$(gil new --working-dir $(pwd) | awk '{print $NF}')
echo $SESSION
```

## 3. 인터뷰

```bash
gil interview $SESSION --provider anthropic --model claude-sonnet-4-6
```

대화형. saturation까지 모든 슬롯 채움 — 시작 후 다시 묻지 않기 위해. 보통 5-10 turns.

## 4. 자율 실행

### Foreground (보면서)
```bash
gil run $SESSION
```

### Background (며칠짜리 권장)
```bash
gil run $SESSION --detach
gil watch $SESSION       # 라이브 진행률
# 또는
giltui                   # 4-pane mission control
```

## 5. 결과

```bash
gil status $SESSION              # 한 줄 요약
gil cost $SESSION                # 토큰 + USD
gil events $SESSION --tail       # 이벤트 로그
gil export $SESSION --format markdown > /tmp/run.md
```

## 6. 망친 경우 복원

Shadow Git checkpoint 매 iter 자동 저장:
```bash
gil restore $SESSION <step>
# step=1 (첫), -1 (마지막), 또는 명시 번호
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
