package mcp

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// The test binary doubles as a fake ssh. PROT_FAKE_SSH=listen opens the -L local port and stays up, like an ssh
// that authenticated; =denied fails the way ssh does when the host is not set up for key login.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PROT_FAKE_SSH"); mode != "" {
		os.Exit(fakeSSH(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeSSH(mode string, args []string) int {
	if path := os.Getenv("PROT_FAKE_SSH_ARGS"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(args, "\n")), 0o600)
	}
	if mode == "denied" {
		fmt.Fprintln(os.Stderr, "deploy@bastion: Permission denied (publickey).")
		return 255
	}
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
				_ = conn.Close()
			}
		}()
		time.Sleep(time.Minute)
	}
	return 0
}

type tunnelTestEnv struct {
	srv *Server
	reg *registry.Registry
	db  *storage.DB
	rt  *proxy.Router
}

func setupTunnelMCP(t *testing.T, sshMode string) *tunnelTestEnv {
	t.Helper()
	t.Setenv("PROT_FAKE_SSH", sshMode)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rt := proxy.NewRouter()
	reg := registry.New(registry.Config{
		DB: db, Router: rt, Tunnels: tunnels.NewManagerWithSSH(self), GatewayHost: "8080",
	})
	t.Cleanup(reg.StopAllTunnels)
	return &tunnelTestEnv{srv: NewServer(reg), reg: reg, db: db, rt: rt}
}

func TestAddTunnelConnectsAndKeepsAWorkingTunnel(t *testing.T) {
	env := setupTunnelMCP(t, "listen")
	argsFile := filepath.Join(t.TempDir(), "ssh-args")
	t.Setenv("PROT_FAKE_SSH_ARGS", argsFile)

	text, isErr := callTool(t, env.srv, "add_tunnel",
		`{"alias": "staging-db", "host": "deploy@bastion.example.com", "remote_port": 5432}`)
	if isErr {
		t.Fatalf("add_tunnel: %s", text)
	}
	if !strings.Contains(text, "connected") || !strings.Contains(text, "staging-db.localhost") {
		t.Errorf("result should confirm the connection and the address, got %q", text)
	}

	views := env.reg.ListTunnels()
	if len(views) != 1 {
		t.Fatalf("want 1 tunnel, got %d", len(views))
	}
	cfg := views[0].Config
	if cfg.SSHHost != "bastion.example.com" || cfg.SSHUser != "deploy" || cfg.RemotePort != 5432 ||
		cfg.RemoteHost != "127.0.0.1" || !cfg.AutoConnect || cfg.HasPassword {
		t.Errorf("unexpected tunnel config: %+v", cfg)
	}
	if views[0].State != tunnels.StateConnected {
		t.Errorf("tunnel should be connected, got %s", views[0].State)
	}
	if _, ok := env.rt.Match("staging-db.localhost"); !ok {
		t.Error("the .localhost route should exist")
	}
	if saved, err := env.db.ListTunnels(context.Background()); err != nil || len(saved) != 1 {
		t.Errorf("tunnel should be persisted, got %d (err=%v)", len(saved), err)
	}

	// ssh must never be allowed to ask a question: BatchMode is what makes an unprepared host fail.
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "BatchMode=yes") {
		t.Errorf("ssh must run in BatchMode, args:\n%s", data)
	}
}

func TestAddTunnelFailsAndSavesNothingWhenTheHostIsNotSetUp(t *testing.T) {
	env := setupTunnelMCP(t, "denied")

	text, isErr := callTool(t, env.srv, "add_tunnel",
		`{"alias": "nope", "host": "bastion.example.com", "remote_port": 22}`)
	if !isErr {
		t.Fatalf("add_tunnel should fail, got %q", text)
	}
	for _, want := range []string{"Permission denied", "Nothing was saved", "ssh-copy-id"} {
		if !strings.Contains(text, want) {
			t.Errorf("error should mention %q, got %q", want, text)
		}
	}

	if left := env.reg.ListTunnels(); len(left) != 0 {
		t.Errorf("a failed tunnel must not stay registered: %+v", left)
	}
	if saved, _ := env.db.ListTunnels(context.Background()); len(saved) != 0 {
		t.Errorf("a failed tunnel must not stay in the database: %+v", saved)
	}
	if _, ok := env.rt.Match("nope.localhost"); ok {
		t.Error("a failed tunnel must not leave a route behind")
	}

	// The alias is free again, so the agent can retry once the user has set the host up.
	t.Setenv("PROT_FAKE_SSH", "listen")
	if text, isErr := callTool(t, env.srv, "add_tunnel",
		`{"alias": "nope", "host": "bastion.example.com", "remote_port": 22}`); isErr {
		t.Errorf("retry after the host is set up should work: %s", text)
	}
}

