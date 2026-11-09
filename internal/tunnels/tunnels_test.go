package tunnels

import (
	"context"
	"testing"
	"time"
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
