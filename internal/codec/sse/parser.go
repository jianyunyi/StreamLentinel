// Package sse is an incremental parser for the text/event-stream format
// (WHATWG HTML Living Standard, section 9.2 "Server-sent events").
//
// # Design
//
// A network read can end anywhere: between two events, in the middle of a
// line, between the CR and the LF of a CRLF pair, or inside the 3-byte BOM or a
// multi-byte character. The Parser therefore keeps explicit state between
// Feed calls, and its central guarantee, checked by a property test and a fuzz
// test, is chunking independence: the same bytes always produce the same items
// no matter how they were split.
//
// Unlike a browser's EventSource, a debugger must not hide what the standard
// tells a client to ignore. The parser reports comments (keep-alives),
// retry hints, and ignored lines (misspelled field names, ids containing NUL,
// non-numeric retry) as items next to the events, so the UI can explain why an
// "event" never arrived.
package sse

import (
	"bytes"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"
)

// DefaultMessageType is the type of an event that has no event field.
const DefaultMessageType = "message"

// ItemKind says what an Item carries.
type ItemKind uint8

const (
	// ItemEvent is a dispatched event.
	ItemEvent ItemKind = iota + 1
	// ItemComment is a line starting with ':'. Servers use them as keep-alives.
	ItemComment
	// ItemRetry is a valid retry field: the new reconnection delay.
	ItemRetry
	// ItemIgnored is a line a conforming client must ignore; Reason says why.
	ItemIgnored
	// ItemDiscarded is reported by Finish when the stream ended mid-event.
	ItemDiscarded
)

// Reasons attached to ItemIgnored and ItemDiscarded. They are stable strings
// used by the event schema.
const (
	ReasonUnknownField    = "unknown_field"
	ReasonIDContainsNUL   = "id_contains_nul"
	ReasonRetryNotNumeric = "retry_not_numeric"
	ReasonIncompleteEvent = "incomplete_event"
	ReasonIncompleteLine  = "incomplete_line"
)

// Event is a dispatched server-sent event.
type Event struct {
	// Type is the event field, or DefaultMessageType when absent.
	Type string
	// Data is the data lines joined with "\n" (without a trailing newline).
	Data string
	// ID is the last event ID in effect when the event was dispatched. It is
	// sticky: it persists across events until a new id field replaces it.
	ID string
}

// Item is one thing the parser observed.
type Item struct {
	Kind    ItemKind
	Event   Event         // ItemEvent
	Comment string        // ItemComment: the text after the colon
	Retry   time.Duration // ItemRetry
	Field   string        // ItemIgnored: field name
	Value   string        // ItemIgnored: field value; ItemDiscarded: the dropped text
	Reason  string        // ItemIgnored, ItemDiscarded
}

// Limits bound the memory an untrusted stream can make the parser hold.
type Limits struct {
	MaxLineBytes  int // longest single line (0 = 1 MiB)
	MaxEventBytes int // longest accumulated data of one event (0 = 8 MiB)
}

// Errors returned by Feed. After one of them the parser is unusable because
// resynchronising on an arbitrary byte stream would be guesswork.
var (
	ErrLineTooLong   = errors.New("sse: line exceeds the configured limit")
	ErrEventTooLarge = errors.New("sse: event data exceeds the configured limit")
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// maxRetry caps the reconnection delay we are willing to report (about 24
// days); anything larger would overflow time.Duration arithmetic downstream.
const maxRetry = 1<<31 - 1 // milliseconds

// Parser parses an event stream incrementally. The zero value is not usable;
// call NewParser. It is not safe for concurrent use.
type Parser struct {
	lim Limits
	err error

	started bool   // the BOM decision has been made
	head    []byte // bytes held while deciding whether the stream starts with a BOM
	skipLF  bool   // the previous chunk ended with CR: swallow an immediately following LF

	line      []byte // the current, unterminated line
	data      []byte // data buffer: every data value followed by '\n'
	eventType string
	lastID    string
	finished  bool
}

// NewParser returns a Parser using the given limits.
func NewParser(l Limits) *Parser {
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = 1 << 20
	}
	if l.MaxEventBytes <= 0 {
		l.MaxEventBytes = 8 << 20
	}
	return &Parser{lim: l}
}

// LastEventID returns the current last event ID. A reconnecting client sends
// it in the Last-Event-ID request header.
func (p *Parser) LastEventID() string { return p.lastID }

