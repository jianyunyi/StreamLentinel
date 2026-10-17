# Event schema v1 (draft)

Every observable thing in a session becomes one **event**. The same shape is
used by the bus, the store, the `.ssc` capture file (one JSON object per line)
and the HTTP API. Anything that depends on wire detail lives in `attrs`; the
envelope never changes.

## Envelope

| Field | Type | Meaning |
| --- | --- | --- |
| `seq` | integer | Per-session, starts at 1, gap-free, assigned by the single writer. Total order of the session. |
| `session_id` | string | Session identifier. |
| `ts_wall` | string (RFC 3339, ns) | Wall clock time; for humans and export. |
| `ts_mono` | integer (ns) | Monotonic offset from session start; for latency and replay timing. Never goes backwards. |
| `dir` | `"out"` \| `"in"` \| `"local"` | Sent by us, received by us, or produced locally (state change, marker). |
| `kind` | string | One of the kinds below. |
| `payload` | string | Content. UTF-8 text as is; binary as base64 (see `payload_kind`). |
| `payload_kind` | `"text"` \| `"base64"` \| `"none"` | How to read `payload`. |
| `size` | integer | Original payload size in bytes. |
| `truncated` | boolean | `payload` was cut to the capture limit; `size` still shows the real size. |
| `attrs` | object | Kind-specific details (below). |

## Kinds

| Kind | Produced when | Notable `attrs` |
| --- | --- | --- |
| `session.state` | State machine transition | `from`, `to`, `reason` |
| `handshake.request` / `handshake.response` | HTTP upgrade or SSE request/response | `method`, `url`, `status`, `headers` |
| `ws.message` | A complete WebSocket message | `opcode` (`text`/`binary`), `fragments`, `compressed` |
| `ws.frame` | A single frame (when frame view is enabled) | `opcode`, `fin`, `rsv`, `masked`, `len_encoding`, `violations` |
| `ws.ping` / `ws.pong` | Control frames | - |
| `ws.close` | Close frame or abnormal end | `code`, `reason`, `has_code` |
| `sse.event` | Dispatched SSE event | `type`, `id` |
| `sse.comment` | A `:` line | - |
| `sse.retry` | Valid `retry:` field | `ms` |
| `sse.ignored` | Line a client must ignore | `field`, `reason` |
| `sse.discarded` | Stream ended mid-event | `reason` |
| `net.error` | I/O error | `op`, `error` |
| `reconnect.scheduled` / `reconnect.attempt` | Reconnect policy | `attempt`, `delay_ms`, `last_event_id` |
| `fault.applied` | Fault injection took effect | `fault`, `params` |
| `marker` | User-placed bookmark | `label` |

### Stable string vocabularies

These come straight from the codec and are part of the schema; never rename.

- `ws.frame.attrs.violations[]` and `ws.close` problems - `reserved_bits`,
  `unknown_opcode`, `control_fragmented`, `control_too_long`, `mask_missing`,
  `mask_unexpected`, `non_minimal_length`, `unexpected_continuation`,
  `interleaved_data`, `invalid_utf8`, `message_too_big`, `bad_close_payload`.
- `ws.frame.attrs.len_encoding` - `7`, `16`, `64`.
- `sse.ignored.attrs.reason` - `unknown_field`, `id_contains_nul`,
  `retry_not_numeric`.
- `sse.discarded.attrs.reason` - `incomplete_event`, `incomplete_line`.

## Examples

A frame that breaks two rules, still recorded faithfully:

```json
{"seq":12,"session_id":"s-01","ts_wall":"2026-10-17T07:04:11.120345678Z","ts_mono":4120345678,
 "dir":"in","kind":"ws.frame","payload":"hi","payload_kind":"text","size":2,"truncated":false,
 "attrs":{"opcode":"text","fin":true,"rsv":[true,false,false],"masked":true,
          "len_encoding":16,"violations":["reserved_bits","mask_unexpected","non_minimal_length"]}}
```

An SSE comment and an event:

```json
{"seq":3,"session_id":"s-02","ts_wall":"2026-10-17T07:05:00Z","ts_mono":900000000,"dir":"in",
 "kind":"sse.comment","payload":" keep-alive","payload_kind":"text","size":11,"truncated":false,"attrs":{}}
{"seq":4,"session_id":"s-02","ts_wall":"2026-10-17T07:05:01Z","ts_mono":1900000000,"dir":"in",
 "kind":"sse.event","payload":"{\"n\":1}","payload_kind":"text","size":7,"truncated":false,
 "attrs":{"type":"tick","id":"41"}}
```

## Compatibility rules

- Adding a kind or an `attrs` key is compatible; consumers must ignore unknown ones.
- Removing or renaming an envelope field, a kind, or a vocabulary value is a
  breaking change and requires a new schema version in the `.ssc` header.
