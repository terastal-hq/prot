package supervisor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// loginShellTimeout bounds how long the user's shell may take to start up. A slow or hung rc file must not
// hold the daemon back.
const loginShellTimeout = 5 * time.Second

const (
	pathMarkerStart = "__PROT_PATH_START__"
	pathMarkerEnd   = "__PROT_PATH_END__"
)

// LoginShellPATH asks the user's login shell what PATH an interactive terminal would have.
//
// protd is normally started by systemd or launchd with a minimal PATH, so tools installed by nvm, fnm, asdf,
// cargo or ~/.local/bin are invisible to the services it spawns. The shell is run as `-ilc` so both the login
// profile and the rc file (where nvm and friends usually hook in) are read. The PATH is printed between markers
// so anything the rc files print is ignored.
func LoginShellPATH(ctx context.Context) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return "", errors.New("SHELL is not set")
	}

	ctx, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc", printPATHScript(shell))
	cmd.Stdin = nil // /dev/null: an rc file that prompts must fail instead of waiting
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return "", err
	}
	return extractPATH(stdout.String())
}

// printPATHScript returns shell code that prints $PATH between the markers. fish keeps PATH as a list.
func printPATHScript(shell string) string {
	if filepath.Base(shell) == "fish" {
		return `printf '%s%s%s' ` + pathMarkerStart + ` (string join : $PATH) ` + pathMarkerEnd
	}
	return `printf '%s%s%s' ` + pathMarkerStart + ` "$PATH" ` + pathMarkerEnd
}

func extractPATH(out string) (string, error) {
	start := strings.Index(out, pathMarkerStart)
	end := strings.LastIndex(out, pathMarkerEnd)
	if start < 0 || end < start {
		return "", errors.New("login shell did not report a PATH")
	}
	path := strings.TrimSpace(out[start+len(pathMarkerStart) : end])
	if path == "" {
		return "", errors.New("login shell reported an empty PATH")
	}
	return path, nil
}

// MergePATH returns login followed by the entries of current that login does not already contain, so
// directories the daemon was started with (for example a service manager's) are never lost.
func MergePATH(login, current string) string {
	seen := map[string]bool{}
	var merged []string
	for _, list := range []string{login, current} {
		for _, dir := range filepath.SplitList(list) {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			merged = append(merged, dir)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}
