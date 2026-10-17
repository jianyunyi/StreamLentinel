package ws

import (
	"encoding/binary"
	"unicode/utf8"
)

// Standard close status codes (RFC 6455 section 7.4.1) that endpoints
// commonly generate.
const (
	CloseNormal          uint16 = 1000
	CloseGoingAway       uint16 = 1001
	CloseProtocolError   uint16 = 1002
	CloseUnsupportedData uint16 = 1003
	CloseNoStatus        uint16 = 1005 // never sent on the wire
	CloseAbnormal        uint16 = 1006 // never sent on the wire
	CloseInvalidData     uint16 = 1007
	ClosePolicyViolation uint16 = 1008
	CloseTooBig          uint16 = 1009
	CloseInternalError   uint16 = 1011
)

// Close is the parsed payload of a Close frame.
type Close struct {
	// HasCode is false for an empty payload; the status is then "no status
	// received" (1005), a value that is only ever synthesised locally.
	HasCode bool
	Code    uint16
	Reason  string
}

// ValidCloseCode reports whether code may appear in a Close frame on the wire.
// 1004, 1005, 1006 and 1015 are reserved for local use; 1016-2999 are reserved
// for future protocol use; 3000-4999 are registered or private.
func ValidCloseCode(code uint16) bool {
	switch {
	case code >= 1000 && code <= 1003:
		return true
	case code >= 1007 && code <= 1014:
		return true
	case code >= 3000 && code <= 4999:
		return true
	}
	return false
}

// ParseClose decodes and validates a Close frame payload: it must be empty or
// at least two bytes (a status code), the code must be valid on the wire, and
// the optional reason must be UTF-8.
func ParseClose(p []byte) (Close, error) {
	switch {
	case len(p) == 0:
		return Close{}, nil
	case len(p) == 1:
		return Close{}, &ProtocolError{ViolationBadClosePayload, CloseProtocolError}
	}
	c := Close{HasCode: true, Code: binary.BigEndian.Uint16(p)}
	if !ValidCloseCode(c.Code) {
		return Close{}, &ProtocolError{ViolationBadClosePayload, CloseProtocolError}
	}
	if !utf8.Valid(p[2:]) {
		return Close{}, &ProtocolError{ViolationInvalidUTF8, CloseInvalidData}
	}
	c.Reason = string(p[2:])
	return c, nil
}

// AppendClosePayload appends a Close frame payload. The reason is truncated at
// a character boundary so the whole payload fits the 125-byte control limit.
func AppendClosePayload(dst []byte, code uint16, reason string) []byte {
	dst = binary.BigEndian.AppendUint16(dst, code)
	const room = MaxControlPayload - 2
	if len(reason) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut]
	}
	return append(dst, reason...)
}
