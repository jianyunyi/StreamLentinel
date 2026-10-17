# StreamScope

A desktop debugger for **WebSocket** and **Server-Sent Events**: observe frames,
record, replay, inject faults and load-test, with a headless Go core and a thin
Wails shell.

> Status: **M0 + M1 done** (repository foundation and the protocol codec).
> There is no UI and no network adapter yet; see the milestone plan in the
> design document.

## What exists

| Path | What it is |
| --- | --- |
| `internal/codec/ws` | RFC 6455 frame codec: decoder, encoder, masking, rule checker, fragment assembler, incremental UTF-8, close payloads |
| `internal/codec/sse` | WHATWG event-stream parser, independent of how the bytes are chunked |
| `internal/archtest` | Test that enforces the layer dependency direction |
| `docs/adr` | Architecture decision records |
| `docs/event-schema.md` | Event schema v1 (draft) |
| `docs/learn/m1-codec.md` | Study notes: why the codec is written this way |

## Develop

```sh
make          # gofmt check, vet, tests with the race detector
make cover    # codec coverage (gate: 90%)
make fuzz     # every fuzz target, 30 s each (FUZZTIME=5m for longer)
```

Requires Go 1.24+. The codec uses only the standard library.

## License

MIT, see `LICENSE`.
