package tunnels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
)

func TestTunnelRegistrationAndLookup(t *testing.T) {
	mgr, err := NewManager()
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	cfg := TunnelConfig{
		ID:         "tun-1",
		Alias:      "remote-db",
		SSHHost:    "dev.example.com",
		RemoteHost: "127.0.0.1",
		RemotePort: 5432,
		LocalPort:  15432,
	}

	st, err := mgr.Register(cfg)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if st.State != StateDisconnected {
		t.Errorf("expected initial state disconnected, got %s", st.State)
	}

	lookup, ok := mgr.GetStatus("tun-1")
	if !ok {
		t.Fatalf("expected to find tun-1")
	}
	if lookup.Config.Alias != "remote-db" {
		t.Errorf("expected alias remote-db, got %s", lookup.Config.Alias)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Errorf("expected 1 tunnel in list, got %d", len(list))
	}
}

func TestTunnelInvalidConfig(t *testing.T) {
	mgr, err := NewManager()
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Missing ID
	_, err = mgr.Register(TunnelConfig{SSHHost: "host.com", RemotePort: 80})
	if err == nil {
		t.Errorf("expected error for missing ID")
	}

	// Missing SSHHost
	_, err = mgr.Register(TunnelConfig{ID: "t", RemotePort: 80})
	if err == nil {
		t.Errorf("expected error for missing SSHHost")
	}

	// Invalid RemotePort
	_, err = mgr.Register(TunnelConfig{ID: "t", SSHHost: "host.com", RemotePort: 0})
	if err == nil {
		t.Errorf("expected error for 0 RemotePort")
	}
}

