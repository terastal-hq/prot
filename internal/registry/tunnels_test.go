package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// The test binary doubles as a fake ssh that opens the -L local port and stays up.
func TestMain(m *testing.M) {
	if os.Getenv("PROT_FAKE_SSH") != "" {
		os.Exit(fakeSSH(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeSSH(args []string) int {
	for i, arg := range args {
		if arg != "-L" || i+1 >= len(args) {
			continue
		}
		parts := strings.Split(args[i+1], ":") // 127.0.0.1:<local>:<host>:<port>
		ln, err := net.Listen("tcp", net.JoinHostPort(parts[0], parts[1]))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 255
		}
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
		time.Sleep(time.Minute)
	}
	return 0
}

type tunnelEnv struct {
	reg    *Registry
	db     *storage.DB
	router *proxy.Router
	mgr    *tunnels.Manager
}

func newTunnelEnv(t *testing.T, db *storage.DB) *tunnelEnv {
	t.Helper()
	t.Setenv("PROT_FAKE_SSH", "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if db == nil {
		db, err = storage.Open(filepath.Join(t.TempDir(), "prot.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
	}
	env := &tunnelEnv{db: db, router: proxy.NewRouter(), mgr: tunnels.NewManagerWithSSH(self)}
	env.reg = New(Config{DB: db, Router: env.router, Tunnels: env.mgr, GatewayHost: "8080"})
	t.Cleanup(env.reg.StopAllTunnels)
	return env
}

func tunnelCfg(t *testing.T, alias string, auto bool) tunnels.TunnelConfig {
	t.Helper()
	port, err := netutil.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	return tunnels.TunnelConfig{
		Alias: alias, SSHHost: "fake.invalid", RemotePort: 5432, LocalPort: port, AutoConnect: auto,
	}
}

func waitTunnel(t *testing.T, env *tunnelEnv, id string, want tunnels.State) TunnelView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last TunnelView
	for time.Now().Before(deadline) {
		for _, v := range env.reg.ListTunnels() {
			if v.Config.ID == id {
				last = v
				if v.State == want {
					return v
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tunnel %s never reached %s; last: %+v", id, want, last.TunnelStatus)
	return last
}

func TestRegisterTunnelPersistsAndRoutes(t *testing.T) {
	env := newTunnelEnv(t, nil)
	cfg := tunnelCfg(t, "  Remote-DB ", false)

	view, err := env.reg.RegisterTunnel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RegisterTunnel: %v", err)
	}
	if view.Config.Alias != "remote-db" || !strings.HasPrefix(view.Config.ID, "tun_") {
		t.Errorf("alias should be normalised and an id generated: %+v", view.Config)
	}
	if view.CanonicalURL != "http://remote-db.localhost:8080" {
		t.Errorf("CanonicalURL = %q", view.CanonicalURL)
	}

	saved, err := env.db.ListTunnels(context.Background())
	if err != nil || len(saved) != 1 || saved[0].Alias != "remote-db" || saved[0].RemotePort != 5432 {
		t.Errorf("tunnel not persisted: %+v, %v", saved, err)
	}

	rt, ok := env.router.GetRoute("remote-db")
	if !ok || rt.TargetURL != netutil.LoopbackURL(cfg.LocalPort) {
		t.Errorf("route = %+v (ok=%v), want the configured local port", rt, ok)
	}
}

func TestRegisterTunnelValidatesAndRejectsDuplicateAliases(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()

	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "Not Valid!", false)); !errors.Is(err, ErrInvalidAlias) {
		t.Errorf("invalid alias: %v", err)
	}
	bad := tunnelCfg(t, "ok", false)
	bad.SSHHost = "-oProxyCommand=evil"
	if _, err := env.reg.RegisterTunnel(ctx, bad); !errors.Is(err, ErrInvalidField) {
		t.Errorf("option-like host: %v", err)
	}

	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "db", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "db", false)); !errors.Is(err, ErrAliasTaken) {
		t.Errorf("second tunnel with the same alias: %v", err)
	}

	web := storage.Service{ID: "svc-web", Name: "Web", Alias: "web", Port: 3000}
	if _, err := env.reg.RegisterService(ctx, web); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "web", false)); !errors.Is(err, ErrAliasTaken) {
		t.Errorf("tunnel alias equal to a service alias: %v", err)
	}
	// ...and a service cannot be renamed onto a tunnel's alias.
	if _, err := env.reg.RenameAlias(ctx, "svc-web", "db"); !errors.Is(err, ErrAliasTaken) {
		t.Errorf("service renamed onto a tunnel alias: %v", err)
	}

	if n := len(env.reg.ListTunnels()); n != 1 {
		t.Errorf("failed registrations must not leave tunnels behind, have %d", n)
	}
}

// With no fixed local port the route has to follow whatever port the connection was given.
func TestConnectedTunnelRouteFollowsAssignedPort(t *testing.T) {
	env := newTunnelEnv(t, nil)
	cfg := tunnelCfg(t, "auto", true)
	cfg.LocalPort = 0

	view, err := env.reg.RegisterTunnel(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	connected := waitTunnel(t, env, view.Config.ID, tunnels.StateConnected)
	if connected.AssignedLocalPort == 0 {
		t.Fatal("connected tunnel has no local port")
	}
	rt, ok := env.router.GetRoute("auto")
	if !ok || rt.TargetURL != netutil.LoopbackURL(connected.AssignedLocalPort) {
		t.Errorf("route = %+v (ok=%v), want port %d", rt, ok, connected.AssignedLocalPort)
	}
}

func TestStartStopByAlias(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	view, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "byalias", false))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := env.reg.StartTunnel(ctx, "byalias"); err != nil {
		t.Fatalf("start by alias: %v", err)
	}
	waitTunnel(t, env, view.Config.ID, tunnels.StateConnected)

	stopped, err := env.reg.StopTunnel(view.Config.ID)
	if err != nil || stopped.State != tunnels.StateDisconnected {
		t.Errorf("stop by id: %+v, %v", stopped, err)
	}

	for _, op := range []func() error{
		func() error { _, err := env.reg.StartTunnel(ctx, "ghost"); return err },
		func() error { _, err := env.reg.StopTunnel("ghost"); return err },
		func() error { return env.reg.DeleteTunnel(ctx, "ghost") },
		func() error { _, err := env.reg.StartTunnel(ctx, ""); return err },
	} {
		if err := op(); !errors.Is(err, tunnels.ErrNotFound) {
			t.Errorf("unknown tunnel should be ErrNotFound, got %v", err)
		}
	}
}

