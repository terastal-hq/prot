package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terastal-hq/prot/internal/storage"
)

func TestMergePATH(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := MergePATH("/nvm/bin"+sep+"/usr/bin", "/usr/bin"+sep+"/bin"+sep+"")
	want := strings.Join([]string{"/nvm/bin", "/usr/bin", "/bin"}, sep)
	if got != want {
		t.Errorf("MergePATH = %q, want %q", got, want)
	}
}

func TestExtractPATHIgnoresNoise(t *testing.T) {
	out := "Welcome back!\nnvm: using node 24\n" + pathMarkerStart + "/a:/b" + pathMarkerEnd + "\nbye\n"
	got, err := extractPATH(out)
	if err != nil || got != "/a:/b" {
		t.Errorf("extractPATH = %q, %v", got, err)
	}
	if _, err := extractPATH("no markers here"); err == nil {
		t.Error("output without markers should be an error")
	}
	if _, err := extractPATH(pathMarkerStart + pathMarkerEnd); err == nil {
		t.Error("an empty PATH should be an error")
	}
}

// A stand-in login shell that ignores its flags, prints rc-file noise and then runs the script it is given with
// a PATH that only a login shell would have.
func TestLoginShellPATHRunsTheUsersShell(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "fakeshell")
	script := "#!/bin/sh\necho 'rc file chatter'\nPATH=/from/login/shell:$PATH\nshift # -ilc\neval \"$1\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	got, err := LoginShellPATH(context.Background())
	if err != nil {
		t.Fatalf("LoginShellPATH: %v", err)
	}
	if !strings.HasPrefix(got, "/from/login/shell:") {
		t.Errorf("PATH should come from the login shell, got %q", got)
	}
}

func TestLoginShellPATHFailsCleanly(t *testing.T) {
	t.Setenv("SHELL", "")
	if _, err := LoginShellPATH(context.Background()); err == nil {
		t.Error("an unset SHELL should be an error")
	}
	t.Setenv("SHELL", "/nonexistent/shell")
	if _, err := LoginShellPATH(context.Background()); err == nil {
		t.Error("a missing shell should be an error")
	}
}

func TestProcessEnvPrecedence(t *testing.T) {
	t.Setenv("PATH", "/daemon/bin")
	svc := storage.Service{Env: map[string]string{"FOO": "bar"}}

	env := processEnv(svc, 3000, "/login/bin:/daemon/bin")
	if got := lastValue(env, "PATH"); got != "/login/bin:/daemon/bin" {
		t.Errorf("base PATH should replace the daemon's, got %q", got)
	}
	if lastValue(env, "FOO") != "bar" || lastValue(env, "PORT") != "3000" {
		t.Errorf("service env and PORT missing: %v", env)
	}

	svc.Env["PATH"] = "/custom"
	if got := lastValue(processEnv(svc, 0, "/login/bin"), "PATH"); got != "/custom" {
		t.Errorf("a service's own PATH must win, got %q", got)
	}
	if got := lastValue(processEnv(storage.Service{}, 0, ""), "PATH"); got != "/daemon/bin" {
		t.Errorf("without a base PATH the daemon's is kept, got %q", got)
	}
}

func lastValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value = v
		}
	}
	return value
}
