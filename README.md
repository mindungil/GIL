# gil

자율 코딩 하네스. 길고 철저한 인터뷰로 모든 요구사항을 추출한 뒤, 며칠이 걸리더라도 다시 묻지 않고 끝까지 작업을 수행하는 CLI 에이전트.

## Install

```bash
git clone https://github.com/mindungil/GIL.git && cd GIL
make install   # → /usr/local/bin/{gil,gild,giltui,gilmcp}
```

## Run

```bash
gil init               # XDG 디렉토리 + auth login
gil doctor             # 환경 진단

# 첫 자율 실행
SESSION=$(gil new --working-dir ~/myproject | awk '{print $NF}')
gil interview $SESSION
gil run $SESSION --detach
gil watch $SESSION
```

## Documentation

전체 사용법, 아키텍처, 명령어, 비용 통제, dogfood 결과 등은 **[GitHub Wiki](https://github.com/mindungil/GIL/wiki)** 참조.

핵심 페이지:
- [Quickstart](https://github.com/mindungil/GIL/wiki/Quickstart) — 5분 안에 첫 자율 실행
- [Commands](https://github.com/mindungil/GIL/wiki/Commands) — 모든 CLI reference
- [Provider Setup](https://github.com/mindungil/GIL/wiki/Provider-Setup) — anthropic / openai / openrouter / vllm
- [Architecture](https://github.com/mindungil/GIL/wiki/Architecture) — 4 binary, gRPC over UDS, 모듈 구성
- [Self-dogfood Reports](https://github.com/mindungil/GIL/wiki/Self-dogfood-Reports) — 11 run (qwen3.6-27b)
- [FAQ](https://github.com/mindungil/GIL/wiki/FAQ)

## License

MIT