func TestDeleteTunnelRemovesEverything(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	view, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "temp", true))
	if err != nil {
		t.Fatal(err)
	}
	connected := waitTunnel(t, env, view.Config.ID, tunnels.StateConnected)

	if err := env.reg.DeleteTunnel(ctx, "temp"); err != nil {
		t.Fatalf("DeleteTunnel: %v", err)
	}
	if _, ok := env.router.GetRoute("temp"); ok {
		t.Error("route survived delete")
	}
	if saved, _ := env.db.ListTunnels(ctx); len(saved) != 0 {
		t.Errorf("tunnel still persisted: %+v", saved)
	}
	if len(env.reg.ListTunnels()) != 0 {
		t.Error("tunnel still listed")
	}
	if processExists(connected.PID) {
		t.Error("ssh survived delete")
	}
	// the alias is free again
	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "temp", false)); err != nil {
		t.Errorf("alias not reusable: %v", err)
	}
}

// A restarted daemon must bring saved tunnels back, reconnecting only the auto_connect ones.
func TestRestoreTunnelsAfterRestart(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	first := newTunnelEnv(t, db)
	auto, err := first.reg.RegisterTunnel(ctx, tunnelCfg(t, "auto-one", true))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := first.reg.RegisterTunnel(ctx, tunnelCfg(t, "manual-one", false))
	if err != nil {
		t.Fatal(err)
	}
	waitTunnel(t, first, auto.Config.ID, tunnels.StateConnected)
	first.reg.StopAllTunnels() // daemon shutting down

	second := newTunnelEnv(t, db) // fresh manager, router and registry on the same database
	if len(second.reg.ListTunnels()) != 0 {
		t.Fatal("a new manager should start empty")
	}
	second.reg.RestoreTunnels(ctx)

	if n := len(second.reg.ListTunnels()); n != 2 {
		t.Fatalf("restored %d tunnels, want 2", n)
	}
	waitTunnel(t, second, auto.Config.ID, tunnels.StateConnected)
	for _, v := range second.reg.ListTunnels() {
		if v.Config.ID == manual.Config.ID && v.State != tunnels.StateDisconnected {
			t.Errorf("manual tunnel was connected on restore: %s", v.State)
		}
	}
	for _, alias := range []string{"auto-one", "manual-one"} {
		if _, ok := second.router.GetRoute(alias); !ok {
			t.Errorf("route for %q not restored", alias)
		}
	}
}

