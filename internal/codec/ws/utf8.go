package ws

import "unicode/utf8"

// UTF8Validator validates a UTF-8 stream that arrives in arbitrary pieces.
//
// Text messages may be fragmented at any byte, including in the middle of a
// multi-byte character, so validating each fragment with utf8.Valid would
// reject legal messages. Conversely, waiting for the final fragment would let a
// peer make us buffer megabytes of garbage before noticing; Autobahn's group 6
// requires failing as soon as the invalid byte is seen. The validator keeps at
// most 3 bytes of an unfinished character between calls.
type UTF8Validator struct {
	carry [utf8.UTFMax]byte
	n     int // bytes of an unfinished character held in carry
}

// Reset forgets any carried bytes.
func (v *UTF8Validator) Reset() { v.n = 0 }

// Write feeds the next piece. It returns false as soon as the stream is
// definitely not valid UTF-8; after that the validator must be Reset.
func (v *UTF8Validator) Write(p []byte) bool {
	// 1. Complete a character left over from the previous piece.
	for v.n > 0 && len(p) > 0 {
		v.carry[v.n] = p[0]
		v.n++
		p = p[1:]
		if utf8.FullRune(v.carry[:v.n]) {
			// FullRune is also true for byte sequences that can never become
			// valid (e.g. C0 80), which is what lets us fail fast here.
			r, size := utf8.DecodeRune(v.carry[:v.n])
			if r == utf8.RuneError && size <= 1 {
				return false
			}
			v.n = 0
		}
	}
	if len(p) == 0 {
		return true
	}

	// 2. Hold back an unfinished character at the end of p (at most 3 bytes):
	// look for the last lead byte in the final 3 bytes.
	end := len(p)
	for i := len(p) - 1; i >= 0 && i >= len(p)-(utf8.UTFMax-1); i-- {
		if p[i]&0xC0 == 0x80 { // continuation byte, keep looking backwards
			continue
		}
		if !utf8.FullRune(p[i:]) {
			end = i
		}
		break
	}

	// 3. Everything before the held-back tail must be valid on its own.
	if !utf8.Valid(p[:end]) {
		return false
	}
	v.n = copy(v.carry[:], p[end:])
	return true
}

// Finish reports whether the stream ended on a character boundary. Call it
// after the last piece of a message.
func (v *UTF8Validator) Finish() bool { return v.n == 0 }
