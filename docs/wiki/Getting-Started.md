# Getting Started

5분 안에 첫 자율 실행.

## 1. Install

```bash
git clone https://github.com/mindungil/GIL.git && cd GIL
make install   # → /usr/local/bin/{gil,gild,giltui,gilmcp}
```

요구사항: Go 1.25+, git. 옵션: bwrap (Linux sandbox), docker, ssh+rsync.

다른 옵션:
- `go install github.com/mindungil/gil/cli/cmd/gil@latest` (각 binary 따로)
- `curl -fsSL https://raw.githubusercontent.com/mindungil/GIL/main/scripts/install.sh | bash` (release tag 후)
- `brew tap mindungil/tap && brew install gil` (tap 등록 후)

## 2. 첫 셋업

```bash
gil init               # XDG dirs + (대화형) auth login
```

XDG 위치 (Linux):
- `~/.config/gil/` — config + auth.json (0600)
- `~/.local/share/gil/sessions/` — 세션 데이터
- `~/.local/state/gil/` — gild socket + logs
- `~/.cache/gil/` — 모델 카탈로그 + repomap cache

전체 한 디렉토리: `export GIL_HOME=/path`. 마이그레이션: 기존 `~/.gil/` 사용자 `gil init` 1회 실행.

## 3. Provider 등록

```bash
gil auth login                    # picker (anthropic/openai/openrouter/vllm)
gil auth login anthropic
gil auth login vllm --base-url http://your-vllm:8000/v1
```

저장: `$XDG_CONFIG_HOME/gil/auth.json` (mode 0600). 외부 전송 없음.

확인: `gil auth list` / `gil doctor`.

## 4. 환경 진단

```bash
gil doctor
```

5 그룹 (Layout / Daemon / Credentials / Sandboxes / Tools) 체크. 모두 OK면 준비 완료.

## 5. 첫 자율 실행

### 채팅 모드 (권장 — Phase 24)

```bash
gil           # 그냥 입력
```

→ 채팅 REPL 시작. 자연어로 task 설명:

```
› I want to add a hello.txt with today's date in ~/dogfood-test
```

gil이 자동으로:
1. 새 세션 생성 (goal + workspace 자동 채움)
2. 인터뷰 — saturation까지 follow-up
3. "Spec freeze + 자율 실행할까요?" → Y → background

### Verb 모드 (스크립트/CI용)

```bash
SESSION=$(gil new --working-dir ~/dogfood-test | awk '{print $NF}')
gil interview $SESSION
gil run $SESSION --detach
```

비대화형(stdout pipe) 시 `gil` 자동으로 verb 모드로 fallback — 스크립트 호환.

## 6. 진행 모니터링

```bash
gil watch <id>                  # 라이브 in-place
gil events <id> --tail          # 이벤트 stream
giltui                          # 4-pane TUI
gil cost <id>                   # 토큰 + USD
```

## 7. 망친 경우 복원

Shadow Git checkpoint 매 iter 자동 저장:
```bash
gil restore <id> <step>
# step=1 (첫), -1 (마지막), 또는 명시 번호
```

## 첫 task 추천

비용 < $0.10 (anthropic claude-haiku):

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

성공 → multi-file refactor 시도. 자세한 가이드: [Configuration](Configuration), [Advanced](Advanced).
