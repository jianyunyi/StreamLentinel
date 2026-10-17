package sse

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

// parseWhole feeds the stream in one chunk and finishes it.
func parseWhole(t testing.TB, stream string) []Item {
	t.Helper()
	return parseChunks(t, []string{stream})
}

func parseChunks(t testing.TB, chunks []string) []Item {
	t.Helper()
	p := NewParser(Limits{})
	var all []Item
	for _, c := range chunks {
		items, err := p.Feed([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, items...)
	}
	return append(all, p.Finish()...)
}

func ev(typ, data, id string) Item {
	return Item{Kind: ItemEvent, Event: Event{Type: typ, Data: data, ID: id}}
}

func TestParseTable(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []Item
	}{
		{"single data line", "data: hello\n\n", []Item{ev("message", "hello", "")}},
		{"multi-line data joined by newline", "data: YHOO\ndata: +2\ndata: 10\n\n", []Item{ev("message", "YHOO\n+2\n10", "")}},
		{"no space after colon", "data:x\n\n", []Item{ev("message", "x", "")}},
		{"only one leading space is removed", "data:  two\n\n", []Item{ev("message", " two", "")}},
		{"field without colon means empty value", "data\n\n", []Item{ev("message", "", "")}},
		{"empty data value still dispatches", "data:\n\n", []Item{ev("message", "", "")}},
		{"two empty data lines", "data\ndata\n\n", []Item{ev("message", "\n", "")}},
		{"custom event type", "event: tick\ndata: 1\n\n", []Item{ev("tick", "1", "")}},
		{"event type does not stick to the next event", "event: a\ndata: 1\n\ndata: 2\n\n",
			[]Item{ev("a", "1", ""), ev("message", "2", "")}},
		{"id is sticky across events", "id: 7\ndata: a\n\ndata: b\n\n",
			[]Item{ev("message", "a", "7"), ev("message", "b", "7")}},
		{"id without value resets the id", "id: 7\ndata: a\n\nid\ndata: b\n\n",
			[]Item{ev("message", "a", "7"), ev("message", "b", "")}},
		{"blank line without data dispatches nothing", "\n\n\n", nil},
		{"event type alone does not dispatch and is reset", "event: x\n\ndata: y\n\n", []Item{ev("message", "y", "")}},
		{"comment", ": ping\n", []Item{{Kind: ItemComment, Comment: " ping"}}},
		{"comment does not break an event", "data: a\n: keepalive\ndata: b\n\n",
			[]Item{{Kind: ItemComment, Comment: " keepalive"}, ev("message", "a\nb", "")}},
		{"retry", "retry: 3000\n", []Item{{Kind: ItemRetry, Retry: 3 * time.Second}}},
		{"retry zero", "retry:0\n", []Item{{Kind: ItemRetry}}},
		{"retry not numeric", "retry: soon\n", []Item{{Kind: ItemIgnored, Field: "retry", Value: "soon", Reason: ReasonRetryNotNumeric}}},
		{"retry with sign", "retry: -5\n", []Item{{Kind: ItemIgnored, Field: "retry", Value: "-5", Reason: ReasonRetryNotNumeric}}},
		{"retry empty", "retry:\n", []Item{{Kind: ItemIgnored, Field: "retry", Reason: ReasonRetryNotNumeric}}},
		{"retry huge is clamped", "retry: 99999999999999999999999\n", []Item{{Kind: ItemRetry, Retry: maxRetry * time.Millisecond}}},
		{"id containing NUL is ignored", "id: a\x00b\ndata: x\n\n", []Item{
			{Kind: ItemIgnored, Field: "id", Value: "a\x00b", Reason: ReasonIDContainsNUL},
			ev("message", "x", ""),
		}},
		{"unknown field is reported", "evnet: oops\ndata: x\n\n", []Item{
			{Kind: ItemIgnored, Field: "evnet", Value: "oops", Reason: ReasonUnknownField},
			ev("message", "x", ""),
		}},
		{"field names are case sensitive", "Data: x\n\n", []Item{
			{Kind: ItemIgnored, Field: "Data", Value: "x", Reason: ReasonUnknownField},
		}},
		{"value may contain colons", "data: a:b:c\n\n", []Item{ev("message", "a:b:c", "")}},
		{"leading BOM is stripped", "\xef\xbb\xbfdata: x\n\n", []Item{ev("message", "x", "")}},
		{"BOM not at the start is data", "data: a\n\n\xef\xbb\xbfdata: b\n\n", []Item{
			ev("message", "a", ""),
			{Kind: ItemIgnored, Field: "\xef\xbb\xbfdata", Value: "b", Reason: ReasonUnknownField},
		}},
		{"CRLF line endings", "data: a\r\ndata: b\r\n\r\n", []Item{ev("message", "a\nb", "")}},
		{"lone CR line endings", "data: a\rdata: b\r\r", []Item{ev("message", "a\nb", "")}},
		{"mixed line endings", "data: a\ndata: b\r\ndata: c\r\n\n", []Item{ev("message", "a\nb\nc", "")}},
		{"CR then LF are one terminator, so no extra blank line", "data: a\r\n\ndata: b\n\n",
			[]Item{ev("message", "a", ""), ev("message", "b", "")}},
		{"invalid UTF-8 becomes U+FFFD", "data: a\xffb\n\n", []Item{ev("message", "a�b", "")}},
		{"unterminated event is discarded and reported", "data: a\n", []Item{
			{Kind: ItemDiscarded, Reason: ReasonIncompleteEvent, Value: "a"},
		}},
		{"unterminated line is discarded and reported", "data: a\n\ndata: b", []Item{
			ev("message", "a", ""),
			{Kind: ItemDiscarded, Reason: ReasonIncompleteLine, Value: "data: b"},
		}},
		{"empty stream", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseWhole(t, tc.stream)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// The reason this parser is written the way it is: every way of cutting the
// stream into network reads must give the same answer.
func TestEveryTwoWaySplitGivesSameResult(t *testing.T) {
	streams := []string{
		"\xef\xbb\xbfdata: héllo €\r\ndata: 日本\r\rid: 1\n\n",
		": c\nevent: e\nretry: 10\nid: 9\ndata: 😀\n\ndata: tail",
		"data: a\r\n\r\ndata: b\r\rdata: c\n\n",
	}
	for _, s := range streams {
		want := parseWhole(t, s)
		for cut := 0; cut <= len(s); cut++ {
			got := parseChunks(t, []string{s[:cut], s[cut:]})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cut=%d of %q\n got %+v\nwant %+v", cut, s, got, want)
			}
		}
		// And one byte per read, the worst case.
		bytewise := make([]string, len(s))
		for i := 0; i < len(s); i++ { // not `range s`: that would step by runes
			bytewise[i] = s[i : i+1]
		}
		if got := parseChunks(t, bytewise); !reflect.DeepEqual(got, want) {
			t.Fatalf("bytewise %q\n got %+v\nwant %+v", s, got, want)
		}
	}
}

func TestCRLFSplitBetweenChunks(t *testing.T) {
	got := parseChunks(t, []string{"data: a\r", "\n\r", "\n", "data: b\n\n"})
	want := []Item{ev("message", "a", ""), ev("message", "b", "")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestSplitBOM(t *testing.T) {
	got := parseChunks(t, []string{"\xef", "\xbb", "\xbfdata: x\n\n"})
	if !reflect.DeepEqual(got, []Item{ev("message", "x", "")}) {
		t.Fatalf("got %+v", got)
	}
	// Stream that starts like a BOM but is not one.
	got = parseChunks(t, []string{"\xef", "\xbbdata: x\n\n"})
	if len(got) != 1 || got[0].Kind != ItemIgnored {
		t.Fatalf("got %+v", got)
	}
	// Ends inside a would-be BOM.
	got = parseChunks(t, []string{"\xef\xbb"})
	if len(got) != 1 || got[0].Kind != ItemDiscarded {
		t.Fatalf("got %+v", got)
	}
}

func TestLastEventIDAndFinishTwice(t *testing.T) {
	p := NewParser(Limits{})
	if _, err := p.Feed([]byte("id: 42\ndata: x\n\n")); err != nil {
		t.Fatal(err)
	}
	if p.LastEventID() != "42" {
		t.Fatalf("LastEventID = %q", p.LastEventID())
	}
	if p.Finish() != nil || p.Finish() != nil {
		t.Fatal("clean stream should have nothing to report")
	}
}

func TestLimits(t *testing.T) {
	t.Run("line too long without terminator", func(t *testing.T) {
		p := NewParser(Limits{MaxLineBytes: 10})
		if _, err := p.Feed([]byte("data: 12345")); !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("got %v", err)
		}
		if _, err := p.Feed([]byte("x")); !errors.Is(err, ErrLineTooLong) {
			t.Fatal("error should be sticky")
		}
	})
	t.Run("line too long with terminator in the same chunk", func(t *testing.T) {
		p := NewParser(Limits{MaxLineBytes: 10})
		if _, err := p.Feed([]byte("data: 123456789\n")); !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("line limit accumulates across chunks", func(t *testing.T) {
		p := NewParser(Limits{MaxLineBytes: 10})
		if _, err := p.Feed([]byte("data: ")); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Feed([]byte("12345")); !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("event too large", func(t *testing.T) {
		p := NewParser(Limits{MaxEventBytes: 8})
		_, err := p.Feed([]byte("data: abcd\ndata: efgh\n\n"))
		if !errors.Is(err, ErrEventTooLarge) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("many small events do not hit the event limit", func(t *testing.T) {
		p := NewParser(Limits{MaxEventBytes: 8})
		items, err := p.Feed([]byte(strings.Repeat("data: abc\n\n", 100)))
		if err != nil || len(items) != 100 {
			t.Fatalf("%d items, %v", len(items), err)
		}
	})
}

func randomStream(rng *rand.Rand) string {
	pieces := []string{
		"data: x", "data:y", "data", "event: e", "id: 5", "id", "retry: 100", "retry: z",
		": c", "weird: v", "data: héllo", "data: 😀", "data: \xff", "id: a\x00", "\xef\xbb\xbf", "",
	}
	ends := []string{"\n", "\r", "\r\n"}
	var sb strings.Builder
	for range 1 + rng.IntN(20) {
		sb.WriteString(pieces[rng.IntN(len(pieces))])
		sb.WriteString(ends[rng.IntN(len(ends))])
	}
	if rng.IntN(3) == 0 {
		sb.WriteString("tail")
	}
	return sb.String()
}

func TestRandomChunkingProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 13))
	for range 1000 {
		s := randomStream(rng)
		want := parseWhole(t, s)
		var chunks []string
		for rest := s; len(rest) > 0; {
			n := 1 + rng.IntN(len(rest))
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		if got := parseChunks(t, chunks); !reflect.DeepEqual(got, want) {
			t.Fatalf("stream %q chunks %q\n got %+v\nwant %+v", s, chunks, got, want)
		}
	}
}

// FuzzChunkingIndependence is the property above, driven by the fuzzer.
func FuzzChunkingIndependence(f *testing.F) {
	f.Add([]byte("data: a\r\ndata: b\r\n\r\n"), uint8(3))
	f.Add([]byte("\xef\xbb\xbfid: 1\ndata: \xff\n\n"), uint8(2))
	f.Add([]byte(": c\nretry: 9\nevent: x\ndata\n\ndata: t"), uint8(7))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		whole := NewParser(Limits{MaxLineBytes: 1 << 12, MaxEventBytes: 1 << 12})
		wantItems, wantErr := whole.Feed(data)
		if wantErr == nil {
			wantItems = append(wantItems, whole.Finish()...)
		}

		n := int(step)%16 + 1
		split := NewParser(Limits{MaxLineBytes: 1 << 12, MaxEventBytes: 1 << 12})
		var gotItems []Item
		var gotErr error
		for rest := data; len(rest) > 0 && gotErr == nil; {
			k := min(n, len(rest))
			var items []Item
			items, gotErr = split.Feed(rest[:k])
			gotItems = append(gotItems, items...)
			rest = rest[k:]
		}
		if gotErr == nil {
			gotItems = append(gotItems, split.Finish()...)
		}

		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("error differs: whole=%v split=%v", wantErr, gotErr)
		}
		if wantErr == nil && !reflect.DeepEqual(gotItems, wantItems) {
			t.Fatalf("items differ\n got %+v\nwant %+v", gotItems, wantItems)
		}
	})
}
