package tunnels

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
)

// These tests drive real OpenSSH: a throwaway sshd on a loopback port and a key protected by a passphrase. A login
// password cannot be checked without root (sshd needs it to read /etc/shadow), but ssh asks for a key passphrase
// through exactly the same askpass mechanism, so this exercises the whole path. They skip when sshd is missing.

const keyPassphrase = "correct horse battery staple"

type testSSHD struct {
	port       uint32
	user       string
	sshPath    string
	identity   string
	knownHosts string // known_hosts file that trusts the server
	emptyHosts string // known_hosts file that trusts nothing
}

func startSSHD(t *testing.T) *testSSHD {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		if _, statErr := os.Stat("/usr/sbin/sshd"); statErr != nil {
			t.Skip("sshd not installed")
		}
		sshd = "/usr/sbin/sshd"
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh not installed")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not installed")
	}
	current, err := user.Current()
	if err != nil {
		t.Skip("cannot determine the current user")
	}

	dir := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run(keygen, "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, "host"))
	run(keygen, "-q", "-t", "ed25519", "-N", keyPassphrase, "-f", filepath.Join(dir, "client"))

	clientPub, _ := os.ReadFile(filepath.Join(dir, "client.pub"))
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), clientPub, 0o600); err != nil {
		t.Fatal(err)
	}

	port, err := netutil.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s/host
AuthorizedKeysFile %s/authorized_keys
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
PidFile %s/sshd.pid
LogLevel ERROR
`, port, dir, dir, dir)
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(sshd, "-D", "-f", cfgPath)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sshd as this user: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })

	if !netutil.WaitForPort(context.Background(), port, 5*time.Second) {
		t.Skip("sshd did not start (it may need privileges on this machine)")
	}

	hostPub, _ := os.ReadFile(filepath.Join(dir, "host.pub"))
	fields := strings.Fields(string(hostPub)) // type, base64, comment
	known := filepath.Join(dir, "known_hosts")
	line := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, fields[0], fields[1])
	if err := os.WriteFile(known, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "known_hosts_empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Nothing from the developer's own environment may authenticate or prompt.
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("PROT_FAKE_SSH", "")
	t.Setenv("SSH_ASKPASS", "")

	return &testSSHD{
		port: port, user: current.Username, sshPath: sshPath, identity: filepath.Join(dir, "client"),
		knownHosts: known, emptyHosts: empty,
	}
}

// remoteHTTP is the service the tunnel forwards to.
func remoteHTTP(t *testing.T) uint32 {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("reached the far side"))
	}))
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	return uint32(port)
}

func (s *testSSHD) tunnel(t *testing.T, id, hosts string, hasPassword bool) TunnelConfig {
	t.Helper()
	local, err := netutil.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	return TunnelConfig{
		ID: id, SSHHost: "127.0.0.1", SSHUser: s.user, RemotePort: remoteHTTP(t), LocalPort: local,
		IdentityFile: s.identity, HasPassword: hasPassword,
		ExtraArgs: []string{
			"-p", strconv.Itoa(int(s.port)), "-o", "UserKnownHostsFile=" + hosts,
			"-o", "GlobalKnownHostsFile=/dev/null", "-o", "IdentitiesOnly=yes",
		},
	}
}

func (s *testSSHD) manager(secret string) *Manager {
	mgr := NewManagerWithSSH(s.sshPath)
	mgr.readyTimeout = 20 * time.Second
	mgr.SetSecretProvider(func(string) (string, error) { return secret, nil })
	return mgr // the askpass helper defaults to this test binary, which TestMain turns into the helper
}

func startAndWait(t *testing.T, mgr *Manager, cfg TunnelConfig, want func(TunnelStatus) bool) TunnelStatus {
	t.Helper()
	if _, err := mgr.Register(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), cfg.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop(cfg.ID) })
	return waitState(t, mgr, cfg.ID, 25*time.Second, want)
}

func TestRealSSHStoredPassphraseConnectsAndForwards(t *testing.T) {
	sshd := startSSHD(t)
	mgr := sshd.manager(keyPassphrase)
	cfg := sshd.tunnel(t, "ok", sshd.knownHosts, true)

	st := startAndWait(t, mgr, cfg, func(s TunnelStatus) bool {
		return s.State == StateConnected || (s.State == StateReconnecting && s.LastError != "")
	})
	if st.State != StateConnected {
		t.Fatalf("did not connect: %s", st.LastError)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", cfg.LocalPort))
	if err != nil {
		t.Fatalf("request through the tunnel: %v", err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); string(body) != "reached the far side" {
		t.Errorf("body = %q", body)
	}
}

func TestRealSSHWrongSecretFailsWithPermissionDenied(t *testing.T) {
	sshd := startSSHD(t)
	mgr := sshd.manager("not the passphrase")

	st := startAndWait(t, mgr, sshd.tunnel(t, "wrong", sshd.knownHosts, true), func(s TunnelStatus) bool {
		return s.State == StateConnected || (s.State == StateReconnecting && s.LastError != "")
	})
	if st.State == StateConnected {
		t.Fatal("a wrong passphrase must not connect")
	}
	if !strings.Contains(st.LastError, "Permission denied") {
		t.Errorf("LastError = %q, want ssh's refusal", st.LastError)
	}
}

// The helper must refuse the "trust this host?" question even though it holds a secret: accepting unknown host
// keys unattended would defeat host key checking.
func TestRealSSHUnknownHostKeyIsRefusedNotAutoAccepted(t *testing.T) {
	sshd := startSSHD(t)
	mgr := sshd.manager(keyPassphrase)

	st := startAndWait(t, mgr, sshd.tunnel(t, "unknown", sshd.emptyHosts, true), func(s TunnelStatus) bool {
		return s.State == StateConnected || (s.State == StateReconnecting && s.LastError != "")
	})
	if st.State == StateConnected {
		t.Fatal("connected to a host whose key was never trusted")
	}
	if !strings.Contains(st.LastError, "Host key verification failed") {
		t.Errorf("LastError = %q, want a host key failure", st.LastError)
	}
}

// Without a stored secret the tunnel stays in BatchMode: a passphrase-protected key fails at once instead of
// hanging on a prompt nobody can answer.
func TestRealSSHWithoutSecretNeverHangs(t *testing.T) {
	sshd := startSSHD(t)
	mgr := sshd.manager("")
	start := time.Now()

	st := startAndWait(t, mgr, sshd.tunnel(t, "nosecret", sshd.knownHosts, false), func(s TunnelStatus) bool {
		return s.State == StateReconnecting && s.LastError != ""
	})
	if !strings.Contains(st.LastError, "Permission denied") {
		t.Errorf("LastError = %q", st.LastError)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v: ssh was waiting for input", time.Since(start))
	}
}
