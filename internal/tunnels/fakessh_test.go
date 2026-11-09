package tunnels

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/askpass"
)

// The test binary doubles as a fake ssh: when PROT_FAKE_SSH is set, TestMain runs fakeSSH instead of the tests.
// Modes: "denied" fails like a refused key, "listen" opens the -L local port and stays up, "exit" fails
// immediately, "silent" stays up without ever opening the port.
func TestMain(m *testing.M) {
	// Fake ssh mode wins: the fake inherits the askpass variables, so it would otherwise mistake itself for the
	// helper.
	if mode := os.Getenv("PROT_FAKE_SSH"); mode != "" {
		os.Exit(fakeSSH(mode, os.Args[1:]))
	}
	// In the real-OpenSSH tests ssh runs this binary as its askpass helper (see package askpass).
	if askpass.Requested(os.Getenv) {
		os.Exit(askpass.Run(os.Args[1:], os.Getenv, os.Stdout))
	}
	os.Exit(m.Run())
}

func fakeSSH(mode string, args []string) int {
	if path := os.Getenv("PROT_FAKE_SSH_ARGS"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(args, "\n")), 0o600)
	}
	if path := os.Getenv("PROT_FAKE_SSH_ENV"); path != "" {
		var seen []string
		for _, key := range []string{"SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "PROT_ASKPASS", "PROT_ASKPASS_SECRET"} {
			if v, ok := os.LookupEnv(key); ok {
				seen = append(seen, key+"="+v)
			}
		}
		_ = os.WriteFile(path, []byte(strings.Join(seen, "\n")), 0o600)
	}
	switch mode {
	case "denied":
		fmt.Fprintln(os.Stderr, "Warning: Permanently added host")
		fmt.Fprintln(os.Stderr, "user@host: Permission denied (publickey).")
		return 255
	case "exit":
		fmt.Fprintln(os.Stderr, "ssh: Could not resolve hostname")
		return 255
	case "silent":
		time.Sleep(time.Minute)
		return 0
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
				conn.Close()
			}
		}()
		time.Sleep(time.Minute)
	}
	return 0
}

func fakeSSHManager(t *testing.T, mode string) *Manager {
	t.Helper()
	t.Setenv("PROT_FAKE_SSH", mode)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mgr := newManager(self)
	mgr.readyTimeout = 400 * time.Millisecond
	return mgr
}

func waitState(
	t *testing.T,
	mgr *Manager,
	id string,
	timeout time.Duration,
	want func(TunnelStatus) bool,
) TunnelStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last TunnelStatus
	for time.Now().Before(deadline) {
		if st, ok := mgr.GetStatus(id); ok {
			last = *st
			if want(last) {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out; last status: %+v", last)
	return last
}
