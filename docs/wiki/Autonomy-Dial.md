# Autonomy Dial

`spec.risk.autonomy` 4 단계:

| Level | 의미 | 권장 시나리오 |
|---|---|---|
| `FULL` | 모든 도구 무제한 | 격리된 sandbox / dispose-able workspace |
| `ASK_DESTRUCTIVE_ONLY` | rm/mv/chmod/chown/dd/mkfs/sudo 만 ask | **기본 권장 — 일반 dev 환경** |
| `ASK_PER_ACTION` | 모든 도구 ask | TUI 와 함께 (interactive supervision) |
| `PLAN_ONLY` | 실행 차단, plan tool만 | "agent가 어떻게 하려는지" 미리 보기 |

## Phase 22.A — bash chain hardening

Self-dogfood Run 9 에서 발견된 버그 fix됨. 이전엔 `cp X.bak && mv X X.bak` 같은 chain 명령에서 첫 단어 `cp` 만 평가됐음. 지금은 `&&`, `;`, `||`, `|` 으로 split해서 각 sub-command 별 평가.

## Phase 12 — 영속 always_allow / always_deny

TUI permission ask 모달에 6 옵션:
- `[a]` allow once
- `[s]` allow session (이번 run만)
- `[A]` always allow (디스크 저장, project-keyed)
- `[d]` deny once
- `[D]` always deny (디스크 저장)
- `[esc]` cancel = deny once

저장 위치: `$XDG_STATE_HOME/gil/permissions.toml` (mode 0600, project absolute path 별).

## 평가 우선순위

```
1. 영속 always_deny (project-keyed)
2. 영속 always_allow (project-keyed)
3. 세션 in-memory deny
4. 세션 in-memory allow
5. spec.risk.autonomy 규칙 (glob)
6. autonomy 디폴트
```

## 명령어

```bash
gil permissions list             # 등록된 영속 rules
gil permissions remove "rm *" --deny --project /abs/path
gil permissions clear --yes
```
