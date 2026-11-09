// Package askpass lets protd answer ssh's password prompts without a terminal.
//
// ssh runs the program named by SSH_ASKPASS (with SSH_ASKPASS_REQUIRE=force) whenever it needs a secret, passing
// the prompt as the only argument and reading the answer from stdout. protd points SSH_ASKPASS at its own binary
// and sets PROT_ASKPASS=1, so a protd started that way is the helper instead of the daemon.
//
// The helper answers only password and passphrase prompts. Everything else, above all "Are you sure you want to
// continue connecting (yes/no)?", is refused: an unattended helper that accepted unknown host keys would defeat
// host key checking.
package askpass

import (
	"fmt"
	"io"
	"strings"
)

// Environment variables that switch protd into helper mode and carry the secret to it.
const (
	EnvMode   = "PROT_ASKPASS"
	EnvSecret = "PROT_ASKPASS_SECRET"
)

// Requested reports whether this process was started by ssh as its askpass helper.
func Requested(getenv func(string) string) bool { return getenv(EnvMode) == "1" }

// Env returns the environment additions that make ssh use program as its askpass helper with the given secret.
// SSH_ASKPASS_REQUIRE=force means ssh uses the helper even when a terminal or DISPLAY exists.
func Env(program, secret string) []string {
	return []string{
		"SSH_ASKPASS=" + program,
		"SSH_ASKPASS_REQUIRE=force",
		EnvMode + "=1",
		EnvSecret + "=" + secret,
	}
}

// Answers reports whether prompt is a request for a password or key passphrase, as opposed to anything else ssh
// might ask (host key confirmation, keyboard-interactive questions).
func Answers(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))

	// Host key confirmation mentions the host name, which could itself contain "password", so refuse it first.
	for _, refused := range []string{"yes/no", "continue connecting", "fingerprint", "authenticity of host"} {
		if strings.Contains(p, refused) {
			return false
		}
	}

	switch {
	case strings.HasSuffix(p, "password:"): // "user@host's password:" and "Password:"
		return true
	case strings.HasPrefix(p, "enter passphrase for"): // "Enter passphrase for key '/path':"
		return true
	}
	return false
}

// Run answers one ssh prompt. args are the command-line arguments after the program name. It returns the process
// exit status: 0 after printing the secret, 1 to refuse.
func Run(args []string, getenv func(string) string, out io.Writer) int {
	if !Answers(strings.Join(args, " ")) {
		return 1
	}
	secret := getenv(EnvSecret)
	if secret == "" {
		return 1
	}
	if _, err := fmt.Fprintln(out, secret); err != nil {
		return 1
	}
	return 0
}
