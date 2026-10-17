package ws

// DefaultMaxMessage is the message size limit used when none is given.
const DefaultMaxMessage = 16 << 20

// MessageKind is the type of an application message.
type MessageKind uint8

const (
	KindText   MessageKind = iota + 1 // opcode 0x1 first frame
	KindBinary                        // opcode 0x2 first frame
)

func (k MessageKind) String() string {
	if k == KindText {
		return "text"
	}
	return "binary"
}

// Message is a complete application message.
type Message struct {
	Kind MessageKind
	// Payload is the concatenated fragments. If Compressed is true it is still
	// deflated (and, for text, still unvalidated): the adapter must inflate it
	// and may then validate UTF-8 with UTF8Validator.
	Payload    []byte
	Compressed bool
	// Fragments is the number of frames the message was split into.
	Fragments int
}

// Options configures an Assembler.
type Options struct {
	// Rules are applied to every header; Rules.Sender says who sent the frames.
	Rules Rules
	// MaxMessageSize bounds the reassembled message (0 = DefaultMaxMessage).
	MaxMessageSize uint64
}

// Output is what one pushed frame produced. At most one field is set; both are
// nil while a fragmented message is still incomplete.
type Output struct {
	Message *Message // a complete data message
	Control *Frame   // a control frame (ping, pong or close), delivered immediately
}

// Assembler is the strict message layer. It turns frames into messages and
// enforces RFC 6455 sections 5.4 (fragmentation), 5.5 (control frames) and
// 8.1 (UTF-8). It is not safe for concurrent use; one Assembler serves one
// direction of one connection.
//
// After Push returns an error the connection must be failed and the Assembler
// discarded.
type Assembler struct {
	opt Options

	inMessage  bool
	kind       MessageKind
	compressed bool
	frags      int
	buf        []byte
	utf8       UTF8Validator
}

// NewAssembler returns an Assembler.
func NewAssembler(o Options) *Assembler {
	if o.MaxMessageSize == 0 {
		o.MaxMessageSize = DefaultMaxMessage
	}
	return &Assembler{opt: o}
}

func protoErr(v Violation, code uint16) error { return &ProtocolError{Violation: v, CloseCode: code} }

// Push feeds the next frame of one direction.
func (a *Assembler) Push(f Frame) (Output, error) {
	if vs := f.Header.Violations(a.opt.Rules); len(vs) > 0 {
		return Output{}, protoErr(vs[0], CloseProtocolError)
	}

	op := f.Header.Opcode
	switch {
	case op.IsControl():
		// Control frames may appear in the middle of a fragmented message and
		// must not disturb it, so they do not touch the assembly state.
		if op == OpClose {
			if _, err := ParseClose(f.Payload); err != nil {
				return Output{}, err
			}
		}
		return Output{Control: &f}, nil

	case op == OpContinuation:
		if !a.inMessage {
			return Output{}, protoErr(ViolationUnexpectedContinuation, CloseProtocolError)
		}

	default: // text or binary: the first frame of a new message
		if a.inMessage {
			return Output{}, protoErr(ViolationInterleavedData, CloseProtocolError)
		}
		a.inMessage = true
		a.kind = KindBinary
		if op == OpText {
			a.kind = KindText
		}
		// RSV1 on the first frame marks a compressed message (RFC 7692).
		a.compressed = f.Header.RSV1
		a.frags = 0
		a.buf = nil
		a.utf8.Reset()
	}

	return a.addFragment(f)
}

func (a *Assembler) addFragment(f Frame) (Output, error) {
	if uint64(len(a.buf))+uint64(len(f.Payload)) > a.opt.MaxMessageSize {
		return Output{}, protoErr(ViolationMessageTooBig, CloseTooBig)
	}

	checkUTF8 := a.kind == KindText && !a.compressed
	if checkUTF8 && !a.utf8.Write(f.Payload) {
		return Output{}, protoErr(ViolationInvalidUTF8, CloseInvalidData)
	}

	// A message that arrives in one frame (the common case) keeps the
	// decoder's slice instead of being copied.
	if a.frags == 0 && f.Header.Fin {
		a.buf = f.Payload
	} else {
		a.buf = append(a.buf, f.Payload...)
	}
	a.frags++

	if !f.Header.Fin {
		return Output{}, nil
	}
	if checkUTF8 && !a.utf8.Finish() {
		return Output{}, protoErr(ViolationInvalidUTF8, CloseInvalidData)
	}

	msg := &Message{Kind: a.kind, Payload: a.buf, Compressed: a.compressed, Fragments: a.frags}
	a.inMessage = false
	a.buf = nil
	return Output{Message: msg}, nil
}
