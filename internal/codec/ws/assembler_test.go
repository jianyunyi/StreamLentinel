package ws

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
	"unicode/utf8"
)

func serverRules() Options { return Options{Rules: Rules{Sender: RoleServer}} }

func frame(op Opcode, fin bool, payload string) Frame {
	return Frame{Header: Header{Fin: fin, Opcode: op, PayloadLen: uint64(len(payload))}, Payload: []byte(payload)}
}

func wantProtocolError(t *testing.T, err error, v Violation, code uint16) {
	t.Helper()
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("got %v, want ProtocolError", err)
	}
	if pe.Violation != v || pe.CloseCode != code {
		t.Fatalf("got %s/%d, want %s/%d", pe.Violation, pe.CloseCode, v, code)
	}
}

func TestAssemblerSingleFrame(t *testing.T) {
	a := NewAssembler(serverRules())
	out, err := a.Push(frame(OpText, true, "hello"))
	if err != nil || out.Message == nil {
		t.Fatalf("%+v %v", out, err)
	}
	m := out.Message
	if m.Kind != KindText || string(m.Payload) != "hello" || m.Fragments != 1 || m.Compressed {
		t.Fatalf("%+v", m)
	}
	if KindText.String() != "text" || KindBinary.String() != "binary" {
		t.Fatal("kind names")
	}
}

func TestAssemblerFragmentedWithInterleavedControl(t *testing.T) {
	a := NewAssembler(serverRules())
	steps := []struct {
		f       Frame
		message string
		control Opcode
	}{
		{frame(OpBinary, false, "ab"), "", 0},
		{frame(OpPing, true, "p"), "", OpPing}, // control frames may be interleaved
		{frame(OpContinuation, false, "cd"), "", 0},
		{frame(OpContinuation, true, "ef"), "abcdef", 0},
	}
	for i, s := range steps {
		out, err := a.Push(s.f)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if s.control != 0 && (out.Control == nil || out.Control.Header.Opcode != s.control) {
			t.Fatalf("step %d: control = %+v", i, out.Control)
		}
		if s.message != "" {
			if out.Message == nil || string(out.Message.Payload) != s.message || out.Message.Fragments != 3 {
				t.Fatalf("step %d: message = %+v", i, out.Message)
			}
		} else if out.Message != nil {
			t.Fatalf("step %d: unexpected message", i)
		}
	}
	// The assembler is reusable for the next message.
	if out, err := a.Push(frame(OpText, true, "next")); err != nil || out.Message == nil {
		t.Fatalf("%v", err)
	}
}

func TestAssemblerSequenceErrors(t *testing.T) {
	t.Run("continuation without start", func(t *testing.T) {
		_, err := NewAssembler(serverRules()).Push(frame(OpContinuation, true, "x"))
		wantProtocolError(t, err, ViolationUnexpectedContinuation, CloseProtocolError)
	})
	t.Run("new data frame inside a fragmented message", func(t *testing.T) {
		a := NewAssembler(serverRules())
		if _, err := a.Push(frame(OpText, false, "a")); err != nil {
			t.Fatal(err)
		}
		_, err := a.Push(frame(OpText, true, "b"))
		wantProtocolError(t, err, ViolationInterleavedData, CloseProtocolError)
	})
	t.Run("header violation", func(t *testing.T) {
		_, err := NewAssembler(serverRules()).Push(frame(OpPing, false, ""))
		wantProtocolError(t, err, ViolationControlFragmented, CloseProtocolError)
	})
	t.Run("unmasked client frame", func(t *testing.T) {
		a := NewAssembler(Options{Rules: Rules{Sender: RoleClient}})
		_, err := a.Push(frame(OpText, true, "x"))
		wantProtocolError(t, err, ViolationMaskMissing, CloseProtocolError)
	})
}

func TestAssemblerUTF8(t *testing.T) {
	t.Run("character split across fragments is fine", func(t *testing.T) {
		euro := "€" // e2 82 ac
		a := NewAssembler(serverRules())
		if _, err := a.Push(frame(OpText, false, euro[:1])); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Push(frame(OpContinuation, false, euro[1:2])); err != nil {
			t.Fatal(err)
		}
		out, err := a.Push(frame(OpContinuation, true, euro[2:]))
		if err != nil || string(out.Message.Payload) != euro {
			t.Fatalf("%v", err)
		}
	})
	t.Run("invalid byte fails on the fragment that carries it", func(t *testing.T) {
		a := NewAssembler(serverRules())
		_, err := a.Push(frame(OpText, false, "ok\xff")) // not the final fragment
		wantProtocolError(t, err, ViolationInvalidUTF8, CloseInvalidData)
	})
	t.Run("message ends inside a character", func(t *testing.T) {
		_, err := NewAssembler(serverRules()).Push(frame(OpText, true, "a\xe2\x82"))
		wantProtocolError(t, err, ViolationInvalidUTF8, CloseInvalidData)
	})
	t.Run("binary messages are never validated", func(t *testing.T) {
		out, err := NewAssembler(serverRules()).Push(frame(OpBinary, true, "\xff\xfe"))
		if err != nil || out.Message == nil {
			t.Fatal(err)
		}
	})
	t.Run("compressed text is left to the adapter", func(t *testing.T) {
		a := NewAssembler(Options{Rules: Rules{Sender: RoleServer, AllowRSV1: true}})
		f := frame(OpText, true, "\xff\xff")
		f.Header.RSV1 = true
		out, err := a.Push(f)
		if err != nil || !out.Message.Compressed {
			t.Fatalf("%v", err)
		}
	})
}

