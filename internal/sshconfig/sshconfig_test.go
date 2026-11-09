package sshconfig

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostsParsing(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	write(t, cfg, `
# comment
Host prod-db  staging
  HostName db.example.com

host=lower
Host = spaced
Host "quoted name"
Host *.internal
Host web-?
Host !secret
Host * # everything
Host trailing # comment
Match host foo
  User x
Host staging
Host   dup dup
`)
	got, err := Hosts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prod-db", "staging", "lower", "spaced", "quoted name", "trailing", "dup"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHostsMissingFileIsEmptyNotAnError(t *testing.T) {
	got, err := Hosts(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("got %v, %v; want a non-nil empty list", got, err)
	}
}

func TestHostsFollowsIncludes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	write(t, filepath.Join(ssh, "config"), "Include conf.d/*.conf\nInclude ~/extra\nInclude missing/*\nHost main\n")
	write(t, filepath.Join(ssh, "conf.d", "a.conf"), "Host from-a\n")
	write(t, filepath.Join(ssh, "conf.d", "b.conf"), "Host from-b\nInclude loop\n")
	write(t, filepath.Join(ssh, "loop"), "Include config\nHost looped\n")
	write(t, filepath.Join(home, "extra"), "Host from-home\n")

	got, err := Hosts(filepath.Join(ssh, "config"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"from-a", "from-b", "main", "looped", "from-home"}
	// config -> conf.d/b.conf -> loop -> config is a cycle; recursion is bounded, so it terminates.
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHostsIgnoresOptionLookalikes(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config")
	write(t, cfg, "Host -oProxyCommand=evil ok\n")
	got, _ := Hosts(cfg)
	if !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("got %q", got)
	}
}

func TestSplitLine(t *testing.T) {
	cases := []struct {
		in   string
		key  string
		args []string
	}{
		{"Host a b", "host", []string{"a", "b"}},
		{"  HOST\ta", "host", []string{"a"}},
		{"Host=a", "host", []string{"a"}},
		{"Host = a", "host", []string{"a"}},
		{`Host "a b" c`, "host", []string{"a b", "c"}},
		{"# Host a", "", nil},
		{"", "", nil},
		{"Host a#b", "host", []string{"a#b"}},
	}
	for _, c := range cases {
		key, args := splitLine(c.in)
		if key != c.key || (len(args) != len(c.args)) || (len(args) > 0 && !reflect.DeepEqual(args, c.args)) {
			t.Errorf("%q: got %q %q, want %q %q", c.in, key, args, c.key, c.args)
		}
	}
}

func TestResolveWithRealSSH(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh is not installed")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	write(t, cfg, `Host prod
  HostName db.example.com
  User deploy
  Port 2222
  IdentityFile ~/.ssh/prod_key
  ProxyJump bastion

Host *
  ServerAliveInterval 30
`)
	hosts, err := Hosts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := Resolver{SSHPath: sshPath, ConfigPath: cfg}

	got, err := r.Resolve(context.Background(), "prod", hosts)
	if err != nil {
		t.Fatal(err)
	}
	want := Resolved{
		Alias: "prod", HostName: "db.example.com", User: "deploy", Port: 2222, ProxyJump: "bastion",
		IdentityFiles: []string{"~/.ssh/prod_key"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v (ssh's default keys must not appear)", got, want)
	}

	if _, err := r.Resolve(context.Background(), "not-listed", hosts); err != ErrUnknownHost {
		t.Errorf("an unlisted name must be refused, got %v", err)
	}
	if _, err := r.Resolve(context.Background(), "-oProxyCommand=x", []string{"-oProxyCommand=x"}); err == nil {
		t.Error("a name that looks like an option must be refused")
	}
}

func TestResolveWithoutSSH(t *testing.T) {
	_, err := Resolver{}.Resolve(context.Background(), "a", []string{"a"})
	if err == nil {
		t.Error("expected an error when ssh is missing")
	}
}
