package tunnels

import (
	"context"
	"strings"
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
