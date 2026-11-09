package supervisor

import (
	"context"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/storage"
)

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

func statusOf(s *Supervisor, id string) string {
	st, ok := s.GetState(id)
	if !ok {
		return "unknown"
	}
	return st.Status
}

func logText(s *Supervisor, id string) string {
	logs, _ := s.GetLogs(id, 0)
	lines := make([]string, len(logs))
	for i, l := range logs {
		lines[i] = l.Stream + ": " + l.Message
	}
	return strings.Join(lines, "\n")
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// A process that prints a lot and exits right away must not lose its last line (the pipe was previously
// closed by Wait before the reader goroutine had drained it).
func TestFinalOutputIsNeverLost(t *testing.T) {
	const runs = 40
	for i := 0; i < runs; i++ {
		s := New(nil)
		svc := storage.Service{
			ID:            "tail",
			Command:       "yes xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx | head -n 5000; echo FINAL_LINE",
			RestartPolicy: storage.RestartNever,
		}
		if _, err := s.Start(context.Background(), svc); err != nil {
			t.Fatalf("start: %v", err)
		}
		eventually(t, 5*time.Second, "process to finish", func() bool { return statusOf(s, "tail") == storage.StatusStopped })

		if !strings.Contains(logText(s, "tail"), "FINAL_LINE") {
			t.Fatalf("run %d lost the final output line", i)
		}
	}
}

func TestStderrAndStdoutAreSeparated(t *testing.T) {
	s := New(nil)
	svc := storage.Service{ID: "streams", Command: "echo to-out; echo to-err >&2", RestartPolicy: storage.RestartNever}
	_, _ = s.Start(context.Background(), svc)
	eventually(t, 5*time.Second, "exit", func() bool { return statusOf(s, "streams") == storage.StatusStopped })

	text := logText(s, "streams")
	if !strings.Contains(text, "stdout: to-out") || !strings.Contains(text, "stderr: to-err") {
		t.Errorf("streams not labelled correctly:\n%s", text)
	}
}

// Stop must not return until the process is really gone, otherwise Restart can race the old instance.
func TestStopWaitsForProcessExit(t *testing.T) {
	s := New(nil)
	st, err := s.Start(context.Background(), storage.Service{ID: "sleeper", Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	pid := st.PID
	eventually(t, 2*time.Second, "running", func() bool { return statusOf(s, "sleeper") == storage.StatusRunning })

	if err := s.Stop("sleeper", 2*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if processAlive(pid) {
		t.Error("process still alive right after Stop returned")
	}
	if got := statusOf(s, "sleeper"); got != storage.StatusStopped {
		t.Errorf("status = %q, want stopped", got)
	}
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	s := New(nil)
	// The shell ignores SIGTERM, so only SIGKILL can end it.
	svc := storage.Service{ID: "stubborn", Command: "trap '' TERM; echo armed; sleep 30; sleep 30"}
	st, _ := s.Start(context.Background(), svc)
	pid := st.PID
	// Signalling before the trap is installed would just kill the shell and prove nothing.
	eventually(t, 2*time.Second, "trap to be armed", func() bool {
		return strings.Contains(logText(s, "stubborn"), "stdout: armed")
	})

	start := time.Now()
	if err := s.Stop("stubborn", 300*time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("escalation too slow: %v", elapsed)
	}
	if processAlive(pid) {
		t.Error("stubborn process survived Stop")
	}
	if !strings.Contains(logText(s, "stubborn"), "SIGKILL") {
		t.Errorf("escalation not logged:\n%s", logText(s, "stubborn"))
	}
}

func TestArgsAreQuotedNotInterpreted(t *testing.T) {
	s := New(nil)
	svc := storage.Service{
		ID:            "args",
		Command:       "printf '[%s]\\n'",
		Args:          []string{"two words", "semi;colon", "it's", "$HOME"},
		RestartPolicy: storage.RestartNever,
	}
	_, _ = s.Start(context.Background(), svc)
	eventually(t, 5*time.Second, "exit", func() bool { return statusOf(s, "args") == storage.StatusStopped })

	text := logText(s, "args")
	for _, want := range []string{"[two words]", "[semi;colon]", "[it's]", "[$HOME]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in output:\n%s", want, text)
		}
	}
}

func TestCleanExitIsNotACrash(t *testing.T) {
	s := New(nil)
	svc := storage.Service{ID: "job", Command: "true", RestartPolicy: storage.RestartOnFailure}
	_, _ = s.Start(context.Background(), svc)
	eventually(t, 5*time.Second, "finish", func() bool { return statusOf(s, "job") != storage.StatusStarting })
	eventually(t, 2*time.Second, "settle", func() bool { return statusOf(s, "job") == storage.StatusStopped })
	time.Sleep(200 * time.Millisecond)
	if st, _ := s.GetState("job"); st.Status != storage.StatusStopped || st.RestartCount != 0 {
		t.Errorf("a finished job should stay stopped without restarts, got %+v", st)
	}
}

func TestCrashRestartsWithBackoff(t *testing.T) {
	s := New(nil)
	s.restartBase = 40 * time.Millisecond
	svc := storage.Service{ID: "flaky", Command: "exit 3", RestartPolicy: storage.RestartOnFailure}
	_, _ = s.Start(context.Background(), svc)

	eventually(t, 5*time.Second, "several restarts", func() bool {
		st, _ := s.GetState("flaky")
		return st.RestartCount >= 3
	})
	if err := s.Stop("flaky", time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	before, _ := s.GetState("flaky")
	time.Sleep(300 * time.Millisecond)
	after, _ := s.GetState("flaky")
	if after.RestartCount != before.RestartCount || after.Status != storage.StatusStopped {
		t.Errorf("a stopped service must not keep restarting: %+v -> %+v", before, after)
	}
}

func TestRestartDelayGrowsAndCaps(t *testing.T) {
	s := New(nil)
	s.restartBase = time.Second
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := s.restartDelay(i + 1); got != w {
			t.Errorf("restartDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
}

// A relaunch that fails (here: the working directory vanished) must be reported and retried, not swallowed.
func TestFailedRelaunchIsLoggedAndRetried(t *testing.T) {
	s := New(nil)
	s.restartBase = 30 * time.Millisecond
	dir := t.TempDir()
	svc := storage.Service{ID: "gone", Cwd: dir, Command: "sleep 0.05; exit 1", RestartPolicy: storage.RestartAlways}
	_, _ = s.Start(context.Background(), svc)

	eventually(t, 5*time.Second, "first run to start", func() bool { return logText(s, "gone") != "" })
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	eventually(t, 5*time.Second, "relaunch failure to be logged", func() bool {
		return strings.Contains(logText(s, "gone"), "Restart failed")
	})
	_ = s.Stop("gone", time.Second)
}

func TestManualStartCancelsPendingRestart(t *testing.T) {
	s := New(nil)
	s.restartBase = 300 * time.Millisecond
	svc := storage.Service{ID: "manual", Command: "exit 1", RestartPolicy: storage.RestartAlways}
	_, _ = s.Start(context.Background(), svc)
	eventually(t, 2*time.Second, "crash", func() bool { return statusOf(s, "manual") == storage.StatusCrashed })

	svc.Command = "sleep 30"
	if _, err := s.Start(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // past the old timer
	st, _ := s.GetState("manual")
	if st.PID == 0 || st.Status == storage.StatusCrashed {
		t.Errorf("manual start should be running undisturbed, got %+v", st)
	}
	if strings.Count(logText(s, "manual"), "Process started") != 2 {
		t.Errorf("expected exactly two launches (crash + manual), log:\n%s", logText(s, "manual"))
	}
	_ = s.Stop("manual", time.Second)
}

// Hammer every entry point concurrently; meaningful under `go test -race`.
func TestConcurrentControlIsRaceFree(t *testing.T) {
	s := New(func(string, string, uint32) {})
	s.restartBase = 10 * time.Millisecond
	svc := storage.Service{ID: "hammer", Command: "sleep 0.05", RestartPolicy: storage.RestartAlways}

	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				switch (g + i) % 5 {
				case 0:
					_, _ = s.Start(context.Background(), svc)
				case 1:
					_ = s.Stop("hammer", 200*time.Millisecond)
				case 2:
					_, _ = s.Restart(context.Background(), "hammer")
				case 3:
					_, _ = s.GetState("hammer")
					_, _ = s.GetLogs("hammer", 10)
				default:
					if ch, unsub, err := s.FollowLogs("hammer"); err == nil {
						select {
						case <-ch:
						case <-time.After(5 * time.Millisecond):
						}
						unsub()
					}
				}
			}
		}(g)
	}
	wg.Wait()
	s.StopAll(time.Second)
	if got := statusOf(s, "hammer"); got == storage.StatusRunning || got == storage.StatusStarting {
		t.Errorf("StopAll left the process %s", got)
	}
}
