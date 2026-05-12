# gil

Continuous coding harness. 자연어로 일을 맡기면 agent가 끝까지 진행한다.

## Install

```bash
git clone https://github.com/mindungil/GIL.git && cd GIL
make install     # /usr/local/bin/{gil,gild,giltui,gilmcp}
```

## Run

```bash
gil init         # 1회: XDG 디렉토리 + provider auth
gil              # 채팅 진입
```

bare `gil`이 단일 surface — 자연어만. 헤드리스/CI 용 verb 모드(`gil status <id>`, `gil run <id>` 등)는 Wiki 참조.

## Documentation

[GitHub Wiki](https://github.com/mindungil/GIL/wiki):
- [Quickstart](https://github.com/mindungil/GIL/wiki/Quickstart)
- [Provider Setup](https://github.com/mindungil/GIL/wiki/Provider-Setup) — anthropic / openai / openrouter / vllm
- [Architecture](https://github.com/mindungil/GIL/wiki/Architecture)
- [FAQ](https://github.com/mindungil/GIL/wiki/FAQ)

## License

MIT