// Feed consumes the next chunk of the stream and returns the items completed by
// it. Chunks may be split anywhere.
func (p *Parser) Feed(chunk []byte) ([]Item, error) {
	if p.err != nil {
		return nil, p.err
	}
	if !p.started {
		p.head = append(p.head, chunk...)
		if len(p.head) < len(bom) && bytes.HasPrefix(bom, p.head) {
			return nil, nil // still could be a BOM: wait for more bytes
		}
		p.started = true
		chunk = p.head
		if bytes.HasPrefix(chunk, bom) {
			chunk = chunk[len(bom):] // a BOM is only stripped at the very start
		}
		p.head = nil
	}

	var items []Item
	for len(chunk) > 0 {
		if p.skipLF {
			p.skipLF = false
			if chunk[0] == '\n' { // second half of a CRLF split across chunks
				chunk = chunk[1:]
				continue
			}
		}

		i := bytes.IndexAny(chunk, "\r\n")
		if i < 0 {
			if len(p.line)+len(chunk) > p.lim.MaxLineBytes {
				p.err = ErrLineTooLong
				return items, p.err
			}
			p.line = append(p.line, chunk...)
			break
		}
		if len(p.line)+i > p.lim.MaxLineBytes {
			p.err = ErrLineTooLong
			return items, p.err
		}
		p.line = append(p.line, chunk[:i]...)
		p.skipLF = chunk[i] == '\r'
		chunk = chunk[i+1:]

		var err error
		if items, err = p.processLine(items); err != nil {
			p.err = err
			return items, err
		}
		p.line = p.line[:0]
	}
	return items, nil
}

// Finish signals the end of the stream. Per the standard an event that was not
// terminated by a blank line is discarded; Finish reports that as an
// ItemDiscarded instead of dropping it silently.
func (p *Parser) Finish() []Item {
	if p.finished {
		return nil
	}
	p.finished = true
	if !p.started {
		// A stream that ends inside a would-be BOM: those bytes are a
		// (nonsense) unterminated line.
		p.line = append(p.line, p.head...)
		p.head = nil
	}
	switch {
	case len(p.data) > 0:
		return []Item{{Kind: ItemDiscarded, Reason: ReasonIncompleteEvent,
			Value: sanitize(bytes.TrimSuffix(p.data, []byte{'\n'}))}}
	case len(p.line) > 0:
		return []Item{{Kind: ItemDiscarded, Reason: ReasonIncompleteLine, Value: sanitize(p.line)}}
	}
	return nil
}

func (p *Parser) processLine(items []Item) ([]Item, error) {
	line := p.line

	// An empty line dispatches the event.
	if len(line) == 0 {
		if len(p.data) == 0 {
			p.eventType = "" // nothing to dispatch; the standard still resets the type
			return items, nil
		}
		typ := p.eventType
		if typ == "" {
			typ = DefaultMessageType
		}
		items = append(items, Item{Kind: ItemEvent, Event: Event{
			Type: typ,
			Data: string(p.data[:len(p.data)-1]), // drop the final '\n'
			ID:   p.lastID,
		}})
		p.data = p.data[:0]
		p.eventType = ""
		return items, nil
	}

	if line[0] == ':' {
		return append(items, Item{Kind: ItemComment, Comment: sanitize(line[1:])}), nil
	}

	name, value := line, []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		name, value = line[:i], line[i+1:]
		if len(value) > 0 && value[0] == ' ' { // exactly one leading space is removed
			value = value[1:]
		}
	}

	switch string(name) {
	case "event":
		p.eventType = sanitize(value)
	case "data":
		if len(p.data)+len(value)+1 > p.lim.MaxEventBytes {
			return items, ErrEventTooLarge
		}
		p.data = append(p.data, sanitize(value)...)
		p.data = append(p.data, '\n')
	case "id":
		if bytes.IndexByte(value, 0) >= 0 {
			return append(items, ignored(name, value, ReasonIDContainsNUL)), nil
		}
		p.lastID = sanitize(value) // "id" with an empty value resets the ID
	case "retry":
		ms, ok := parseRetry(value)
		if !ok {
			return append(items, ignored(name, value, ReasonRetryNotNumeric)), nil
		}
		items = append(items, Item{Kind: ItemRetry, Retry: time.Duration(ms) * time.Millisecond})
	default:
		items = append(items, ignored(name, value, ReasonUnknownField))
	}
	return items, nil
}

func ignored(name, value []byte, reason string) Item {
	return Item{Kind: ItemIgnored, Field: sanitize(name), Value: sanitize(value), Reason: reason}
}

// parseRetry accepts only ASCII digits, as the standard requires: no sign, no
// spaces, no decimal point. Values above maxRetry are clamped.
func parseRetry(v []byte) (int64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil || n > maxRetry { // err means overflow: the digits were already checked
		return maxRetry, true
	}
	return n, true
}

// sanitize converts bytes to a string, replacing every invalid byte with
// U+FFFD, which is what decoding the stream as UTF-8 requires.
//
// Known deviation: the Encoding Standard replaces each "maximal subpart" of an
// ill-formed sequence with one U+FFFD, whereas this replaces every offending
// byte. The two differ only for truncated multi-byte sequences.
func sanitize(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb bytes.Buffer
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.Write(b[:size])
		}
		b = b[size:]
	}
	return sb.String()
}