func TestAssemblerMessageSizeLimit(t *testing.T) {
	a := NewAssembler(Options{Rules: Rules{Sender: RoleServer}, MaxMessageSize: 10})
	if _, err := a.Push(frame(OpBinary, false, "123456")); err != nil {
		t.Fatal(err)
	}
	_, err := a.Push(frame(OpContinuation, true, "78901"))
	wantProtocolError(t, err, ViolationMessageTooBig, CloseTooBig)
}

func TestParseClose(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		c, err := ParseClose(nil)
		if err != nil || c.HasCode {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("code and reason", func(t *testing.T) {
		c, err := ParseClose(append([]byte{0x03, 0xe8}, "bye"...))
		if err != nil || !c.HasCode || c.Code != 1000 || c.Reason != "bye" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("one byte", func(t *testing.T) {
		_, err := ParseClose([]byte{0x03})
		wantProtocolError(t, err, ViolationBadClosePayload, CloseProtocolError)
	})
	t.Run("reserved codes", func(t *testing.T) {
		for _, code := range []uint16{0, 999, 1004, 1005, 1006, 1015, 1016, 2000, 2999, 5000} {
			_, err := ParseClose([]byte{byte(code >> 8), byte(code)})
			wantProtocolError(t, err, ViolationBadClosePayload, CloseProtocolError)
		}
	})
	t.Run("valid codes", func(t *testing.T) {
		for _, code := range []uint16{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014, 3000, 3999, 4000, 4999} {
			if _, err := ParseClose([]byte{byte(code >> 8), byte(code)}); err != nil {
				t.Fatalf("code %d: %v", code, err)
			}
		}
	})
	t.Run("invalid utf-8 reason", func(t *testing.T) {
		_, err := ParseClose([]byte{0x03, 0xe8, 0xff})
		wantProtocolError(t, err, ViolationInvalidUTF8, CloseInvalidData)
	})
	t.Run("through the assembler", func(t *testing.T) {
		_, err := NewAssembler(serverRules()).Push(Frame{Header: Header{Fin: true, Opcode: OpClose, PayloadLen: 1}, Payload: []byte{1}})
		wantProtocolError(t, err, ViolationBadClosePayload, CloseProtocolError)
		out, err := NewAssembler(serverRules()).Push(frame(OpClose, true, "\x03\xe8"))
		if err != nil || out.Control == nil {
			t.Fatal(err)
		}
	})
}

func TestAppendClosePayload(t *testing.T) {
	p := AppendClosePayload(nil, CloseNormal, "bye")
	if !bytes.Equal(p, []byte{0x03, 0xe8, 'b', 'y', 'e'}) {
		t.Fatalf("%x", p)
	}
	long := bytes.Repeat([]byte("€"), 100) // 300 bytes, each character 3 bytes
	p = AppendClosePayload(nil, CloseGoingAway, string(long))
	if len(p) > MaxControlPayload || !utf8.Valid(p[2:]) {
		t.Fatalf("len=%d valid=%v", len(p), utf8.Valid(p[2:]))
	}
	if _, err := ParseClose(p); err != nil {
		t.Fatal(err)
	}
}

// --- UTF-8 validator -------------------------------------------------------

func TestUTF8ValidatorEveryChunking(t *testing.T) {
	samples := []string{"", "ascii", "héllo", "€uro", "日本語", "😀 emoji", "\u0000\u007f\u0080߿ࠀ￿\U00010000\U0010ffff"}
	for _, s := range samples {
		for split := 0; split <= len(s); split++ {
			var v UTF8Validator
			if !v.Write([]byte(s[:split])) || !v.Write([]byte(s[split:])) || !v.Finish() {
				t.Fatalf("%q rejected at split %d", s, split)
			}
		}
		// One byte at a time is the worst case.
		var v UTF8Validator
		for i := 0; i < len(s); i++ {
			if !v.Write([]byte{s[i]}) {
				t.Fatalf("%q rejected byte-wise at %d", s, i)
			}
		}
		if !v.Finish() {
			t.Fatalf("%q not finished", s)
		}
	}
}

func TestUTF8ValidatorRejects(t *testing.T) {
	bad := []string{
		"\x80",             // lone continuation byte
		"\xc0\x80",         // overlong NUL
		"\xc1\xbf",         // overlong
		"\xe0\x80\x80",     // overlong 3-byte
		"\xed\xa0\x80",     // UTF-16 surrogate
		"\xf4\x90\x80\x80", // beyond U+10FFFF
		"\xf8\x88\x80\x80\x80",
		"\xff",
		"\xe2\x28\xa1",
	}
	for _, s := range bad {
		for split := 0; split <= len(s); split++ {
			var v UTF8Validator
			ok := v.Write([]byte(s[:split]))
			ok = ok && v.Write([]byte(s[split:]))
			ok = ok && v.Finish()
			if ok {
				t.Fatalf("%x accepted at split %d", s, split)
			}
		}
	}
	// Truncated at the end: Write accepts it (more bytes may follow) but Finish does not.
	var v UTF8Validator
	if !v.Write([]byte("a\xe2\x82")) || v.Finish() {
		t.Fatal("truncated character should be pending, then fail at Finish")
	}
	v.Reset()
	if !v.Finish() {
		t.Fatal("Reset should clear pending bytes")
	}
}

// FuzzUTF8Chunking: splitting the input into pieces must never change the
// verdict, and the verdict must equal the standard library's.
func FuzzUTF8Chunking(f *testing.F) {
	f.Add([]byte("héllo €"), uint8(3), uint8(5))
	f.Add([]byte("\xed\xa0\x80"), uint8(1), uint8(2))
	f.Add([]byte("\xf0\x9f\x98\x80"), uint8(1), uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, a, b uint8) {
		cut1 := int(a) % (len(data) + 1)
		cut2 := cut1 + int(b)%(len(data)-cut1+1)
		var v UTF8Validator
		ok := v.Write(data[:cut1]) && v.Write(data[cut1:cut2]) && v.Write(data[cut2:]) && v.Finish()
		if ok != utf8.Valid(data) {
			t.Fatalf("chunked=%v stdlib=%v for %x (cuts %d,%d)", ok, utf8.Valid(data), data, cut1, cut2)
		}
	})
}

// FuzzAssembler: arbitrary frame sequences never panic, and every message that
// comes out respects the invariants the rest of the system will assume.
func FuzzAssembler(f *testing.F) {
	f.Add(unhex(f, "01 03 48 65 6c 89 00 80 02 6c 6f"), false)
	f.Add(unhex(f, "81 02 c3 28"), false)
	f.Add(unhex(f, "88 01 00"), true)
	f.Fuzz(func(t *testing.T, data []byte, fromClient bool) {
		rules := Rules{Sender: RoleServer, AllowRSV1: len(data)%2 == 0}
		if fromClient {
			rules.Sender = RoleClient
		}
		a := NewAssembler(Options{Rules: rules, MaxMessageSize: 1 << 12})
		d := NewDecoder(bytes.NewReader(data), 1<<12)
		for {
			fr, err := d.Next()
			if err != nil {
				return
			}
			out, err := a.Push(fr)
			if err != nil {
				var pe *ProtocolError
				if !errors.As(err, &pe) {
					t.Fatalf("non-protocol error %v", err)
				}
				return
			}
			if out.Message != nil {
				m := out.Message
				if len(m.Payload) > 1<<12 {
					t.Fatal("message over limit")
				}
				if m.Kind == KindText && !m.Compressed && !utf8.Valid(m.Payload) {
					t.Fatal("invalid UTF-8 in accepted text message")
				}
			}
		}
	})
}

// Property: any message split into fragments at random points is reassembled
// to the original, with ping frames injected between fragments.
func TestAssemblerRandomFragmentationProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	text := []byte("héllo wörld — 日本語 😀 ")
	for range 300 {
		msg := bytes.Repeat(text, 1+rng.IntN(5))
		a := NewAssembler(serverRules())
		var got *Message
		rest := msg
		first := true
		for len(rest) > 0 || first {
			n := rng.IntN(len(rest) + 1)
			piece := rest[:n]
			rest = rest[n:]
			op := OpContinuation
			if first {
				op = OpText
			}
			first = false
			out, err := a.Push(Frame{Header: Header{Fin: len(rest) == 0, Opcode: op}, Payload: piece})
			if err != nil {
				t.Fatal(err)
			}
			if out.Message != nil {
				got = out.Message
			}
			if rng.IntN(3) == 0 {
				if _, err := a.Push(frame(OpPing, true, "x")); err != nil {
					t.Fatal(err)
				}
			}
		}
		if got == nil || !bytes.Equal(got.Payload, msg) {
			t.Fatalf("reassembly mismatch")
		}
	}
}
