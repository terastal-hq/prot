package supervisor

import (
	"bytes"
	"sync"
	"time"
)

// Log stream names used in LogEntry.Stream.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamSystem = "system"
)

// maxLineBytes bounds a single log line. Longer output is split rather than buffered without limit.
const maxLineBytes = 64 * 1024

// LogEntry represents a single captured log line.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"` // StreamStdout, StreamStderr or StreamSystem
	Message   string    `json:"message"`
}

// LogRing is a fixed-size ring buffer of log lines with live subscribers.
type LogRing struct {
	mu          sync.RWMutex
	entries     []LogEntry // fixed length; slot (start+i)%len holds the i-th oldest entry
	start       int
	count       int
	subscribers map[chan LogEntry]struct{}
}

// NewLogRing creates a ring buffer holding at most capacity lines (default 2000).
func NewLogRing(capacity int) *LogRing {
	if capacity <= 0 {
		capacity = 2000
	}
	return &LogRing{
		entries:     make([]LogEntry, capacity),
		subscribers: make(map[chan LogEntry]struct{}),
	}
}

// Append adds a new log line, overwriting the oldest when full, and notifies subscribers.
func (r *LogRing) Append(stream, message string) {
	entry := LogEntry{Timestamp: time.Now().UTC(), Stream: stream, Message: message}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count < len(r.entries) {
		r.entries[(r.start+r.count)%len(r.entries)] = entry
		r.count++
	} else {
		r.entries[r.start] = entry
		r.start = (r.start + 1) % len(r.entries)
	}

	for ch := range r.subscribers {
		select {
		case ch <- entry:
		default: // slow subscriber: drop rather than block the process output
		}
	}
}

// GetRecent returns the last limit entries, oldest first. A limit <= 0 returns everything buffered.
func (r *LogRing) GetRecent(limit int) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > r.count {
		limit = r.count
	}

	result := make([]LogEntry, limit)
	skip := r.count - limit
	for i := range result {
		result[i] = r.entries[(r.start+skip+i)%len(r.entries)]
	}
	return result
}

// Subscribe returns a channel that receives live entries and an unsubscribe function, which is safe to call
// more than once.
func (r *LogRing) Subscribe() (<-chan LogEntry, func()) {
	ch := make(chan LogEntry, 100)
	r.mu.Lock()
	r.subscribers[ch] = struct{}{}
	r.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.subscribers, ch)
			close(ch)
			r.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// lineWriter is an io.Writer that turns a byte stream into log lines. Handing it to exec.Cmd as Stdout or
// Stderr lets Cmd.Wait drain the pipe completely before returning, so the final lines of a process that exits
// or crashes are never lost.
type lineWriter struct {
	ring    *LogRing
	stream  string
	pending []byte
}

func newLineWriter(ring *LogRing, stream string) *lineWriter {
	return &lineWriter{ring: ring, stream: stream}
}

// Write splits p into lines. exec.Cmd calls it from a single goroutine per stream, so it needs no locking.
func (w *lineWriter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			w.pending = append(w.pending, p...)
			w.flushOversized()
			break
		}
		w.pending = append(w.pending, p[:newline]...)
		w.emit()
		p = p[newline+1:]
	}
	return written, nil
}

// Flush emits a trailing line that had no newline. Call it once the process has exited.
func (w *lineWriter) Flush() {
	if len(w.pending) > 0 {
		w.emit()
	}
}

func (w *lineWriter) flushOversized() {
	for len(w.pending) >= maxLineBytes {
		w.ring.Append(w.stream, string(w.pending[:maxLineBytes]))
		w.pending = w.pending[maxLineBytes:]
	}
}

func (w *lineWriter) emit() {
	line := bytes.TrimSuffix(w.pending, []byte{'\r'})
	w.flushLine(line)
	w.pending = w.pending[:0]
}

func (w *lineWriter) flushLine(line []byte) {
	for len(line) > maxLineBytes {
		w.ring.Append(w.stream, string(line[:maxLineBytes]))
		line = line[maxLineBytes:]
	}
	w.ring.Append(w.stream, string(line))
}