func TestTunnelStop(t *testing.T) {
	mgr, err := NewManager()
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	cfg := TunnelConfig{
		ID:         "tun-stop",
		Alias:      "app",
		SSHHost:    "dummy.invalid",
		RemotePort: 8000,
	}

	_, _ = mgr.Register(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, _ = mgr.Start(ctx, "tun-stop")
	err = mgr.Stop("tun-stop")
	if err != nil {
		t.Errorf("stop failed: %v", err)
	}

	st, ok := mgr.GetStatus("tun-stop")
	if !ok || st.State != StateDisconnected {
		t.Errorf("expected state disconnected after stop, got %v", st.State)
	}
}

func registerFake(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	port, err := netutil.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := TunnelConfig{ID: id, SSHHost: "fake.invalid", RemotePort: 5432, LocalPort: port}
	if _, err := mgr.Register(cfg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestTunnelBecomesConnectedOnlyWhenPortOpens(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	registerFake(t, mgr, "ok")
	if _, err := mgr.Start(context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop("ok")

	st := waitState(t, mgr, "ok", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })
	if st.PID == 0 || st.ConnectedAt == nil {
		t.Errorf("connected tunnel should report its ssh pid and time: %+v", st)
	}
}

// Previously a tunnel whose forward never opened was still marked connected.
func TestTunnelWithoutOpenPortIsNotConnected(t *testing.T) {
	mgr := fakeSSHManager(t, "silent")
	registerFake(t, mgr, "silent")
	_, _ = mgr.Start(context.Background(), "silent")
	defer mgr.Stop("silent")

	st := waitState(t, mgr, "silent", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateReconnecting })
	if !strings.Contains(st.LastError, "did not open") {
		t.Errorf("LastError = %q, want the port timeout", st.LastError)
	}
	if st.ConnectedAt != nil {
		t.Error("never-connected tunnel must not have a ConnectedAt")
	}
}

func TestTunnelSSHExitingEarlyIsReportedAndRetried(t *testing.T) {
	mgr := fakeSSHManager(t, "exit")
	registerFake(t, mgr, "bad")
	_, _ = mgr.Start(context.Background(), "bad")
	defer mgr.Stop("bad")

	st := waitState(t, mgr, "bad", 5*time.Second, func(s TunnelStatus) bool { return s.Reconnections >= 2 })
	if !strings.Contains(st.LastError, "exited before the tunnel was ready") {
		t.Errorf("LastError = %q", st.LastError)
	}
	if st.State == StateConnected {
		t.Error("an ssh that exits immediately must never look connected")
	}
}

func TestStoppedTunnelStaysStopped(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	registerFake(t, mgr, "stop")
	_, _ = mgr.Start(context.Background(), "stop")
	waitState(t, mgr, "stop", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })

	if err := mgr.Stop("stop"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if st, _ := mgr.GetStatus("stop"); st.State != StateDisconnected || st.PID != 0 {
		t.Errorf("stopped tunnel came back: %+v", st)
	}
}

func TestRegisterRejectsOptionLikeHosts(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	for _, cfg := range []TunnelConfig{
		{ID: "a", SSHHost: "-oProxyCommand=touch /tmp/pwned", RemotePort: 1},
		{ID: "b", SSHHost: "host", SSHUser: "-x", RemotePort: 1},
	} {
		if _, err := mgr.Register(cfg); err == nil {
			t.Errorf("Register(%+v) should be rejected", cfg)
		}
	}
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestSSHErrorOutputIsKeptInLastError(t *testing.T) {
	mgr := fakeSSHManager(t, "denied")
	registerFake(t, mgr, "denied")
	_, _ = mgr.Start(context.Background(), "denied")
	defer mgr.Stop("denied")

	st := waitState(t, mgr, "denied", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateReconnecting })
	if !strings.Contains(st.LastError, "Permission denied (publickey)") {
		t.Errorf("LastError = %q, want ssh's own explanation", st.LastError)
	}
	if !strings.Contains(st.LastError, "exit status 255") {
		t.Errorf("LastError = %q, want the exit status too", st.LastError)
	}
}

func TestTailBufferKeepsTheEndAndOneLine(t *testing.T) {
	tail := &tailBuffer{}
	for i := 0; i < 500; i++ {
		fmt.Fprintf(tail, "noise line %d\n", i)
	}
	fmt.Fprint(tail, "first\x1b[31m real\nsecond\n\nthird\nfourth\n")

	got := tail.summary()
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("summary must be one clean line, got %q", got)
	}
	if !strings.HasSuffix(got, "second | third | fourth") || strings.Contains(got, "noise line 0") {
		t.Errorf("summary should be the last three non-empty lines, got %q", got)
	}
	if len(tail.buf) > tailLimit {
		t.Errorf("buffer grew to %d bytes", len(tail.buf))
	}
}

// ssh must never be able to prompt: the daemon has no terminal.
func TestSSHRunsNonInteractively(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("PROT_FAKE_SSH_ARGS", argsFile)
	mgr := fakeSSHManager(t, "listen")

	port, _ := netutil.FreePort()
	cfg := TunnelConfig{
		ID: "args", SSHHost: "fake.invalid", SSHUser: "me", RemotePort: 80, LocalPort: port,
		ExtraArgs: []string{"-o", "BatchMode=no"}, // a tunnel must not be able to turn prompting back on
	}
	if _, err := mgr.Register(cfg); err != nil {
		t.Fatal(err)
	}
	_, _ = mgr.Start(context.Background(), "args")
	defer mgr.Stop("args")
	waitState(t, mgr, "args", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(data), "\n")
	first := func(opt string) int {
		for i, a := range args {
			if a == opt {
				return i
			}
		}
		return -1
	}
	if first("BatchMode=yes") < 0 || first("ConnectTimeout=10") < 0 {
		t.Fatalf("missing non-interactive options in %v", args)
	}
	if first("BatchMode=yes") > first("BatchMode=no") {
		t.Errorf("BatchMode=yes must come before user-supplied options (ssh keeps the first value): %v", args)
	}
	if args[len(args)-1] != "me@fake.invalid" {
		t.Errorf("destination must be last: %v", args)
	}
}

// Stop returns only once the ssh process is really gone.
func TestStopWaitsForSSHToExit(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	registerFake(t, mgr, "wait")
	_, _ = mgr.Start(context.Background(), "wait")
	st := waitState(t, mgr, "wait", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })
	pid := st.PID

	if err := mgr.Stop("wait"); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Error("ssh still alive right after Stop returned")
	}
}

