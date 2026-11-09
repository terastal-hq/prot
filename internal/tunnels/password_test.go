package tunnels

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
)

func registerWithPassword(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	port, _ := netutil.FreePort()
	cfg := TunnelConfig{
		ID: id, SSHHost: "fake.invalid", SSHUser: "me", RemotePort: 80, LocalPort: port, HasPassword: true,
		ExtraArgs: []string{"-o", "BatchMode=yes"}, // a tunnel must not be able to turn BatchMode back on
	}
	if _, err := mgr.Register(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordTunnelUsesAskpassInsteadOfBatchMode(t *testing.T) {
	dir := t.TempDir()
	argsFile, envFile := filepath.Join(dir, "args"), filepath.Join(dir, "env")
	t.Setenv("PROT_FAKE_SSH_ARGS", argsFile)
	t.Setenv("PROT_FAKE_SSH_ENV", envFile)

	mgr := fakeSSHManager(t, "listen")
	mgr.SetSecretProvider(func(id string) (string, error) {
		if id != "pw" {
			t.Errorf("secret requested for %q", id)
		}
		return "s3cret value", nil
	})
	mgr.SetAskpassProgram("/opt/prot/protd")
	registerWithPassword(t, mgr, "pw")
	_, _ = mgr.Start(context.Background(), "pw")
	defer mgr.Stop("pw")
	waitState(t, mgr, "pw", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })

	argsData, _ := os.ReadFile(argsFile)
	args := strings.Split(string(argsData), "\n")
	indexOf := func(v string) int {
		for i, a := range args {
			if a == v {
				return i
			}
		}
		return -1
	}
	if indexOf("NumberOfPasswordPrompts=1") < 0 {
		t.Errorf("a wrong password must cost one attempt, args: %v", args)
	}
	// Our own options come first; the tunnel's BatchMode=yes is the only one present (the user's), proving prot
	// did not add BatchMode itself, and that the "first value wins" ordering is what protects the other options.
	if n := strings.Count(string(argsData), "BatchMode=yes"); n != 1 {
		t.Errorf("expected only the user-supplied BatchMode, found %d in %v", n, args)
	}

	envData, _ := os.ReadFile(envFile)
	env := string(envData)
	for _, want := range []string{
		"SSH_ASKPASS=/opt/prot/protd", "SSH_ASKPASS_REQUIRE=force", "PROT_ASKPASS=1", "PROT_ASKPASS_SECRET=s3cret value",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("ssh environment is missing %q:\n%s", want, env)
		}
	}
	if strings.Contains(string(argsData), "s3cret") {
		t.Error("the secret must never appear on the command line")
	}
}

func TestTunnelWithoutPasswordStaysBatchMode(t *testing.T) {
	dir := t.TempDir()
	argsFile, envFile := filepath.Join(dir, "args"), filepath.Join(dir, "env")
	t.Setenv("PROT_FAKE_SSH_ARGS", argsFile)
	t.Setenv("PROT_FAKE_SSH_ENV", envFile)
	t.Setenv("SSH_ASKPASS", "") // whatever the developer's shell has

	mgr := fakeSSHManager(t, "listen")
	mgr.SetSecretProvider(func(string) (string, error) { t.Error("no secret should be looked up"); return "", nil })
	registerFake(t, mgr, "plain")
	_, _ = mgr.Start(context.Background(), "plain")
	defer mgr.Stop("plain")
	waitState(t, mgr, "plain", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })

	argsData, _ := os.ReadFile(argsFile)
	batch := strings.Contains(string(argsData), "BatchMode=yes")
	if !batch || strings.Contains(string(argsData), "NumberOfPasswordPrompts") {
		t.Errorf("tunnels without a password must stay in BatchMode: %q", argsData)
	}
	envData, _ := os.ReadFile(envFile)
	if strings.Contains(string(envData), "PROT_ASKPASS") {
		t.Errorf("askpass must not be configured for a tunnel without a password: %q", envData)
	}
}

// A keyring that is locked or missing must show up as the reason, and the tunnel must keep retrying so it
// connects once the keyring opens.
func TestMissingKeyringIsReportedAndRetried(t *testing.T) {
	mgr := fakeSSHManager(t, "listen")
	calls := 0
	mgr.SetSecretProvider(func(string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("keyring is locked")
		}
		return "now unlocked", nil
	})
	registerWithPassword(t, mgr, "locked")
	_, _ = mgr.Start(context.Background(), "locked")
	defer mgr.Stop("locked")

	st := waitState(t, mgr, "locked", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateReconnecting })
	for _, want := range []string{"reading the stored password", "keyring is locked"} {
		if !strings.Contains(st.LastError, want) {
			t.Errorf("LastError = %q, want it to mention %q", st.LastError, want)
		}
	}
	waitState(t, mgr, "locked", 10*time.Second, func(s TunnelStatus) bool { return s.State == StateConnected })
}

func TestPasswordTunnelWithoutAnyProviderFailsClearly(t *testing.T) {
	mgr := fakeSSHManager(t, "listen") // no SetSecretProvider
	registerWithPassword(t, mgr, "noprov")
	_, _ = mgr.Start(context.Background(), "noprov")
	defer mgr.Stop("noprov")

	st := waitState(t, mgr, "noprov", 5*time.Second, func(s TunnelStatus) bool { return s.State == StateReconnecting })
	if !strings.Contains(st.LastError, "no keyring is configured") {
		t.Errorf("LastError = %q", st.LastError)
	}
}
