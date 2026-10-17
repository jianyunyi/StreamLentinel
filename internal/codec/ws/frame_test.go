package ws

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The examples of RFC 6455 section 5.7. Using the specification's own bytes as
// test vectors is the cheapest way to prove we read the same spec as everyone.
func TestRFC6455Examples(t *testing.T) {
	tests := []struct {
		name    string
		wire    string
		fin     bool
		op      Opcode
		masked  bool
		payload string
	}{
		{"unmasked text Hello", "81 05 48 65 6c 6c 6f", true, OpText, false, "Hello"},
		{"masked text Hello", "81 85 37 fa 21 3d 7f 9f 4d 51 58", true, OpText, true, "Hello"},
		{"fragment 1", "01 03 48 65 6c", false, OpText, false, "Hel"},
		{"fragment 2", "80 02 6c 6f", true, OpContinuation, false, "lo"},
		{"unmasked ping", "89 05 48 65 6c 6c 6f", true, OpPing, false, "Hello"},
		{"masked pong", "8a 85 37 fa 21 3d 7f 9f 4d 51 58", true, OpPong, true, "Hello"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wire := unhex(t, tc.wire)
			f, err := NewDecoder(bytes.NewReader(wire), 0).Next()
			if err != nil {
				t.Fatal(err)
			}
			h := f.Header
			if h.Fin != tc.fin || h.Opcode != tc.op || h.Masked != tc.masked || string(f.Payload) != tc.payload {
				t.Fatalf("decoded %+v %q", h, f.Payload)
			}
			// Byte-exact re-encoding is the fidelity property the proxy relies on.
			got, err := AppendFrame(nil, h, f.Payload)
			if err != nil || !bytes.Equal(got, wire) {
				t.Fatalf("re-encode = %x, %v; want %x", got, err, wire)
			}
		})
	}
}

// The length boundaries 125 / 126 / 65535 / 65536 decide which of the three
// header forms is used; off-by-one mistakes here are the classic codec bug.
func TestLengthBoundaries(t *testing.T) {
	tests := []struct {
		n        int
		wantForm LenEncoding
		wantHdr  int
	}{
		{0, Len7, 2},
		{125, Len7, 2},
		{126, Len16, 4},
		{65535, Len16, 4},
		{65536, Len64, 10},
	}
	for _, tc := range tests {
		payload := bytes.Repeat([]byte{'x'}, tc.n)
		for _, masked := range []bool{false, true} {
			h := Header{Fin: true, Opcode: OpBinary, Masked: masked, MaskKey: [4]byte{1, 2, 3, 4}}
			wire, err := AppendFrame(nil, h, payload)
			if err != nil {
				t.Fatal(err)
			}
			wantSize := tc.wantHdr
			if masked {
				wantSize += 4
			}
			if len(wire) != wantSize+tc.n {
				t.Fatalf("n=%d masked=%v: wire length %d, want %d", tc.n, masked, len(wire), wantSize+tc.n)
			}
			f, err := NewDecoder(bytes.NewReader(wire), 0).Next()
			if err != nil {
				t.Fatalf("n=%d: %v", tc.n, err)
			}
			if f.Header.LenEncoding != tc.wantForm || f.Header.PayloadLen != uint64(tc.n) || !bytes.Equal(f.Payload, payload) {
				t.Fatalf("n=%d: decoded form=%v len=%d", tc.n, f.Header.LenEncoding, f.Header.PayloadLen)
			}
			if f.Header.Size() != wantSize {
				t.Fatalf("n=%d: Header.Size()=%d want %d", tc.n, f.Header.Size(), wantSize)
			}
		}
	}
}

func TestAppendFrameLengthForm(t *testing.T) {
	_, err := AppendFrame(nil, Header{Fin: true, Opcode: OpBinary, LenEncoding: Len7}, make([]byte, 126))
	if !errors.Is(err, ErrLengthForm) {
		t.Fatalf("got %v", err)
	}
	_, err = AppendFrame(nil, Header{Fin: true, Opcode: OpBinary, LenEncoding: Len16}, make([]byte, 65536))
	if !errors.Is(err, ErrLengthForm) {
		t.Fatalf("got %v", err)
	}
}

