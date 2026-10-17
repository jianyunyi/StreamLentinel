package ws

import (
	"errors"
	"fmt"
)

// Opcode is the 4-bit frame type (RFC 6455 section 5.2). Unknown values are
// kept as they are so that a debugger can display and re-encode them.
type Opcode uint8

const (
	OpContinuation Opcode = 0x0
	OpText         Opcode = 0x1
	OpBinary       Opcode = 0x2
	OpClose        Opcode = 0x8
	OpPing         Opcode = 0x9
	OpPong         Opcode = 0xA
)

// IsControl reports whether the opcode is a control opcode. Per the RFC the
// control opcodes are exactly those whose most significant bit is set, which
// includes the reserved ones (0xB-0xF).
func (o Opcode) IsControl() bool { return o&0x8 != 0 }

// Known reports whether the opcode is defined by RFC 6455.
func (o Opcode) Known() bool {
	switch o {
	case OpContinuation, OpText, OpBinary, OpClose, OpPing, OpPong:
		return true
	}
	return false
}

func (o Opcode) String() string {
	switch o {
	case OpContinuation:
		return "continuation"
	case OpText:
		return "text"
	case OpBinary:
		return "binary"
	case OpClose:
		return "close"
	case OpPing:
		return "ping"
	case OpPong:
		return "pong"
	}
	return fmt.Sprintf("reserved(0x%X)", uint8(o))
}

// MaxControlPayload is the largest payload a control frame may carry.
const MaxControlPayload = 125

// LenEncoding records which of the three length forms a frame used on the
// wire. The RFC requires the shortest form, but a debugger must be able to
// observe and reproduce frames that did not follow that rule.
type LenEncoding uint8

const (
	// LenAuto is only meaningful when encoding: pick the shortest form.
	LenAuto LenEncoding = iota
	// Len7 stores the length in the 7 bits of the second header byte (0-125).
	Len7
	// Len16 uses marker 126 followed by a 16-bit length.
	Len16
	// Len64 uses marker 127 followed by a 64-bit length (top bit must be 0).
	Len64
)

// Header is the decoded frame header. PayloadLen and LenEncoding describe what
// was on the wire; when encoding, the length always comes from the payload
// passed to AppendFrame and only LenEncoding is consulted.
type Header struct {
	Fin              bool
	RSV1, RSV2, RSV3 bool
	Opcode           Opcode
	Masked           bool
	MaskKey          [4]byte
	PayloadLen       uint64
	LenEncoding      LenEncoding
}

// Size is the number of bytes the header occupies on the wire.
func (h Header) Size() int {
	n := 2
	switch h.LenEncoding {
	case Len16:
		n += 2
	case Len64:
		n += 8
	case Len7:
	default: // LenAuto: shortest form for PayloadLen
		switch {
		case h.PayloadLen <= 125:
		case h.PayloadLen <= 0xFFFF:
			n += 2
		default:
			n += 8
		}
	}
	if h.Masked {
		n += 4
	}
	return n
}

// Frame is one WebSocket frame. Payload is always the unmasked application
// data, even when Header.Masked is set; the mask key is kept in the header.
type Frame struct {
	Header  Header
	Payload []byte
}

// ErrLengthForm is returned by AppendFrame when the requested LenEncoding
// cannot represent the payload length.
var ErrLengthForm = errors.New("ws: payload too long for the requested length encoding")

// AppendFrame appends the wire form of a frame to dst. payload is the unmasked
// data; if h.Masked is set it is masked with h.MaskKey in the output (the
// input slice is not modified).
func AppendFrame(dst []byte, h Header, payload []byte) ([]byte, error) {
	n := uint64(len(payload))
	form := h.LenEncoding
	if form == LenAuto {
		switch {
		case n <= 125:
			form = Len7
		case n <= 0xFFFF:
			form = Len16
		default:
			form = Len64
		}
	}
	if (form == Len7 && n > 125) || (form == Len16 && n > 0xFFFF) {
		return dst, ErrLengthForm
	}

	var b0 byte
	if h.Fin {
		b0 |= 0x80
	}
	if h.RSV1 {
		b0 |= 0x40
	}
	if h.RSV2 {
		b0 |= 0x20
	}
	if h.RSV3 {
		b0 |= 0x10
	}
	b0 |= byte(h.Opcode) & 0x0F

	var b1 byte
	if h.Masked {
		b1 = 0x80
	}
	switch form {
	case Len7:
		dst = append(dst, b0, b1|byte(n))
	case Len16:
		dst = append(dst, b0, b1|126, byte(n>>8), byte(n))
	default:
		dst = append(dst, b0, b1|127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}

	if h.Masked {
		dst = append(dst, h.MaskKey[:]...)
		start := len(dst)
		dst = append(dst, payload...)
		ApplyMask(h.MaskKey, dst[start:])
		return dst, nil
	}
	return append(dst, payload...), nil
}
