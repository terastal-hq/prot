package main

import (
	"reflect"
	"testing"
)

func TestParseTunnelEditOnlyTouchesWhatIsGiven(t *testing.T) {
	edit, err := parseTunnelEdit([]string{"db", "--remote-port", "6000"})
	if err != nil {
		t.Fatal(err)
	}
	p := edit.patch
	if edit.target != "db" || p.RemotePort == nil || *p.RemotePort != 6000 {
		t.Errorf("edit: %+v", edit)
	}
	if p.Alias != nil || p.SSHHost != nil || p.SSHUser != nil || p.IdentityFile != nil || p.LocalPort != nil ||
		p.AutoConnect != nil || p.ExtraArgs != nil || edit.sshPort != nil || edit.sshArgs != nil {
		t.Errorf("fields that were not given must stay nil: %+v", edit)
	}
}

func TestParseTunnelEditClearsAndSplitsHost(t *testing.T) {
	edit, err := parseTunnelEdit(
		[]string{"db", "--user", "", "--identity", "", "--local-port", "0", "--auto-connect=false"})
	if err != nil {
		t.Fatal(err)
	}
	p := edit.patch
	if p.SSHUser == nil || *p.SSHUser != "" || p.IdentityFile == nil || *p.IdentityFile != "" {
		t.Errorf("an empty value must clear the field: %+v", p)
	}
	if p.LocalPort == nil || *p.LocalPort != 0 || p.AutoConnect == nil || *p.AutoConnect {
		t.Errorf("local port 0 and auto-connect=false must be sent: %+v", p)
	}

	edit, err = parseTunnelEdit([]string{"db", "--host", "admin@bastion"})
	if err != nil {
		t.Fatal(err)
	}
	if *edit.patch.SSHHost != "bastion" || edit.patch.SSHUser == nil || *edit.patch.SSHUser != "admin" {
		t.Errorf("user@host: %+v", edit.patch)
	}

	edit, _ = parseTunnelEdit([]string{"db", "--host", "admin@bastion", "--user", "ops"})
	if *edit.patch.SSHUser != "ops" {
		t.Errorf("--user must win over user@host: %q", *edit.patch.SSHUser)
	}
}

func TestParseTunnelEditRefusesNonsense(t *testing.T) {
	cases := [][]string{
		{},                                    // no target
		{"--remote-port", "1"},                // target missing
		{"db"},                                // nothing to change
		{"db", "--remote-port", "0"},          // a tunnel needs a remote port
		{"db", "--remote-port", "70000"},      // out of range
		{"db", "--ssh-port", "70000"},         // out of range
		{"db", "--bogus", "1"},                // unknown option
		{"db", "extra", "--remote-port", "1"}, // stray argument
	}
	for _, args := range cases {
		if _, err := parseTunnelEdit(args); err == nil {
			t.Errorf("%v should be refused", args)
		}
	}
}

func TestMergeExtraArgs(t *testing.T) {
	port := func(n uint) *uint { return &n }
	args := func(a ...string) *[]string { return &a }

	cases := []struct {
		name    string
		current []string
		port    *uint
		extra   *[]string
		want    []string
	}{
		{"change the port, keep the rest", []string{"-p", "22", "-o", "X=1"}, port(2222), nil,
			[]string{"-p", "2222", "-o", "X=1"}},
		{"add a port", []string{"-o", "X=1"}, port(2222), nil, []string{"-p", "2222", "-o", "X=1"}},
		{"port 0 removes it", []string{"-p", "22", "-o", "X=1"}, port(0), nil, []string{"-o", "X=1"}},
		{"replace the rest, keep the port", []string{"-p", "22", "-o", "X=1"}, nil, args("-v"), []string{"-p", "22", "-v"}},
		{"replace both", []string{"-p", "22", "-o", "X=1"}, port(2200), args("-v"), []string{"-p", "2200", "-v"}},
		{"nothing at all", nil, port(0), nil, []string{}},
	}
	for _, c := range cases {
		if got := mergeExtraArgs(c.current, c.port, c.extra); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
