package ws

import "fmt"

// Violation is a stable, machine-readable reason why a frame or message does
// not conform to RFC 6455. The String form is part of the event schema, so do
// not rename existing values.
type Violation uint8

const (
	// Header-level violations (see Header.Violations).
	ViolationReservedBits Violation = iota + 1
	ViolationUnknownOpcode
	ViolationControlFragmented
	ViolationControlTooLong
	ViolationMaskMissing
	ViolationMaskUnexpected
	ViolationNonMinimalLength

	// Sequence- and content-level violations (reported by Assembler).
	ViolationUnexpectedContinuation
	ViolationInterleavedData
	ViolationInvalidUTF8
	ViolationMessageTooBig
	ViolationBadClosePayload
)

var violationNames = [...]string{
	ViolationReservedBits:           "reserved_bits",
	ViolationUnknownOpcode:          "unknown_opcode",
	ViolationControlFragmented:      "control_fragmented",
	ViolationControlTooLong:         "control_too_long",
	ViolationMaskMissing:            "mask_missing",
	ViolationMaskUnexpected:         "mask_unexpected",
	ViolationNonMinimalLength:       "non_minimal_length",
	ViolationUnexpectedContinuation: "unexpected_continuation",
	ViolationInterleavedData:        "interleaved_data",
	ViolationInvalidUTF8:            "invalid_utf8",
	ViolationMessageTooBig:          "message_too_big",
	ViolationBadClosePayload:        "bad_close_payload",
}

func (v Violation) String() string {
	if int(v) < len(violationNames) && violationNames[v] != "" {
		return violationNames[v]
	}
	return fmt.Sprintf("violation(%d)", uint8(v))
}

// Role says who sent a frame. RFC 6455 section 5.1 makes masking depend on it:
// every client-to-server frame MUST be masked and no server-to-client frame
// may be.
type Role uint8

const (
	RoleClient Role = iota + 1 // the frame was sent by a client
	RoleServer                 // the frame was sent by a server
)

// Rules are the parameters needed to judge a header.
type Rules struct {
	// Sender is who sent the frames being judged.
	Sender Role
	// AllowRSV1 is true once an extension that uses RSV1 (permessage-deflate,
	// RFC 7692) was negotiated. RSV1 is then legal only on the first frame of
	// a data message.
	AllowRSV1 bool
}

// Violations returns every header-level rule the frame breaks, in a stable
// order. An empty result means the header is legal under the given rules.
func (h Header) Violations(r Rules) []Violation {
	var out []Violation

	if h.RSV2 || h.RSV3 {
		out = append(out, ViolationReservedBits)
	} else if h.RSV1 && !(r.AllowRSV1 && (h.Opcode == OpText || h.Opcode == OpBinary)) {
		out = append(out, ViolationReservedBits)
	}
	if !h.Opcode.Known() {
		out = append(out, ViolationUnknownOpcode)
	}
	if h.Opcode.IsControl() {
		if !h.Fin {
			out = append(out, ViolationControlFragmented)
		}
		if h.PayloadLen > MaxControlPayload {
			out = append(out, ViolationControlTooLong)
		}
	}
	switch {
	case r.Sender == RoleClient && !h.Masked:
		out = append(out, ViolationMaskMissing)
	case r.Sender == RoleServer && h.Masked:
		out = append(out, ViolationMaskUnexpected)
	}
	if (h.LenEncoding == Len16 && h.PayloadLen <= 125) ||
		(h.LenEncoding == Len64 && h.PayloadLen <= 0xFFFF) {
		out = append(out, ViolationNonMinimalLength)
	}
	return out
}

// ProtocolError is a violation that makes a strict endpoint fail the
// connection. CloseCode is the status the endpoint should send in its Close
// frame (1002 protocol error, 1007 invalid data, 1009 message too big).
type ProtocolError struct {
	Violation Violation
	CloseCode uint16
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("ws: protocol error: %s (close %d)", e.Violation, e.CloseCode)
}
