package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/storage"
)

func TestLogRing(t *testing.T) {
	ring := NewLogRing(3)
	ring.Append("stdout", "line 1")
	ring.Append("stdout", "line 2")
	ring.Append("stderr", "line 3")
	ring.Append("stdout", "line 4") // should drop line 1

	entries := ring.GetRecent(10)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Message != "line 2" {
		t.Errorf("expected oldest remaining to be line 2, got %s", entries[0].Message)
	}
	if entries[2].Message != "line 4" {
		t.Errorf("expected newest to be line 4, got %s", entries[2].Message)
	}

	// Test subscription
	ch, unsub := ring.Subscribe()
	defer unsub()

	go ring.Append("stdout", "sub message")

	select {
	case entry := <-ch:
		if entry.Message != "sub message" {
			t.Errorf("unexpected subscriber message: %s", entry.Message)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for subscription")
	}
}

func TestSupervisorLifecycle(t *testing.T) {
	statusEvents := make(chan string, 10)
	sup := New(func(id, status string, port uint32) {
		statusEvents <- status
	})

	svc := storage.Service{
		ID:            "test-proc",
		Name:          "Echo Daemon",
		Command:       "sh -c 'echo server-ready; sleep 30'",
		AutoPort:      false,
		RestartPolicy: "no",
	}

	ctx := context.Background()
	state, err := sup.Start(ctx, svc)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if state.Status != "starting" && state.Status != "running" {
		t.Errorf("expected starting/running state, got %s", state.Status)
	}

	// Wait briefly for stdout log
	time.Sleep(300 * time.Millisecond)

	logs, err := sup.GetLogs("test-proc", 50)
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	foundReady := false
	for _, l := range logs {
		if strings.Contains(l.Message, "server-ready") {
			foundReady = true
			break
		}
	}
	if !foundReady {
		t.Errorf("did not find 'server-ready' in captured logs: %+v", logs)
	}

	// Stop process
	if err := sup.Stop("test-proc", 1*time.Second); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	st, ok := sup.GetState("test-proc")
	if !ok || st.Status != "stopped" {
		t.Errorf("expected stopped status after Stop, got %+v", st)
	}
}