func TestTunnelsUnavailableWithoutSSH(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := New(Config{DB: db, Router: proxy.NewRouter(), GatewayHost: "8080"}) // no tunnel manager

	if reg.TunnelsAvailable() {
		t.Error("TunnelsAvailable should be false")
	}
	_, err = reg.RegisterTunnel(context.Background(), tunnels.TunnelConfig{Alias: "x"})
	if !errors.Is(err, ErrTunnelsUnavailable) {
		t.Errorf("RegisterTunnel: %v", err)
	}
	if _, err := reg.StartTunnel(context.Background(), "x"); !errors.Is(err, ErrTunnelsUnavailable) {
		t.Errorf("StartTunnel: %v", err)
	}
	if got := reg.ListTunnels(); got == nil || len(got) != 0 {
		t.Errorf("ListTunnels must be an empty, non-nil slice, got %#v", got)
	}
	reg.StopAllTunnels() // must not panic
}

func TestTunnelEventsAreBroadcast(t *testing.T) {
	env := newTunnelEnv(t, nil)
	events, unsub := env.reg.Subscribe()
	defer unsub()

	view, err := env.reg.RegisterTunnel(context.Background(), tunnelCfg(t, "evt", true))
	if err != nil {
		t.Fatal(err)
	}
	waitTunnel(t, env, view.Config.ID, tunnels.StateConnected)

	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for !(seen["tunnel.registered"] && seen["tunnel.status"]) {
		select {
		case ev := <-events:
			seen[ev.Type] = true
		case <-deadline:
			t.Fatalf("events seen: %v", seen)
		}
	}
}

func TestWaitForTunnelConnectedAndTimeout(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	view, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "wait", false))
	if err != nil {
		t.Fatal(err)
	}

	// not started: nothing to wait for, so it returns at the timeout with the disconnected status
	got, err := env.reg.WaitForTunnel(ctx, "wait", 150*time.Millisecond)
	if err != nil || got.State != tunnels.StateDisconnected {
		t.Errorf("idle tunnel: %+v, %v", got, err)
	}

	if _, err := env.reg.StartTunnel(ctx, view.Config.ID); err != nil {
		t.Fatal(err)
	}
	got, err = env.reg.WaitForTunnel(ctx, "wait", 5*time.Second)
	if err != nil || got.State != tunnels.StateConnected {
		t.Errorf("start then wait: %+v, %v", got, err)
	}

	if _, err := env.reg.WaitForTunnel(ctx, "ghost", time.Second); !errors.Is(err, tunnels.ErrNotFound) {
		t.Errorf("unknown tunnel: %v", err)
	}
}

// A refused connection should come back with ssh's reason instead of waiting out the whole timeout.
func TestWaitForTunnelReturnsEarlyWithTheFailureReason(t *testing.T) {
	ssh := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\necho 'user@host: Permission denied (publickey).' >&2\nexit 255\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := New(Config{DB: db, Router: proxy.NewRouter(), Tunnels: tunnels.NewManagerWithSSH(ssh), GatewayHost: "8080"})
	defer reg.StopAllTunnels()

	ctx := context.Background()
	cfg := tunnels.TunnelConfig{Alias: "denied", SSHHost: "h", RemotePort: 1, AutoConnect: true}
	if _, err := reg.RegisterTunnel(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	got, err := reg.WaitForTunnel(ctx, "denied", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.LastError, "Permission denied (publickey)") {
		t.Errorf("LastError = %q", got.LastError)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("waited %v even though the failure was immediate", time.Since(start))
	}
}