// A frame that does not use the shortest length form is illegal but must still
// be observable and reproducible.
func TestNonMinimalLengthIsPreserved(t *testing.T) {
	wire := unhex(t, "82 7e 00 03 61 62 63") // 3 bytes sent with the 16-bit form
	f, err := NewDecoder(bytes.NewReader(wire), 0).Next()
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.LenEncoding != Len16 || string(f.Payload) != "abc" {
		t.Fatalf("%+v %q", f.Header, f.Payload)
	}
	vs := f.Header.Violations(Rules{Sender: RoleServer})
	if len(vs) != 1 || vs[0] != ViolationNonMinimalLength {
		t.Fatalf("violations = %v", vs)
	}
	got, _ := AppendFrame(nil, f.Header, f.Payload)
	if !bytes.Equal(got, wire) {
		t.Fatalf("re-encode = %x", got)
	}
}

func TestDecoderErrors(t *testing.T) {
	t.Run("clean EOF at frame boundary", func(t *testing.T) {
		if _, err := NewDecoder(bytes.NewReader(nil), 0).Next(); err != io.EOF {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("truncated at every offset", func(t *testing.T) {
		h := Header{Fin: true, Opcode: OpBinary, Masked: true, MaskKey: [4]byte{9, 8, 7, 6}}
		for _, size := range []int{5, 200, 70000} {
			wire, _ := AppendFrame(nil, h, bytes.Repeat([]byte{'z'}, size))
			cuts := []int{1, 2, 3, 4, 5, 8, 11, 12, 13, len(wire) - 1}
			for _, cut := range cuts {
				if cut <= 0 || cut >= len(wire) {
					continue
				}
				_, err := NewDecoder(bytes.NewReader(wire[:cut]), 0).Next()
				if err != io.ErrUnexpectedEOF {
					t.Fatalf("size=%d cut=%d: got %v, want ErrUnexpectedEOF", size, cut, err)
				}
			}
		}
	})
	t.Run("64-bit length with top bit set", func(t *testing.T) {
		wire := unhex(t, "82 7f 80 00 00 00 00 00 00 00")
		if _, err := NewDecoder(bytes.NewReader(wire), 0).Next(); !errors.Is(err, ErrInvalidLength) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("too large is rejected before reading the payload", func(t *testing.T) {
		wire := unhex(t, "82 7f 00 00 00 00 40 00 00 00") // announces 1 GiB, sends nothing
		f, err := NewDecoder(bytes.NewReader(wire), 1<<20).Next()
		if !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("got %v", err)
		}
		if f.Header.PayloadLen != 1<<30 {
			t.Fatalf("header not reported: %+v", f.Header)
		}
	})
	t.Run("large announced payload with little data", func(t *testing.T) {
		wire := unhex(t, "82 7f 00 00 00 00 00 80 00 00") // 8 MiB announced
		wire = append(wire, 1, 2, 3)
		_, err := NewDecoder(bytes.NewReader(wire), 16<<20).Next()
		if err != io.ErrUnexpectedEOF {
			t.Fatalf("got %v", err)
		}
	})
}

func TestViolations(t *testing.T) {
	client := Rules{Sender: RoleClient}
	server := Rules{Sender: RoleServer}
	tests := []struct {
		name  string
		h     Header
		rules Rules
		want  []Violation
	}{
		{"legal masked client text", Header{Fin: true, Opcode: OpText, Masked: true}, client, nil},
		{"legal unmasked server text", Header{Fin: true, Opcode: OpText}, server, nil},
		{"client frame unmasked", Header{Fin: true, Opcode: OpText}, client, []Violation{ViolationMaskMissing}},
		{"server frame masked", Header{Fin: true, Opcode: OpText, Masked: true}, server, []Violation{ViolationMaskUnexpected}},
		{"rsv1 without extension", Header{Fin: true, RSV1: true, Opcode: OpText}, server, []Violation{ViolationReservedBits}},
		{"rsv1 with extension on data frame", Header{Fin: true, RSV1: true, Opcode: OpText}, Rules{Sender: RoleServer, AllowRSV1: true}, nil},
		{"rsv1 with extension on continuation", Header{Fin: true, RSV1: true, Opcode: OpContinuation}, Rules{Sender: RoleServer, AllowRSV1: true}, []Violation{ViolationReservedBits}},
		{"rsv1 with extension on control", Header{Fin: true, RSV1: true, Opcode: OpPing}, Rules{Sender: RoleServer, AllowRSV1: true}, []Violation{ViolationReservedBits}},
		{"rsv2 always illegal", Header{Fin: true, RSV2: true, Opcode: OpText}, Rules{Sender: RoleServer, AllowRSV1: true}, []Violation{ViolationReservedBits}},
		{"reserved data opcode", Header{Fin: true, Opcode: 3}, server, []Violation{ViolationUnknownOpcode}},
		{"reserved control opcode", Header{Fin: true, Opcode: 0xB}, server, []Violation{ViolationUnknownOpcode}},
		{"fragmented ping", Header{Opcode: OpPing}, server, []Violation{ViolationControlFragmented}},
		{"oversized ping", Header{Fin: true, Opcode: OpPing, PayloadLen: 126, LenEncoding: Len16}, server, []Violation{ViolationControlTooLong}},
		{"several at once", Header{RSV3: true, Opcode: 0xF, PayloadLen: 200, LenEncoding: Len16}, client,
			[]Violation{ViolationReservedBits, ViolationUnknownOpcode, ViolationControlFragmented, ViolationControlTooLong, ViolationMaskMissing}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.h.Violations(tc.rules)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestViolationString(t *testing.T) {
	if ViolationMaskMissing.String() != "mask_missing" || Violation(250).String() == "" {
		t.Fatal("unexpected names")
	}
	if (&ProtocolError{ViolationInvalidUTF8, CloseInvalidData}).Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestOpcodeHelpers(t *testing.T) {
	for _, op := range []Opcode{OpContinuation, OpText, OpBinary, OpClose, OpPing, OpPong, 3, 0xB} {
		if op.String() == "" {
			t.Fatal("empty name")
		}
	}
	if !OpPing.IsControl() || OpText.IsControl() || !Opcode(0xF).IsControl() || Opcode(7).Known() {
		t.Fatal("classification wrong")
	}
}

// naiveMask is the textbook definition used as the oracle for the fast path.
func naiveMask(key [4]byte, offset uint64, b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[i] ^ key[(offset+uint64(i))%4]
	}
	return out
}

func TestMaskMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		var key [4]byte
		for i := range key {
			key[i] = byte(rng.IntN(256))
		}
		b := make([]byte, rng.IntN(100))
		for i := range b {
			b[i] = byte(rng.IntN(256))
		}
		offset := uint64(rng.IntN(1000))
		want := naiveMask(key, offset, b)
		got := bytes.Clone(b)
		MaskAt(key, offset, got)
		if !bytes.Equal(got, want) {
			t.Fatalf("len=%d offset=%d key=%x", len(b), offset, key)
		}
		MaskAt(key, offset, got) // XOR twice is the identity
		if !bytes.Equal(got, b) {
			t.Fatal("masking is not an involution")
		}
	}
}

// Masking a payload in two pieces must equal masking it whole: this is what a
// streaming proxy relies on.
func TestMaskChunking(t *testing.T) {
	key := [4]byte{0xde, 0xad, 0xbe, 0xef}
	data := []byte("the quick brown fox jumps over the lazy dog")
	whole := bytes.Clone(data)
	ApplyMask(key, whole)
	for split := 0; split <= len(data); split++ {
		parts := bytes.Clone(data)
		MaskAt(key, 0, parts[:split])
		MaskAt(key, uint64(split), parts[split:])
		if !bytes.Equal(parts, whole) {
			t.Fatalf("split=%d", split)
		}
	}
}

// Property: for any header and payload, decode(encode(x)) == x and the bytes
// are reproduced exactly.
func TestEncodeDecodeProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 7))
	sizes := []int{0, 1, 2, 124, 125, 126, 127, 255, 256, 65535, 65536, 70000}
	for range 600 {
		h := Header{
			Fin:    rng.IntN(2) == 0,
			RSV1:   rng.IntN(2) == 0,
			RSV2:   rng.IntN(2) == 0,
			RSV3:   rng.IntN(2) == 0,
			Opcode: Opcode(rng.IntN(16)),
			Masked: rng.IntN(2) == 0,
		}
		for i := range h.MaskKey {
			h.MaskKey[i] = byte(rng.IntN(256))
		}
		if !h.Masked {
			h.MaskKey = [4]byte{}
		}
		payload := make([]byte, sizes[rng.IntN(len(sizes))])
		for i := range payload {
			payload[i] = byte(rng.IntN(256))
		}
		// Sometimes force a longer-than-necessary length form.
		switch rng.IntN(3) {
		case 0:
			h.LenEncoding = LenAuto
		case 1:
			h.LenEncoding = Len64
		case 2:
			if len(payload) <= 0xFFFF {
				h.LenEncoding = Len16
			}
		}

		wire, err := AppendFrame(nil, h, payload)
		if err != nil {
			t.Fatal(err)
		}
		f, err := NewDecoder(bytes.NewReader(wire), 0).Next()
		if err != nil {
			t.Fatalf("%+v: %v", h, err)
		}
		if !bytes.Equal(f.Payload, payload) && !(len(payload) == 0 && len(f.Payload) == 0) {
			t.Fatalf("payload mismatch for %+v", h)
		}
		again, err := AppendFrame(nil, f.Header, f.Payload)
		if err != nil || !bytes.Equal(again, wire) {
			t.Fatalf("not byte-exact for %+v", h)
		}
	}
}

func TestDecoderReadsConsecutiveFrames(t *testing.T) {
	var wire []byte
	for _, s := range []string{"a", "bb", "ccc"} {
		wire, _ = AppendFrame(wire, Header{Fin: true, Opcode: OpText}, []byte(s))
	}
	d := NewDecoder(bytes.NewReader(wire), 0)
	for _, want := range []string{"a", "bb", "ccc"} {
		f, err := d.Next()
		if err != nil || string(f.Payload) != want {
			t.Fatalf("got %q, %v", f.Payload, err)
		}
	}
	if _, err := d.Next(); err != io.EOF {
		t.Fatalf("got %v", err)
	}
}

// FuzzDecoder checks the three properties that matter for a parser that eats
// untrusted bytes: it never panics, it never returns more than the limit, and
// whatever it accepted can be re-encoded to the exact same bytes.
func FuzzDecoder(f *testing.F) {
	f.Add(unhex(f, "81 05 48 65 6c 6c 6f"))
	f.Add(unhex(f, "81 85 37 fa 21 3d 7f 9f 4d 51 58"))
	f.Add(unhex(f, "82 7e 00 03 61 62 63"))
	f.Add(unhex(f, "82 7f 80 00 00 00 00 00 00 00"))
	f.Add(unhex(f, "01 03 48 65 6c 80 02 6c 6f 89 00"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		const limit = 1 << 16
		r := bytes.NewReader(data)
		d := NewDecoder(r, limit)
		var rebuilt []byte
		for {
			fr, err := d.Next()
			if err != nil {
				break
			}
			if uint64(len(fr.Payload)) > limit {
				t.Fatalf("payload %d exceeds limit", len(fr.Payload))
			}
			var aerr error
			rebuilt, aerr = AppendFrame(rebuilt, fr.Header, fr.Payload)
			if aerr != nil {
				t.Fatalf("re-encode failed: %v", aerr)
			}
		}
		if !bytes.HasPrefix(data, rebuilt) {
			t.Fatalf("re-encoded bytes differ from input prefix\n got %x\nfrom %x", rebuilt, data)
		}
	})
}