// The daemon calls StopAll on shutdown; nothing may be left running.
func TestStopAllLeavesNoProcesses(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	var pids []int
	for _, id := range []string{"a", "b", "c"} {
		registerFake(t, mgr, id)
		_, _ = mgr.Start(context.Background(), id)
	}
	for _, id := range []string{"a", "b", "c"} {
		st := waitState(t, mgr, id, 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })
		pids = append(pids, st.PID)
	}

	mgr.StopAll()
	for i, pid := range pids {
		if processAlive(pid) {
			t.Errorf("tunnel %d (pid %d) survived StopAll", i, pid)
		}
	}
	for _, st := range mgr.List() {
		if st.State != StateDisconnected {
			t.Errorf("%s is %s after StopAll", st.Config.ID, st.State)
		}
	}
}

func TestUnknownTunnelIsErrNotFound(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	if _, err := mgr.Start(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Start: %v", err)
	}
	if err := mgr.Stop("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stop: %v", err)
	}
	if err := mgr.Remove("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove: %v", err)
	}
}

func TestRemoveStopsAndForgets(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	registerFake(t, mgr, "gone")
	_, _ = mgr.Start(context.Background(), "gone")
	st := waitState(t, mgr, "gone", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })

	if err := mgr.Remove("gone"); err != nil {
		t.Fatal(err)
	}
	if processAlive(st.PID) {
		t.Error("ssh survived Remove")
	}
	if _, ok := mgr.GetStatus("gone"); ok {
		t.Error("removed tunnel still listed")
	}
	registerFake(t, mgr, "gone") // the id is free again
}

// Starting a tunnel that is retrying must not spawn a second supervise loop.
func TestStartWhileReconnectingDoesNotDuplicate(t *testing.T) {
	mgr := fakeSSHManager(t, "exit")
	registerFake(t, mgr, "retry")
	_, _ = mgr.Start(context.Background(), "retry")
	defer mgr.Stop("retry")
	waitState(t, mgr, "retry", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateReconnecting })

	st, err := mgr.Start(context.Background(), "retry")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateReconnecting {
		t.Errorf("a second Start reset the state to %s, which means it started another loop", st.State)
	}
}

func TestObserverSeesConnectAndDisconnect(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	var mu sync.Mutex
	var seen []State
	mgr.SetObserver(func(st TunnelStatus) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, st.State)
		if st.State == StateConnected && st.AssignedLocalPort == 0 {
			t.Error("connected status must carry the local port")
		}
	})
	registerFake(t, mgr, "obs")
	_, _ = mgr.Start(context.Background(), "obs")
	waitState(t, mgr, "obs", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })
	_ = mgr.Stop("obs")

	mu.Lock()
	defer mu.Unlock()
	joined := fmt.Sprint(seen)
	if !strings.Contains(joined, "connecting") || !strings.Contains(joined, "connected") ||
		seen[len(seen)-1] != StateDisconnected {
		t.Errorf("observer saw %v, want connecting, connected, ..., disconnected", seen)
	}
}

// Clients poll the list; Go map order would make the tunnels jump around on every refresh.
func TestListIsSortedAndStable(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	for _, alias := range []string{"zeta", "alpha", "mid", "beta", "omega", "gamma"} {
		port, _ := netutil.FreePort()
		cfg := TunnelConfig{ID: "id-" + alias, Alias: alias, SSHHost: "h", RemotePort: 1, LocalPort: port}
		if _, err := mgr.Register(cfg); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"alpha", "beta", "gamma", "mid", "omega", "zeta"}
	for round := 0; round < 25; round++ {
		var got []string
		for _, st := range mgr.List() {
			got = append(got, st.Config.Alias)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("round %d: order %v, want %v", round, got, want)
		}
	}
}
