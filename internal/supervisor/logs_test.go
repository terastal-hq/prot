package supervisor

import (
	"strings"
	"testing"
)

func messages(entries []LogEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Message
	}
	return out
}

func TestLogRingWrapsAndKeepsOrder(t *testing.T) {
	ring := NewLogRing(3)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		ring.Append(StreamStdout, m)
	}

	if got := strings.Join(messages(ring.GetRecent(0)), ","); got != "c,d,e" {
		t.Errorf("full buffer = %q, want c,d,e", got)
	}
	if got := strings.Join(messages(ring.GetRecent(2)), ","); got != "d,e" {
		t.Errorf("last two = %q, want d,e", got)
	}
	if got := len(ring.GetRecent(99)); got != 3 {
		t.Errorf("limit above size returned %d entries", got)
	}
}

func TestLogRingUnsubscribeTwiceDoesNotPanic(t *testing.T) {
	ring := NewLogRing(5)
	ch, unsub := ring.Subscribe()
	ring.Append(StreamSystem, "hello")
	if e := <-ch; e.Message != "hello" {
		t.Errorf("got %q", e.Message)
	}
	unsub()
	unsub()
	ring.Append(StreamSystem, "after") // must not send on the closed channel
}

func TestLineWriterSplitsLinesAcrossWrites(t *testing.T) {
	ring := NewLogRing(10)
	w := newLineWriter(ring, StreamStdout)

	_, _ = w.Write([]byte("one\ntw"))
	_, _ = w.Write([]byte("o\r\nthree"))
	if got := strings.Join(messages(ring.GetRecent(0)), "|"); got != "one|two" {
		t.Errorf("before flush = %q, want one|two (partial line held back, CR stripped)", got)
	}

	w.Flush()
	if got := strings.Join(messages(ring.GetRecent(0)), "|"); got != "one|two|three" {
		t.Errorf("after flush = %q, want the unterminated line emitted", got)
	}
}

func TestLineWriterBoundsHugeLines(t *testing.T) {
	ring := NewLogRing(10)
	w := newLineWriter(ring, StreamStderr)

	_, _ = w.Write([]byte(strings.Repeat("x", maxLineBytes*2+10) + "\n"))
	entries := ring.GetRecent(0)
	if len(entries) != 3 {
		t.Fatalf("expected the long line split into 3 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if len(e.Message) > maxLineBytes || e.Stream != StreamStderr {
			t.Errorf("bad chunk: %d bytes on %s", len(e.Message), e.Stream)
		}
	}
}

// Every line is delivered exactly once: lines buffered before subscribing come as history, later ones on the
// channel, none in both.
func TestSubscribeWithHistorySplitsCleanly(t *testing.T) {
	ring := NewLogRing(10)
	ring.Append(StreamStdout, "a")
	ring.Append(StreamStdout, "b")

	history, ch, unsub := ring.SubscribeWithHistory(0)
	defer unsub()
	ring.Append(StreamStdout, "c")
	ring.Append(StreamStdout, "d")

	if got := strings.Join(messages(history), ","); got != "a,b" {
		t.Errorf("history = %q, want a,b", got)
	}
	var live []string
	for len(ch) > 0 {
		live = append(live, (<-ch).Message)
	}
	if got := strings.Join(live, ","); got != "c,d" {
		t.Errorf("live = %q, want c,d", got)
	}
}

func TestSubscribeWithHistoryLimit(t *testing.T) {
	ring := NewLogRing(10)
	for _, m := range []string{"a", "b", "c", "d"} {
		ring.Append(StreamStdout, m)
	}

	history, _, unsub := ring.SubscribeWithHistory(2)
	defer unsub()
	if got := strings.Join(messages(history), ","); got != "c,d" {
		t.Errorf("history = %q, want c,d", got)
	}

	none, _, unsub2 := ring.SubscribeWithHistory(-1)
	defer unsub2()
	if len(none) != 0 {
		t.Errorf("limit -1 must return no history, got %v", messages(none))
	}
}
