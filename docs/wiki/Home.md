# gil — autonomous coding harness

길고 철저한 인터뷰로 모든 요구사항을 추출한 뒤, 며칠이 걸리더라도 다시 묻지 않고 끝까지 작업을 수행하는 CLI 에이전트.

## 무엇이 다른가

| | Claude Code | aider | Cline | gil |
|---|---|---|---|---|
| Interview-first | ✗ | ✗ | ✗ | ✓ |
| 며칠 자율 실행 | ✗ | ✗ | 부분 | ✓ |
| 객관 종료 조건 (verifier) | ✗ | ✗ | ✗ | ✓ |
| Cache prefix 며칠 보존 | ✗ | ✗ | ✗ | ✓ |
| Daemon 백그라운드 | ✗ | ✗ | ✗ | ✓ |

핵심 패턴:
- **인터뷰는 길고 철저하게** — saturation까지
- **에이전트가 결정, 시스템은 안전망**
- **단일 stop 조건** — verifier 통과 + stuck 회복 끝 + budget exhausted
- **캐시 보존 압축** — Hermes 패턴

## Wiki 페이지

- **[Getting Started](Getting-Started)** — 설치 + 첫 실행 (5분)
- **[Commands](Commands)** — CLI reference
- **[Configuration](Configuration)** — provider / AGENTS.md / autonomy / cost
- **[Operations](Operations)** — workspace backends + stuck recovery + verifier
- **[Advanced](Advanced)** — architect/coder split + Anthropic runbook + soak + SWE-bench
- **[Architecture](Architecture)** — 설계 개요
- **[FAQ](FAQ)**

## 빠른 첫 실행

```bash
git clone https://github.com/mindungil/GIL.git && cd GIL
make install
gil init
gil           # 채팅 시작 → 자연어로 task 설명
```

## 더 깊은 자료 (repo)

- `docs/design.md` — 전체 설계 narrative
- `docs/dogfood/` — 11 self-dogfood run (qwen3.6-27b)
- `docs/research/` — 7 reference harness 비교 audit
- `docs/plans/` — phase 별 구현 계획

## 상태

- v0.1.0-alpha (2026-04-28)
- 17 e2e green / 24 phase plans / ~340 commits
- 11 self-dogfood runs — 모든 핵심 가설 검증

## 라이선스

MIT
