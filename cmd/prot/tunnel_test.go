package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/tunnels"
)

func TestParseTunnelAdd(t *testing.T) {
	cfg, _, err := parseTunnelAdd([]string{
		"db", "--host", "deploy@bastion.example", "--remote-port", "5432", "--remote-host", "10.0.0.5",
		"--local-port", "15432", "--identity", "/home/me/.ssh/id_ed25519", "--ssh-port", "2222",
		"--ssh-arg", "-o", "--ssh-arg", "Compression=yes", "--auto-connect",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := tunnels.TunnelConfig{
		Alias: "db", SSHHost: "bastion.example", SSHUser: "deploy", RemoteHost: "10.0.0.5", RemotePort: 5432,
		LocalPort: 15432, IdentityFile: "/home/me/.ssh/id_ed25519", AutoConnect: true,
		ExtraArgs: []string{"-p", "2222", "-o", "Compression=yes"},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestParseTunnelAddDefaultsAndUserFlag(t *testing.T) {
	cfg, _, err := parseTunnelAdd([]string{"web", "--host", "bastion", "--user", "alice", "--remote-port", "80"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSHUser != "alice" || cfg.SSHHost != "bastion" || cfg.AutoConnect || cfg.LocalPort != 0 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.ExtraArgs == nil {
		t.Error("ExtraArgs must be non-nil so it serialises as []")
	}

	// an explicit --user wins over user@ in --host
	cfg, _, _ = parseTunnelAdd([]string{"web", "--host", "bob@bastion", "--user", "alice", "--remote-port", "80"})
	if cfg.SSHUser != "alice" || cfg.SSHHost != "bastion" {
		t.Errorf("--user should win: %+v", cfg)
	}
}

func TestParseTunnelAddRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"no alias":         {},
		"flag as alias":    {"--host", "h", "--remote-port", "1"},
		"missing host":     {"db", "--remote-port", "1"},
		"missing port":     {"db", "--host", "h"},
		"port too big":     {"db", "--host", "h", "--remote-port", "70000"},
		"local port range": {"db", "--host", "h", "--remote-port", "1", "--local-port", "99999"},
		"unknown flag":     {"db", "--host", "h", "--remote-port", "1", "--bogus"},
		"non numeric port": {"db", "--host", "h", "--remote-port", "abc"},
	}
	for name, args := range cases {
		if _, _, err := parseTunnelAdd(args); err == nil {
			t.Errorf("%s: expected an error for %v", name, args)
		}
	}
}

// scriptedDaemon returns a fixed sequence of tunnel lists, one per call.
type scriptedDaemon struct {
	steps [][]registry.TunnelView
	calls int
	err   error
}

func (s *scriptedDaemon) listTunnels() ([]registry.TunnelView, error) {
	if s.err != nil {
		return nil, s.err
	}
	i := min(s.calls, len(s.steps)-1)
	s.calls++
	return s.steps[i], nil
}

func view(id string, state tunnels.State, lastErr string) registry.TunnelView {
	return registry.TunnelView{TunnelStatus: tunnels.TunnelStatus{
		Config: tunnels.TunnelConfig{ID: id, Alias: id}, State: state, LastError: lastErr,
	}}
}

func TestWaitForTunnelReturnsOnceConnected(t *testing.T) {
	d := &scriptedDaemon{steps: [][]registry.TunnelView{
		{view("t", tunnels.StateConnecting, "")},
		{view("t", tunnels.StateConnecting, "")},
		{view("t", tunnels.StateConnected, "")},
	}}
	got, err := waitForTunnel(d, "t", time.Second, time.Millisecond)
	if err != nil || got.State != tunnels.StateConnected || d.calls != 3 {
		t.Errorf("got %s after %d polls, err %v", got.State, d.calls, err)
	}
}

// A failed attempt carries ssh's reason, so there is no point waiting any longer.
func TestWaitForTunnelStopsAtFirstFailureWithReason(t *testing.T) {
	d := &scriptedDaemon{steps: [][]registry.TunnelView{
		{view("t", tunnels.StateConnecting, "")},
		{view("t", tunnels.StateReconnecting, "ssh exited: Permission denied (publickey).")},
		{view("t", tunnels.StateConnected, "")},
	}}
	got, err := waitForTunnel(d, "t", time.Second, time.Millisecond)
	if err != nil || got.State != tunnels.StateReconnecting || !strings.Contains(got.LastError, "Permission denied") {
		t.Errorf("got %+v, err %v", got, err)
	}
	if d.calls != 2 {
		t.Errorf("kept polling after the failure: %d calls", d.calls)
	}
}

func TestWaitForTunnelTimesOutAndReportsDaemonErrors(t *testing.T) {
	slow := &scriptedDaemon{steps: [][]registry.TunnelView{{view("t", tunnels.StateConnecting, "")}}}
	start := time.Now()
	got, err := waitForTunnel(slow, "t", 40*time.Millisecond, 5*time.Millisecond)
	if err != nil || got.State != tunnels.StateConnecting {
		t.Errorf("timeout: %+v, %v", got, err)
	}
	if time.Since(start) > time.Second {
		t.Error("did not honour the timeout")
	}

	broken := &scriptedDaemon{err: errors.New("daemon gone")}
	if _, err := waitForTunnel(broken, "t", time.Second, time.Millisecond); err == nil {
		t.Error("a daemon error must be returned")
	}
}

func TestTunnelPathEscapesAndTunnelStateBadge(t *testing.T) {
	if got := tunnelPath("a/b", "stop"); got != "/v1/tunnels/a%2Fb/stop" {
		t.Errorf("tunnelPath = %q", got)
	}
	for state, want := range map[tunnels.State]string{
		tunnels.StateConnected:    "● connected",
		tunnels.StateReconnecting: "↻ retrying",
		tunnels.StateDisconnected: "○ disconnected",
	} {
		if got := tunnelStateBadge(state); got != want {
			t.Errorf("badge(%s) = %q, want %q", state, got, want)
		}
	}
	if got := truncateText(strings.Repeat("x", 100), 20); len(got) != 20 || !strings.HasSuffix(got, "...") {
		t.Errorf("truncateText = %q", got)
	}
}

func TestParseTunnelAddPasswordSources(t *testing.T) {
	base := []string{"db", "--host", "h", "--remote-port", "1"}
	cases := []struct {
		extra []string
		want  passwordSource
		fail  bool
	}{
		{nil, passwordNone, false},
		{[]string{"--password"}, passwordPrompt, false},
		{[]string{"--password-stdin"}, passwordStdin, false},
		{[]string{"--password", "--password-stdin"}, passwordNone, true},
	}
	for _, c := range cases {
		_, source, err := parseTunnelAdd(append(append([]string{}, base...), c.extra...))
		if (err != nil) != c.fail || (!c.fail && source != c.want) {
			t.Errorf("%v: source %v, err %v", c.extra, source, err)
		}
	}
}

// The password must never be a flag: it would show up in ps output and shell history.
func TestPasswordIsNotAFlagValue(t *testing.T) {
	if _, _, err := parseTunnelAdd([]string{"db", "--host", "h", "--remote-port", "1", "--password=hunter2"}); err == nil {
		t.Error("--password=<value> must be rejected; it is a switch, not a value")
	}
}

func TestReadPasswordLine(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
		fail bool
	}{
		"plain":            {"s3cret\n", "s3cret", false},
		"crlf":             {"s3cret\r\n", "s3cret", false},
		"no final newline": {"s3cret", "s3cret", false},
		"spaces kept":      {"  two words  \n", "  two words  ", false},
		"only first line":  {"first\nsecond\n", "first", false},
		"empty line":       {"\n", "", true},
		"nothing":          {"", "", true},
	}
	for name, c := range cases {
		got, err := readPasswordLine(strings.NewReader(c.in))
		if (err != nil) != c.fail || got != c.want {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestNewTunnelRequestCarriesThePasswordOnlyWhenGiven(t *testing.T) {
	with, _ := json.Marshal(newTunnelRequest{TunnelConfig: tunnels.TunnelConfig{Alias: "db"}, Password: "pw"})
	without, _ := json.Marshal(newTunnelRequest{TunnelConfig: tunnels.TunnelConfig{Alias: "db"}})
	if !strings.Contains(string(with), `"password":"pw"`) || !strings.Contains(string(with), `"alias":"db"`) {
		t.Errorf("with password: %s", with)
	}
	if strings.Contains(string(without), "password") && !strings.Contains(string(without), "has_password") {
		t.Errorf("an unset password must be omitted: %s", without)
	}
	if strings.Contains(string(without), `"password"`) {
		t.Errorf("an unset password must be omitted: %s", without)
	}
}
