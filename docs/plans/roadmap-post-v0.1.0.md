# Post v0.1.0-alpha — roadmap (Phase 14 ~ 17)

> v0.1.0-alpha는 Phase 1-13까지 닫은 상태. 14 e2e green, 4 binary, fresh-install + in-session UX + distribution path 완비. 그 다음에 무엇을 할지 한눈 정리.

## 현 상태 점검 (2026-04-27)

- ✓ Build green (`make build` → 4 binaries)
- ✓ Tests green (`make test` 모든 모듈)
- ✓ E2E 14 phase green (`make e2e-all` ~3분)
- ✓ Local smoke OK: `gil --version`, `gil init`, `gil doctor` 모두 작동
- ✓ origin (mindungil/GIL) 에 main + develop + v0.1.0-alpha tag push 완료
- ⚠ 실제 LLM (Anthropic) 으로의 dogfood는 미실행 — mock provider만 검증됨
- ⚠ 209개 기존 commits author는 Test User (rewrite 제안 보류 중)

## Phase 14 — Monitoring UX (TUI/CLI 재구성)

**Why**: 현재 TUI/CLI는 event-stream-centric. 며칠짜리 자율 실행을 "켜놓고 떠나서 가끔 들여다보는" 시나리오에 최적화 안 됨. 

**Tracks** (`docs/plans/phase-14-monitoring-ux.md` 상세):
- A. TUI 4-pane (Sessions / Spec+Progress / Activity / Memory) — progress bar + verify matrix + cost meter + autonomy indicator
- B. Stuck/Recovery 시각화
- C. Checkpoint 타임라인 modal
- D. `gil watch <id>` live monitor (TUI 없는 곳)
- E. `gil events --filter milestones|errors|tools`
- F. `gil status` 시각 모드 (RUNNING 진행률 막대)
- G. `gil` no-arg quick summary

**완료 시나리오**: 사용자가 `ssh server && gil` 한 줄로 진행률 글랜스, `gil watch abc123` 으로 떠나기 전 마지막 확인.

## Phase 15 — Real LLM hardening

**Why**: 14 e2e는 모두 mock provider. 실제 Anthropic/OpenAI/OpenRouter 호출에서 발생할 수 있는 이슈 (rate limit, timeout, 캐시 미스, 비용 폭주, malformed JSON, retry 실패) 검증 안 됨.

**Tracks**:
- A. Provider integration tests with **VCR cassettes** (record real API once, replay forever) — 첫 record는 사용자 키 필요
- B. Long-run stress: 1-hour real run with `claude-haiku-4-5` (cheap model) — verify 캐시 prefix 보존, 메모리 누수 없음
- C. Multi-provider parity: `gpt-4o-mini`, `anthropic/claude-haiku` (via OpenRouter), `meta-llama/llama-3.3` (via vLLM)
- D. Failure injection: 503 / rate limit / timeout / partial response — Retry wrapper가 정확히 동작?
- E. Cost catalog auto-update via GitHub Action (월 1회 fetch)
- F. Token budget warnings: spec.run.budget 도달 시 사용자에게 알림 (TUI 통해)

**검증**: `make test-real-providers` 새 target — env var 있을 때만 실행 (CI는 skip).

## Phase 16 — Ecosystem activation

**Why**: 코드/문서는 다 있지만 외부 활성화는 0. 첫 user는 마찰 큼.

**Tracks**:
- A. **GitHub Release**: `git push origin v0.1.0-alpha` → GoReleaser workflow 트리거 → 16 binary + deb + rpm + brew formula publish
- B. **Homebrew tap**: `mindungil/homebrew-tap` repo 생성, 첫 `Formula/gil.rb` push (GoReleaser가 자동 PR)
- C. **VS Code Marketplace**: `vscode/` scaffold publish — publisher 등록 + `vsce publish`
- D. **demo dogfood**: 실제 ANTHROPIC_API_KEY로 작은 프로젝트 한 개 자율 실행 → `docs/dogfood/2026-XX-first-real-run.md` 작성
- E. **landing page** (옵션): GitHub Pages으로 `gil.dev` (또는 도메인) — README 기반 정적 사이트
- F. **demo video**: asciinema or screencast — interview → run → verify pass 흐름

대부분 사용자 액션 (계정/도메인 필요). 이 phase의 plan은 "절차 문서"가 됨.

## Phase 17 — 잔여 기술 부채

낮은 우선순위, 시간 날 때 정리:

- **History rewrite**: 209 commits author `Test User → mindungil` (filter-repo + force push). 하면 contribution graph 정확. 안 하면 v0.1.0-alpha 시점 commits만 그대로.
- **gil doctor dev mode**: `gild` PATH 검색 외에 `./bin/gild` 도 fallback 으로 봐주기 (or `gil --version` 빌드 시점에 path 기록)
- **gild --base 완전 제거**: deprecation 경고 → vNext에 제거 (현재는 alias 유지)
- **slash command stub들 실 RPC**: `/compact` (RunService.RequestCompact), `/model` (next-turn hint event), `/diff` (ShadowGit.Diff RPC)
- **gil permissions list/remove** subcommand (Phase 12 T10f 보류)
- **gil session list/rm** (Phase 12 T16에서 audit) — 별도 subcommand 필요한지 결정
- **proto Adopt RPC**: `gil import` 로 들어온 세션이 SessionService.List 에 보이게
- **MCP OAuth 실제 구현**: `gil mcp login <name>` 현재 stub
- **테스트 시점**: `Test User` author 이메일이 e2e fixtures + commit messages 일부에 박혀 있을 수 있음 — grep 후 정리
- **(완료, Phase 17)** GoReleaser brews owner / install.sh / README / CONTRIBUTING / SECURITY / PRIVACY 의 placeholder 들 모두 `mindungil/GIL` + `alswnsrlf12@naver.com` 으로 swap.

## 우선순위 의견

1. **Phase 14 (UX)** — 코드 베이스가 안정된 지금이 사용자 진입 마찰을 줄일 적기. 이게 없으면 dogfood 자체가 힘듦.
2. **Phase 17 부분 (repo 이름/owner placeholder swap)** — 1시간 정도면 끝. v0.1.0-alpha 가 아직 push 직후라서 사용자 혼란 적을 때 정리.
3. **Phase 16 (ecosystem)** — 사용자 결심 + 일부 액션 필요.
4. **Phase 15 (real LLM hardening)** — Phase 16 dogfood에서 실제 문제 만나면 그 시점에. 지금부터 가설 기반으로 만들면 over-engineering.
