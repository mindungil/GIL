# Phase 13 — Distribution + project stance

> Phase 11/12에서 fresh-install + in-session UX 완성. Phase 13은 **배포 채널 결정**과 **프로젝트의 명시적 입장 문서화** (텔레메트리, 보안, 비공개 데이터 처리).

**Goal**: `gil update` 가능한 배포 경로 결정 + 첫 release tag 후보 + 명시적 stance 문서.

**Skip**: 실제 marketplace/store/registry 등록 (외부 계정 필요).

---

## Track A — 배포 채널 결정 + gil update

### T1: 배포 채널 결정 문서

**Files**: `docs/distribution.md`

3 채널 후보 비교:
- GitHub releases + curl-installer (Phase 10 GoReleaser가 이미 만듦)
- Homebrew tap (`mindungil/homebrew-tap` repo 필요 — placeholder formula GoReleaser가 만듦)
- `go install github.com/mindungil/gil/cli/cmd/gil@latest` (모듈 path 정리 필요)

각각의 trade-off + 권장 (curl-installer + homebrew). Marketplace는 phase-by-phase.

### T2: install.sh

**Files**: `scripts/install.sh`

```bash
curl -fsSL https://gil.example/install | bash
```

내용: latest GitHub release tag detect → tarball 다운 → `/usr/local/bin/{gil,gild,giltui,gilmcp}` 설치 (필요 시 sudo). PATH 안내.

### T3: gil update

**Files**: `cli/cmd/gil/update.go`

설치 시 marker (`/usr/local/bin/.gil-installer-method`)로 어떻게 설치됐는지 기록. `gil update`은 marker 읽고 적절한 명령 호출 (`brew upgrade gil` 또는 install.sh 재실행).

Commit (3 commits 통합 가능): `feat(distribution): install.sh + gil update + distribution.md`

---

## Track B — 명시적 stance 문서

### T4: SECURITY.md

**Files**: `SECURITY.md`

내용:
- 보고 채널 (이메일 또는 GitHub Security Advisory)
- 지원하는 버전 (latest only — 초기 단계)
- 주요 보안 모델 (sandbox 기본 ON 권장, autonomy 다이얼 의의, credstore 0600)
- 알려진 한계 (LOCAL_NATIVE 사용 시 워크스페이스 범위 외 보호 없음)

### T5: PRIVACY.md

**Files**: `PRIVACY.md`

내용:
- gil은 텔레메트리를 보내지 않음 (anonymous/usage 모두)
- 데이터는 모두 로컬 ($XDG_*/gil/)
- LLM provider에게 보내지는 데이터는 spec.system + 사용자 메시지 + tool 결과 — provider의 PRIVACY 정책 따름
- ANTHROPIC_API_KEY/etc는 credstore에 저장, 외부 전송 없음
- Prometheus metrics는 opt-in (`--metrics :PORT`), 로컬 endpoint만

### T6: CODE_OF_CONDUCT.md + CONTRIBUTING.md

표준 OSS 보일러플레이트 (Contributor Covenant 2.1 + GitHub flow).

Commit: `docs: SECURITY + PRIVACY + CODE_OF_CONDUCT + CONTRIBUTING`

---

## Track C — 첫 release tag

### T7: v0.1.0-alpha 준비

**Files**: `CHANGELOG.md`, version constants

- `core/version/version.go`: build-time embedded (LDFLAGS)
- `CHANGELOG.md`: Keep a Changelog 형식, Phase 1-13 요약
- `gil --version` / `gild --version` 동작 확인

Tag 생성은 user-driven (release 이메일 + GitHub release notes 작성).

Commit: `chore(release): v0.1.0-alpha CHANGELOG + version constants`

### T8: progress + README

`docs/progress.md` Phase 13 row, README banner를 "Phase 13 완료 — first release ready"로.

Commit: `docs: Phase 13 — distribution + stance`

---

## Phase 13 완료 체크리스트

- [ ] `docs/distribution.md` 채널 비교 + 권장
- [ ] `scripts/install.sh` curl-pipe-able
- [ ] `gil update` 설치 방법 sniff + 호출
- [ ] SECURITY.md / PRIVACY.md / CODE_OF_CONDUCT.md / CONTRIBUTING.md
- [ ] CHANGELOG + version constants + `gil --version`
- [ ] release tag는 user-driven (코드 + 문서 준비만)

## Phase 13 이후 (외부 활동)

- 실제 GitHub release tag (v0.1.0-alpha) 생성
- Homebrew tap repo 생성 + first formula publish
- VS Code Marketplace 게시
- 실제 OAuth 공급자 (Google/Auth0) 통합 검증
- Atropos training run via OpenRouter
- 실제 Anthropic dogfood + 결과 docs/dogfood/ 추가
