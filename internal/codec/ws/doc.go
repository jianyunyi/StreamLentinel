// Package ws is the WebSocket wire codec (RFC 6455) used by StreamScope.
//
// # Why a hand-written codec
//
// A debugger has to show what is on the wire, including frames that a normal
// WebSocket library would reject or hide: reserved bits set, non-minimal length
// encodings, unmasked client frames, truncated closes. General-purpose
// libraries treat those as errors and tear the connection down. This package
// therefore separates three concerns that libraries usually fuse together:
//
//  1. Syntax  (Decoder, AppendFrame): bytes <-> Frame. It never judges a frame;
//     everything it accepted can be re-encoded to the identical bytes.
//  2. Rules   (Header.Violations): "is this frame legal for this sender?".
//     It returns every violation instead of the first one, so the UI can show
//     all of them, and the caller decides whether to forward, log or abort.
//  3. Messages (Assembler): fragmentation, control-frame interleaving,
//     incremental UTF-8 validation and size limits. This is the strict layer a
//     normal client or server needs, and it reports the close code to send.
//
// The package depends only on the standard library and knows nothing about
// sockets, goroutines, clocks or compression (permessage-deflate is the
// adapter's job: it only has to tell the Assembler that a message is
// compressed, see Options.Rules.AllowRSV1 and Message.Compressed).
package ws
