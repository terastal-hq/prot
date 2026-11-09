package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildProtd compiles the real daemon binary, because the askpass behaviour is about how that binary is started.
func buildProtd(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds protd")
	}
	bin := filepath.Join(t.TempDir(), "protd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func runProtd(bin string, env []string, args ...string) (string, int) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// ssh starts protd as its askpass helper: it must answer a password prompt and then exit, not start a daemon.
func TestProtdActsAsTheAskpassHelper(t *testing.T) {
	bin := buildProtd(t)
	env := []string{"PROT_ASKPASS=1", "PROT_ASKPASS_SECRET=open sesame"}

	out, code := runProtd(bin, env, "alice@bastion's password: ")
	if code != 0 || out != "open sesame\n" {
		t.Errorf("password prompt: exit %d, output %q", code, out)
	}

	out, code = runProtd(bin, env, "Are you sure you want to continue connecting (yes/no/[fingerprint])? ")
	if code != 1 || out != "" {
		t.Errorf("host key prompt must be refused with no output: exit %d, output %q", code, out)
	}
}

// Without the helper switch protd is the daemon as before: -version works and prompts are not special.
func TestProtdIsStillTheDaemonByDefault(t *testing.T) {
	bin := buildProtd(t)
	out, code := runProtd(bin, []string{"PROT_ASKPASS=", "PROT_ASKPASS_SECRET=leak"}, "-version")
	if code != 0 || !strings.Contains(out, "protd version") || strings.Contains(out, "leak") {
		t.Errorf("-version: exit %d, output %q", code, out)
	}
}
