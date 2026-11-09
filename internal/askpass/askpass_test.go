package askpass

import (
	"bytes"
	"strings"
	"testing"
)

func TestAnswersOnlyPasswordAndPassphrasePrompts(t *testing.T) {
	answer := []string{
		"user@bastion.example.com's password: ",
		"alice@10.0.0.5's password:",
		"Password: ",
		"Enter passphrase for key '/home/me/.ssh/id_ed25519': ",
		"Enter passphrase for /home/me/.ssh/id_rsa: ",
		"[user@host] Password:",
	}
	refuse := []string{
		"The authenticity of host 'bastion (10.0.0.5)' can't be established.\nED25519 key fingerprint is SHA256:abc.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ",
		"Are you sure you want to continue connecting (yes/no)? ",
		// a host that happens to be called "password" must not turn a host-key question into a password prompt
		"The authenticity of host 'password.example.com (1.2.3.4)' can't be established.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ",
		"Enter your verification code: ",
		"Verification code:",
		"Enter PIN for 'token': ",
		"Please type 'yes', 'no' or the fingerprint: ",
		"",
		"Permission denied, please try again.",
	}
	for _, p := range answer {
		if !Answers(p) {
			t.Errorf("should answer %q", p)
		}
	}
	for _, p := range refuse {
		if Answers(p) {
			t.Errorf("must refuse %q", p)
		}
	}
}

func TestRunPrintsTheSecretOnlyForAnsweredPrompts(t *testing.T) {
	env := map[string]string{EnvSecret: "hunter2 with spaces"}
	getenv := func(k string) string { return env[k] }

	var out bytes.Buffer
	if code := Run([]string{"alice@host's password: "}, getenv, &out); code != 0 || out.String() != "hunter2 with spaces\n" {
		t.Errorf("password prompt: code %d, out %q", code, out.String())
	}

	out.Reset()
	if code := Run([]string{"Are you sure you want to continue connecting (yes/no)? "}, getenv, &out); code != 1 || out.Len() != 0 {
		t.Errorf("host key prompt must be refused without output: code %d, out %q", code, out.String())
	}

	out.Reset()
	if code := Run([]string{"alice@host's password: "}, func(string) string { return "" }, &out); code != 1 || out.Len() != 0 {
		t.Errorf("no secret configured: code %d, out %q", code, out.String())
	}
}

func TestEnvSelectsTheHelperAndForcesIt(t *testing.T) {
	env := strings.Join(Env("/usr/bin/protd", "s3cret"), "\n")
	for _, want := range []string{
		"SSH_ASKPASS=/usr/bin/protd", "SSH_ASKPASS_REQUIRE=force", "PROT_ASKPASS=1", "PROT_ASKPASS_SECRET=s3cret",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("Env is missing %s:\n%s", want, env)
		}
	}

	if !Requested(func(k string) string { return map[string]string{EnvMode: "1"}[k] }) {
		t.Error("PROT_ASKPASS=1 must select helper mode")
	}
	if Requested(func(string) string { return "" }) {
		t.Error("helper mode must be off by default")
	}
}