func TestAddTunnelRejectsBadInput(t *testing.T) {
	env := setupTunnelMCP(t, "listen")

	cases := map[string]struct{ args, want string }{
		"no port":          {`{"alias": "a", "host": "h"}`, "remote_port"},
		"zero port":        {`{"alias": "a", "host": "h", "remote_port": 0}`, "remote_port"},
		"port range":       {`{"alias": "a", "host": "h", "remote_port": 70000}`, "remote_port"},
		"no host":          {`{"alias": "a", "remote_port": 1}`, "alias and host"},
		"bad alias":        {`{"alias": "Bad_Alias", "host": "h", "remote_port": 1}`, "alias must be"},
		"option injection": {`{"alias": "a", "host": "-oProxyCommand=touch /tmp/pwned", "remote_port": 1}`, "must not start with '-'"},
		"user injection":   {`{"alias": "a", "host": "h", "user": "-oProxyCommand=x", "remote_port": 1}`, "must not start with '-'"},
		"wrong type":       {`{"alias": "a", "host": "h", "remote_port": "22"}`, "remote_port"},
	}
	for name, tc := range cases {
		text, isErr := callTool(t, env.srv, "add_tunnel", tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s: want error containing %q, got %q (err=%v)", name, tc.want, text, isErr)
		}
	}
	if left := env.reg.ListTunnels(); len(left) != 0 {
		t.Errorf("rejected input must not create tunnels: %+v", left)
	}
}

func TestAddTunnelRefusesATakenAlias(t *testing.T) {
	env := setupTunnelMCP(t, "listen")
	if text, isErr := callTool(t, env.srv, "add_service", `{"name": "Web", "cmd": "true"}`); isErr {
		t.Fatal(text)
	}
	text, isErr := callTool(t, env.srv, "add_tunnel", `{"alias": "web", "host": "h", "remote_port": 1}`)
	if !isErr || !strings.Contains(text, "already used") {
		t.Errorf("an alias used by a service must be refused, got %q (err=%v)", text, isErr)
	}
}

func TestAddTunnelIsAttributedToMCP(t *testing.T) {
	env := setupTunnelMCP(t, "listen")
	if text, isErr := callTool(t, env.srv, "add_tunnel",
		`{"alias": "db", "host": "bastion", "remote_port": 5432}`); isErr {
		t.Fatal(text)
	}
	events, err := env.db.GetRecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type == "tunnel.registered" {
			if !strings.Contains(e.Payload, `"source":"mcp"`) || !strings.Contains(e.Payload, `"host":"bastion"`) {
				t.Errorf("event should record the source and host, got %q", e.Payload)
			}
			return
		}
	}
	t.Error("no tunnel.registered event was logged")
}

func writeSSHConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := "Host staging prod-db\n  HostName db.example.com\n  User deploy\n  Port 2222\n\nHost *.internal\n  User ignored\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListSSHHostsNamesTheConfiguredMachines(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()
	cfg := writeSSHConfig(t)
	srv.SetSSH(cfg, "")

	text, isErr := callTool(t, srv, "list_ssh_hosts", `{}`)
	if isErr {
		t.Fatalf("list_ssh_hosts: %s", text)
	}
	for _, want := range []string{`"staging"`, `"prod-db"`, `"count": 2`, cfg} {
		if !strings.Contains(text, want) {
			t.Errorf("result should contain %s, got %s", want, text)
		}
	}
	if strings.Contains(text, "internal") {
		t.Errorf("wildcard patterns are not machines: %s", text)
	}
}

func TestListSSHHostsWithoutAConfigFileIsEmptyNotAnError(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()
	srv.SetSSH(filepath.Join(t.TempDir(), "missing"), "")

	text, isErr := callTool(t, srv, "list_ssh_hosts", `{}`)
	if isErr || !strings.Contains(text, `"count": 0`) {
		t.Errorf("a missing config means no machines, got %q (err=%v)", text, isErr)
	}
}

func TestListSSHHostsResolvesOneMachine(t *testing.T) {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh binary")
	}
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()
	srv.SetSSH(writeSSHConfig(t), sshBin)

	text, isErr := callTool(t, srv, "list_ssh_hosts", `{"host": "staging"}`)
	if isErr {
		t.Fatalf("resolve: %s", text)
	}
	for _, want := range []string{`"hostname": "db.example.com"`, `"user": "deploy"`, `"port": 2222`} {
		if !strings.Contains(text, want) {
			t.Errorf("resolved host should contain %s, got %s", want, text)
		}
	}

	// A name the configuration does not define must never reach ssh.
	text, isErr = callTool(t, srv, "list_ssh_hosts", `{"host": "-oProxyCommand=evil"}`)
	if !isErr || !strings.Contains(text, "not defined") {
		t.Errorf("an unlisted name should be refused, got %q (err=%v)", text, isErr)
	}
}

func TestListSSHHostsResolveNeedsSSH(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()
	srv.SetSSH(writeSSHConfig(t), "")

	if text, isErr := callTool(t, srv, "list_ssh_hosts", `{"host": "staging"}`); !isErr ||
		!strings.Contains(text, "ssh is not installed") {
		t.Errorf("resolving without ssh should say so, got %q (err=%v)", text, isErr)
	}
}
