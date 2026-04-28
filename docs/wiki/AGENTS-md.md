# AGENTS.md

`<workspace>/AGENTS.md` 또는 ancestor 디렉토리의 AGENTS.md / CLAUDE.md / `.cursor/rules/*.mdc` 자동 트리워크 (Phase 12).

agent 매 turn system prompt 에 prepend → 프로젝트별 영구 instructions.

## 발견 순서 (priority low → high; concat-then-render 시 high가 마지막)

1. `$HOME/AGENTS.md` (lowest)
2. `$XDG_CONFIG_HOME/gil/AGENTS.md` (글로벌)
3. ancestors (git-root → workspace), 각 layer:
   - AGENTS.md
   - CLAUDE.md (옵션, `instructions.disable_claude_md = true` 시 skip)
   - `.cursor/rules/*.mdc`
4. workspace 자신 (highest)

## Budget

기본 8KB. 초과 시 lowest-priority sources 먼저 drop.

## 예시 AGENTS.md

```markdown
# Project conventions for gil-driven autonomous work

## Code style
- Go 1.25+
- gofmt + goimports
- Errors via cliutil.UserError pattern at user-facing boundaries
- No global mutable state in core/

## Testing
- _test.go in same package
- Use t.TempDir() for isolation
- Mock external services (no real network in tests)
- httptest.NewServer for HTTP-bound tests

## Commit style
- feat(scope): ...
- fix(scope): ...
- docs(scope): ...
- test(scope): ...
- chore(scope): ...

## Forbidden
- DO NOT add new third-party deps without justification
- DO NOT modify generated proto files manually (use make gen)
- DO NOT skip pre-commit hooks (--no-verify)

## Reference patterns
- Edit DSL: cli/internal/cmd/uistyle/overflow.go (DRY helper)
- Permission: core/permission/from_spec.go (last-wins glob)
- Stuck recovery: core/stuck/recovery.go (4 strategies)
```

## 효과

- 인터뷰 saturation까지 시간 단축 (이미 알고 있는 건 안 묻음)
- agent 가 매 turn 일관된 컨벤션 유지
- 다양한 task 에 동일 스타일 적용

## Self-dogfood 검증

Run 6 (multi-file refactor) 에서 agent 가 AGENTS.md 직접 안 읽었지만 codebase 의 `›` glyph + `p.Dim()` 패턴을 자율 발견 + 모방. AGENTS.md 가 있으면 더 빠르게 같은 일관성 도달.

## Cline `.clinerules` 호환

`.cursor/rules/*.mdc` (Cursor 형식) 도 동일 트리워크에서 인식.
