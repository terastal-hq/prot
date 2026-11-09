package daemon

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/tunnels"
)

// fakeSSHScript is an "ssh" that just stays alive, so the tunnel is supervised (and retried) without any network.
func fakeSSHScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func startTestDaemon(t *testing.T, dataDir, sshPath string) *Daemon {
	t.Helper()
	d, err := New(Config{DataDir: dataDir, GatewayAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0", SSHPath: sshPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return d
}

func tunnelPID(t *testing.T, d *Daemon, id string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range d.reg.ListTunnels() {
			if v.Config.ID == id && v.PID > 0 {
				return v.PID
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tunnel %s never started an ssh process", id)
	return 0
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Shutting the daemon down used to leave every ssh -L process running (reparented to init).
func TestDaemonStopLeavesNoSSHProcesses(t *testing.T) {
	d := startTestDaemon(t, t.TempDir(), fakeSSHScript(t))

	cfg := tunnels.TunnelConfig{Alias: "db", SSHHost: "fake.invalid", RemotePort: 5432, AutoConnect: true}
	view, err := d.reg.RegisterTunnel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RegisterTunnel: %v", err)
	}
	pid := tunnelPID(t, d, view.Config.ID)

	if err := d.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if alive(pid) {
		t.Errorf("ssh process %d is still running after the daemon stopped", pid)
	}
}

// Saved tunnels come back after a restart, and auto_connect ones reconnect.
func TestTunnelsSurviveDaemonRestart(t *testing.T) {
	dataDir := t.TempDir()
	ssh := fakeSSHScript(t)

	first := startTestDaemon(t, dataDir, ssh)
	auto := tunnels.TunnelConfig{
		Alias: "auto", SSHHost: "fake.invalid", RemotePort: 80, LocalPort: 39001, AutoConnect: true,
	}
	manual := tunnels.TunnelConfig{Alias: "manual", SSHHost: "fake.invalid", RemotePort: 81, LocalPort: 39002}
	autoView, err := first.reg.RegisterTunnel(context.Background(), auto)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.reg.RegisterTunnel(context.Background(), manual); err != nil {
		t.Fatal(err)
	}
	tunnelPID(t, first, autoView.Config.ID)
	if err := first.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := startTestDaemon(t, dataDir, ssh)
	defer second.Stop(context.Background())

	views := second.reg.ListTunnels()
	if len(views) != 2 {
		t.Fatalf("restored %d tunnels, want 2: %+v", len(views), views)
	}
	if pid := tunnelPID(t, second, autoView.Config.ID); pid == 0 {
		t.Error("the auto_connect tunnel did not reconnect")
	}
	for _, v := range views {
		if v.Config.Alias == "manual" && v.PID != 0 {
			t.Error("a manual tunnel must stay disconnected after a restart")
		}
	}
	if _, ok := second.router.GetRoute("auto"); !ok {
		t.Error("route for the restored tunnel is missing")
	}
}

func TestDaemonWithoutSSHKeepsRunning(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH, so no ssh
	d := startTestDaemon(t, t.TempDir(), "")
	defer d.Stop(context.Background())

	if d.reg.TunnelsAvailable() {
		t.Error("tunnels should be unavailable without ssh")
	}
}
