# gil

자율 코딩 하네스. 자연어로 작업을 묘사하면 agent가 며칠짜리 실행도 사용자 개입 없이 끝까지 수행한다.

## Install

```bash
git clone https://github.com/mindungil/GIL.git && cd GIL
make install     # /usr/local/bin/{gil,gild,giltui,gilmcp}
```

## Run

```bash
gil init         # 1회: XDG 디렉토리 + provider auth
gil              # 채팅 진입 — 자연어로 작업 묘사
```

bare `gil`이 사람용 단일 surface. 슬래시도 verb-mode 명령도 외울 필요 없음 — 자연어로 다 됨. 헤드리스/CI 용 verb 모드(`gil status <id>`, `gil run <id>` 등)는 Wiki 참조.

## Documentation

[GitHub Wiki](https://github.com/mindungil/GIL/wiki):
- [Quickstart](https://github.com/mindungil/GIL/wiki/Quickstart)
- [Provider Setup](https://github.com/mindungil/GIL/wiki/Provider-Setup) — anthropic / openai / openrouter / vllm
- [Architecture](https://github.com/mindungil/GIL/wiki/Architecture)
- [FAQ](https://github.com/mindungil/GIL/wiki/FAQ)

## License

MIT
