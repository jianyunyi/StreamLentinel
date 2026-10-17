# ADR-001: Hand-written WebSocket codec

- Status: **Proposed** (implementation exists; the Autobahn gate below is still open)
- Date: 2026-10-17
- Milestone: M0/M1

## Context

StreamScope is a debugger. Its value is showing what is on the wire, including
frames that are *wrong*: reserved bits set, non-minimal length encodings,
unmasked client frames, close payloads of one byte. Mainstream Go WebSocket
libraries are written to protect applications, so they reject such frames,
hide frame boundaries, or close the connection before the caller can look.

## Decision

Write the frame codec in-repo (`internal/codec/ws`), standard library only, and
split it into three layers with different strictness:

1. **Syntax** (`Decoder`, `AppendFrame`) - never judges; byte-exact round trip.
2. **Rules** (`Header.Violations`) - lists every violated rule.
3. **Messages** (`Assembler`) - strict: fragmentation, control frames,
   incremental UTF-8, size limits, close codes.

Compression (permessage-deflate) is the adapter's job; the codec only exposes
`RSV1` handling and `Message.Compressed`.

## Alternatives

| Option | Why not (or when) |
| --- | --- |
| `gorilla/websocket` / `coder/websocket` / `x/net/websocket` | Cannot show invalid frames or exact framing; a proxy would have to re-frame and so would alter traffic. |
| Library for the client path, own codec only for the proxy | This is the **fallback** if the gate below fails. |

## Consequences

- We own correctness, so we must prove it: unit tests built from the RFC 6455
  section 5.7 vectors, property and fuzz tests (round trip, chunking
  independence), and the Autobahn test suite.
- No third-party dependency in the codec layer; enforced by
  `internal/archtest`.

## Gate (open)

Autobahn|Testsuite groups 1 (framing) and 2 (ping/pong) must pass against a
client built on this codec before this ADR becomes **Accepted**. Autobahn runs in
Docker and needs a network, so it was not run in the authoring sandbox; the
session adapter that drives it arrives in M2 (see the milestone plan). Until
then the in-repo vectors and fuzzers are the evidence.
