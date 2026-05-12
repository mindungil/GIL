# FAQ

## gil 명령어 너무 많은데?

11+ 명령어 (auth/init/doctor/new/interview/run/events/status/cost/etc). 진짜 핵심은 4개:

1. `gil init` — 첫 셋업 (1회만)
2. `gil` — 세션 list / 다음 step 추천
3. `gil interview <id>` — spec 작성 대화
4. `gil run <id>` — 자율 실행

나머지는 monitoring / management 용. Phase 24 후엔 `gil` no-arg 가 chat 모드로 대체될 예정.

## 다른 harness 와 뭐가 다른가?

| | Claude Code | aider | Cline | gil |
|---|---|---|---|---|
| Interview-first | ✗ | ✗ | ✗ | ✓ |
| 자율 며칠 실행 | ✗ (대화 driven) | ✗ | 부분 | ✓ |
| 명시 stop 조건 (verifier) | ✗ | ✗ | ✗ | ✓ |
| Cache prefix 보존 며칠 | ✗ | ✗ | ✗ | ✓ |
| Daemon 백그라운드 | ✗ | ✗ | ✗ | ✓ |

## qwen 같은 작은 모델로 진짜 작동해?

11 self-dogfood (qwen3.6-27b) 결과:
- Run 5: 첫 verify pass
- Run 6: 첫 budget 안 done (multi-file refactor)
- Run 11: stuck recovery EM2EM

Phase 20 (per-provider verbose tool block) 덕분에 qwen 도 edit / apply_patch format 정확히 사용.

## 비용 폭주가 무서워

```yaml
budget:
  maxIterations: 30
  maxTotalTokens: 500000
  maxTotalCostUSD: 5.00
  reserveTokens: 20000
```

`max_total_cost_usd` 도달 시 즉시 abort. Phase 19.A reserve 덕분에 verifier 마지막에 한 번 실행되어 "어디까지 갔나" 정직 보고.

## 에이전트가 망가뜨리면?

Shadow Git checkpoint 매 iter 자동:
```bash
gil restore <id> <step>
```

워크스페이스 자체가 별도 git 이라 사용자 repo 무영향. 또는 `git worktree add` 으로 격리 권장.

## interview 가 너무 오래 걸려

AGENTS.md / CLAUDE.md 작성하면 인터뷰 시간 50%+ 단축 — 이미 알고 있는 건 안 묻음.

## 몇일짜리 실행 어떻게 켜놓아?

```bash
ssh dev-server
tmux new -d -s gil-task "gil run $SESSION --detach"

# 로컬에서 가끔 들여다보기:
ssh dev-server gil watch $SESSION
```

또는 `giltui` 띄워두면 4-pane mission control.

## 인터뷰가 saturation 못 도달

가끔 weak model (qwen 등) 에서 발생. 해결:
- `gil resume <id>` 으로 재시작
- 또는 spec.yaml 직접 작성 후 `setfrozen` helper로 freeze (`tests/e2e/helpers/setfrozen.go`)
- Architect/Coder split — 인터뷰는 sonnet, run은 haiku

## Stuck 후 어떻게 진행?

```bash
gil events <id> --filter milestones,errors  # 무엇이 stuck 였는지
gil restore <id> <step>                      # 좋았던 시점으로 복원
# spec 재검토 (verifier 너무 까다로운가? task 분할 가능?)
gil interview <id>  # 또는 새 세션으로 재시도
```

## VS Code 안에서 쓸 수 있어?

`vscode/` scaffold 가 있음 (Cline pattern lift). `cd vscode && npm install && npm run package` 으로 .vsix 빌드 가능. Marketplace publish 는 user action (Phase 16).

## SWE-bench 점수는?

`python/gil_swebench/` (Phase 23.C) 통합 어댑터. 실 점수는 사용자 자원 (anthropic key + 수 시간) 필요. 100 task ~ $150-300 with claude-haiku 추정.

## OAuth 다중 사용자?

`gild --auth-issuer <url> --auth-audience <aud>` (Phase 10). UDS bypass default (local-trusted). TCP 만 인증 강제.

## Modal / Daytona 진짜 작동해?

코드는 있음 (Phase 10):
- Modal: CLI shell-out + manifest gen (`runtime/modal/`)
- Daytona: REST API + RemoteExecutor (`runtime/daytona/`)

실 검증은 키 + 계정 필요 (사용자 액션).

## 라이센스?

MIT.

## 기여 방법?

`CONTRIBUTING.md` 참조. gitflow (main / develop / feature/* / release/* / hotfix/*).
