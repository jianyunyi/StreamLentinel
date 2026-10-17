package ws

import "encoding/binary"

// ApplyMask XORs b with the 4-byte masking key in place (RFC 6455 section 5.3).
//
// Masking is not encryption: its purpose is to stop a malicious page from
// crafting bytes that a transparent HTTP proxy would mistake for a request
// (cache poisoning). Because XOR is its own inverse, the same function masks
// and unmasks.
func ApplyMask(key [4]byte, b []byte) { MaskAt(key, 0, b) }

// MaskAt is ApplyMask for a chunk that starts offset bytes into the payload.
// The key position is payload_index mod 4, so a streaming caller that sees the
// payload in pieces must tell us where each piece starts.
func MaskAt(key [4]byte, offset uint64, b []byte) {
	// Rotate the key so that k[0] lines up with b[0].
	var k [4]byte
	for i := range k {
		k[i] = key[(offset+uint64(i))&3]
	}

	// Fast path: 8 bytes per iteration. The 8-byte stride is a multiple of 4,
	// so the key phase is the same at the start of every word.
	w := binary.LittleEndian.Uint64([]byte{k[0], k[1], k[2], k[3], k[0], k[1], k[2], k[3]})
	i := 0
	for ; i+8 <= len(b); i += 8 {
		binary.LittleEndian.PutUint64(b[i:], binary.LittleEndian.Uint64(b[i:])^w)
	}
	for ; i < len(b); i++ {
		b[i] ^= k[i&3]
	}
}
