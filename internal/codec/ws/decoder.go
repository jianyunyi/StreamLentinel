package ws

import (
	"bytes"
	"errors"
	"io"
)

// DefaultMaxPayload is the per-frame payload limit used when none is given.
const DefaultMaxPayload = 16 << 20

var (
	// ErrInvalidLength means a 64-bit length had its most significant bit set,
	// which RFC 6455 forbids. The stream cannot be resynchronised after this.
	ErrInvalidLength = errors.New("ws: 64-bit payload length has the most significant bit set")

	// ErrFrameTooLarge means the announced payload exceeds the decoder limit.
	// The returned Frame carries the header so callers can still report it.
	ErrFrameTooLarge = errors.New("ws: frame payload exceeds the configured limit")
)

// Decoder reads frames from a byte stream. It is pure syntax: it does not check
// reserved bits, opcodes, masking policy or fragmentation (see Header.Violations
// and Assembler). Every frame it returns can be encoded back to the same bytes.
type Decoder struct {
	r   io.Reader
	max uint64
}

// NewDecoder returns a Decoder with the given per-frame payload limit
// (0 selects DefaultMaxPayload).
func NewDecoder(r io.Reader, maxPayload uint64) *Decoder {
	if maxPayload == 0 {
		maxPayload = DefaultMaxPayload
	}
	return &Decoder{r: r, max: maxPayload}
}

// Next reads one frame. It returns io.EOF only when the stream ended exactly
// on a frame boundary; a stream that ends inside a frame yields
// io.ErrUnexpectedEOF, so the two cases (clean end vs. truncation) stay
// distinguishable for the UI.
func (d *Decoder) Next() (Frame, error) {
	var fixed [2]byte
	if _, err := io.ReadFull(d.r, fixed[:]); err != nil {
		return Frame{}, err // io.EOF if nothing was read, io.ErrUnexpectedEOF if one byte
	}

	h := Header{
		Fin:    fixed[0]&0x80 != 0,
		RSV1:   fixed[0]&0x40 != 0,
		RSV2:   fixed[0]&0x20 != 0,
		RSV3:   fixed[0]&0x10 != 0,
		Opcode: Opcode(fixed[0] & 0x0F),
		Masked: fixed[1]&0x80 != 0,
	}

	switch l := fixed[1] & 0x7F; l {
	case 126:
		var ext [2]byte
		if err := d.fill(ext[:]); err != nil {
			return Frame{}, err
		}
		h.PayloadLen = uint64(ext[0])<<8 | uint64(ext[1])
		h.LenEncoding = Len16
	case 127:
		var ext [8]byte
		if err := d.fill(ext[:]); err != nil {
			return Frame{}, err
		}
		for _, c := range ext {
			h.PayloadLen = h.PayloadLen<<8 | uint64(c)
		}
		h.LenEncoding = Len64
		if h.PayloadLen>>63 != 0 {
			return Frame{Header: h}, ErrInvalidLength
		}
	default:
		h.PayloadLen = uint64(l)
		h.LenEncoding = Len7
	}

	// Check the limit before reading or allocating anything: the length field
	// is attacker-controlled.
	if h.PayloadLen > d.max {
		return Frame{Header: h}, ErrFrameTooLarge
	}

	if h.Masked {
		if err := d.fill(h.MaskKey[:]); err != nil {
			return Frame{}, err
		}
	}

	payload, err := d.readPayload(h.PayloadLen)
	if err != nil {
		return Frame{}, err
	}
	if h.Masked {
		ApplyMask(h.MaskKey, payload)
	}
	return Frame{Header: h, Payload: payload}, nil
}

// fill reads exactly len(p) bytes; running out of input here is always a
// truncation because we are already inside a frame.
func (d *Decoder) fill(p []byte) error {
	_, err := io.ReadFull(d.r, p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// readPayload reads n bytes. Small payloads are allocated up front. Large ones
// are read through a growing buffer so memory use follows the bytes that
// actually arrive: a peer that announces 16 MiB and then sends 10 bytes must
// not make us commit 16 MiB.
func (d *Decoder) readPayload(n uint64) ([]byte, error) {
	if n == 0 {
		return nil, nil
	}
	const eager = 64 << 10
	if n <= eager {
		buf := make([]byte, n)
		if err := d.fill(buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	var b bytes.Buffer
	if _, err := io.CopyN(&b, d.r, int64(n)); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b.Bytes(), nil
}
