# gil — autonomous coding harness

길고 철저한 인터뷰로 모든 요구사항을 추출한 뒤, 며칠이 걸리더라도 다시 묻지 않고 끝까지 작업을 수행하는 CLI 에이전트.

## 무엇이 다른가

기존 코딩 CLI들 (Claude Code, opencode, codex 등)은 작업 도중 사용자에게 묻거나 미완성으로 끝납니다. **gil은 시작 전에 모든 것을 묻고, 시작 후에는 끝까지 자율로 갑니다.**

핵심 패턴:
- **인터뷰는 길고 철저하게** — saturation까지 모든 슬롯을 채움
- **에이전트가 결정, 시스템은 안전망** — 도구 순서/임계값은 LLM이 정함; 시스템은 스키마/budget/객관 종료/영속성만
- **단일 stop 조건** — verifier 통과 + stuck 회복 시도 끝 + budget exhausted = 작업 종료
- **캐시 보존 압축** — 며칠짜리 작업의 prompt cache prefix가 깨지지 않도록 Hermes 패턴

## Wiki 페이지

- **[Install](Install)** — 설치 (curl-installer / homebrew / build from source)
- **[Quickstart](Quickstart)** — 5분 안에 첫 자율 실행
- **[Commands](Commands)** — 모든 CLI 명령어 reference
- **[Architecture](Architecture)** — 4 binary, gRPC over UDS, core/runtime 모듈
- **[Workspace Backends](Workspace-Backends)** — LOCAL/SANDBOX/DOCKER/SSH/MODAL/DAYTONA
- **[Autonomy Dial](Autonomy-Dial)** — FULL / ASK_DESTRUCTIVE_ONLY / ASK_PER_ACTION / PLAN_ONLY
- **[Provider Setup](Provider-Setup)** — anthropic / openai / openrouter / vllm
- **[AGENTS.md](AGENTS-md)** — 프로젝트별 영구 instructions
- **[Architect-Coder Recipe](Architect-Coder-Recipe)** — 강한 모델 + 싼 모델 페어링
- **[Stuck Recovery](Stuck-Recovery)** — 5+1 패턴 + 4 recovery 전략
- **[Cost & Budget](Cost-and-Budget)** — token + USD 통제
- **[Self-dogfood Reports](Self-dogfood-Reports)** — 11 run 결과 (qwen3.6-27b)
- **[Anthropic Runbook](Anthropic-Runbook)** — 첫 실 모델 dogfood 절차
- **[Soak Harness](Soak-Harness)** — 24h+ 안정성 테스트
- **[FAQ](FAQ)** — 자주 묻는 질문
- **[Reference Lifts](Reference-Lifts)** — 7 harness에서 lift한 패턴 출처

## 빠른 첫 실행

```bash
# Install
git clone https://github.com/mindungil/GIL.git && cd GIL
make install

# Setup
gil init               # XDG dirs + auth login prompt
gil doctor             # 환경 체크

# Run
SESSION=$(gil new --working-dir ~/myproject | awk '{print $NF}')
gil interview $SESSION
gil run $SESSION --detach
gil watch $SESSION
```

자세한 단계는 [Quickstart](Quickstart) 참조.

## 상태

- v0.1.0-alpha (2026-04-28)
- 17 e2e green / 22 phase plans / ~330 commits
- 11 self-dogfood runs (qwen3.6-27b) — 모든 핵심 가설 EM2EM 검증

## 라이선스

MIT
