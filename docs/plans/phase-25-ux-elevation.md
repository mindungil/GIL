# Phase 25 — UX Elevation (general-user)

> 사용자 피드백: "그 하네스가 보통 '나만을 위한' 것이 아니잖아? UX라는것을 고도화해야 한다."
> 진단: gil은 dev(나)의 환경/지식 가정이 박혀 있어 일반 사용자 첫 경험이 거침. 이번 phase는 그걸 체계적으로 고친다.

## UX 갭 audit (실제 사용자 첫 30분 시나리오 기반)

### 발견된 거친 표면

| # | 갭 | 사용자 영향 | tier |
|---|---|---|---|
| 1 | `gil auth login` 한 줄 prompt — provider/model picker 없음 | 매번 외워야 함, 모델 이름 기억 안 나면 막힘 | **S** |
| 2 | `vllm default model = qwen3.6-27b` (dev hardcoded) | 다른 endpoint 사용자는 무조건 막힘 | **S** |
| 3 | 첫 실행 `gil`에서 onboarding journey 없음 (`gil init` 따로 알아야) | 첫 사용자가 뭐부터 해야 할지 모름 | **S** |
| 4 | Chat 중 LLM 응답 대기 시 "thinking" 표시 없음 | freeze 처럼 보임, 사용자가 ctrl-C 누름 | **S** |
| 5 | `gil status` 가 abandoned (CREATED + 0 events + old) 세션도 표시 | 100개 cluttering — Phase 12에서 chat 화면만 부분 fix됨 | **S** |
| 6 | 에러 메시지에 다음 액션 제안 부족 | "그래서 뭐해?" — 사용자가 docs/wiki 봐야 함 | **A** |
| 7 | `/help` 가 명령어 dump | 단계별 (시작/진행 중/모니터링) 그룹핑 없음 | **A** |
| 8 | 세션 ID 외워야 함 (`01kq8v`) | 사람 친화 이름 없음 | **A** |
| 9 | 시간 표시가 RFC3339 raw (`2026-04-28T01:08:42`) | "2분 전" 같은 relative 없음 | **A** |
| 10 | LLM 응답 한 번에 print (스트리밍 X) | 긴 응답 체감 latency ↑ | **B** |
| 11 | dim 색 과사용 — 강조점 흐림 | 정보 위계 흐려짐 | **B** |
| 12 | TUI(giltui)와 chat 분리 — 한 surface에서 못 봄 | 작업 흐름이 끊김 | **B** |

## 우선순위 + 실행 순서

### Tier S — 시급 (이번 phase 내 모두 처리)

**S1 — Provider Wizard (P25 — 진행 중)** — `gil auth login` 다단계 wizard. provider 선택 → key → model picker → test connection. credstore에 model 필드 추가.

**S2 — "Qwen 하드코드" 제거** — `intentModelFor("vllm")`이 credstore.Model 읽음. 없으면 wizard 유도.

**S3 — Onboarding Journey** — 첫 `gil` 실행 시:
```
─ no init done       → "Welcome. Run `gil init` (1-time setup)"
─ init done, no creds → "Ready for credentials. Run `gil auth login`"
─ creds, no sessions  → "Standing by. Describe your first mission."
─ creds + sessions    → 기존 banner + chat
```
4 가지 상태 모두 명확한 다음 단계 제시. 새 사용자가 어느 단계에 있는지 즉시 파악.

**S4 — LLM Thinking Indicator** — chat에서 LLM Complete() 호출 동안 Braille spinner + "thinking..." 메시지 표시. ctrl-C 시 graceful cancel.

**S5 — Abandoned Session Hide** — `gil status` (visual + JSON)에서도 default로 abandoned hide. `--all` flag으로 전체 보기.

### Tier A — 마찰 줄이기

**A1 — Error → Action** — 모든 user-facing error에 `Hint:` 다음 액션 명령. 이미 일부 있음, 매트릭스 audit + 누락 채우기.

**A2 — `/help` Stage Grouping** — 현재 dump → 3단계 그룹:
```
Just starting     → gil auth login / gil init / gil chat
Currently working → /status / /cost / /diff
Recovery          → /quit / gil restore <id> <step>
```

**A3 — Human-friendly Session Names** — auto-generate slug from goal (`add-dark-mode-2026-04-28`) + ID 옆에 표시. Fuzzy resume by name fragment.

**A4 — Relative Time** — `gil status` / `gil cost` / events에 "2m ago" / "started 18:01 (2h 36m)". 옵션: `--absolute` 으로 raw timestamp.

### Tier B — 폴리시 (이번 phase 미만, 별도)

B1 (스트리밍), B2 (color audit), B3 (TUI 통합) — Phase 26+ 검토.

## 실행 plan

| Step | 트랙 | 상태 |
|---|---|---|
| 1 | S1 Provider Wizard | 🔄 in flight (agent dispatched) |
| 2 | S2 — qwen 하드코드 제거 | S1과 같이 처리 |
| 3 | S3 Onboarding Journey | 다음 dispatch |
| 4 | S4 Thinking Indicator | 다음 dispatch |
| 5 | S5 Abandoned Session Hide | small fix, 인-프로세스 |
| 6 | A1-A4 (Tier A 묶음) | S 끝난 후 |
| 7 | docs/wiki 갱신 + push | 마무리 |

## 검증 시나리오 (실 사용자 시뮬)

각 step 끝나고 다음 시나리오 통과:

1. **Fresh box, gil 처음 입력**: → onboarding journey 시작 (S3)
2. **`gil init` 후 auth**: → wizard provider picker → vllm 선택 → base URL → model picker (S1)
3. **그 후 `gil`**: → chat banner + "Standing by"
4. **인사 입력**: → LLM이 자연스러운 응답 (P24-redesign 완료)
5. **task 입력**: → LLM 응답 중 spinner 표시 (S4) → manifest preview → confirm
6. **인터뷰 saturation 까지**: → 매 turn spinner (S4)
7. **새 세션 N회 후 `gil status`**: → 활성/완료만 보임 (S5)

## 비목표

- 풀-스크린 TUI 재작성 (giltui 이미 있음, Phase 26)
- VS Code 확장 UX (vscode/ scaffold 별도)
- 다국어 i18n (현재 한국어/영어 혼용 OK, 사용자 본인 환경)

## 누적 phase 산출 (현재까지)

- **Phase 1-13**: 코어 엔진 + 첫 release prep
- **Phase 14-20**: 자율 실행 인프라 + dogfood loop
- **Phase 21-22**: stuck recovery + bash chain hardening
- **Phase 23**: external-readiness (anthropic runbook, soak, swebench, user guide)
- **Phase 24**: chat surface 통합 (LLM-driven)
- **Phase 25 (이 phase)**: general-user UX elevation
