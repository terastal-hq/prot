package main

import (
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/terastal-hq/prot/internal/storage"
)

func TestParseServiceAdd(t *testing.T) {
	svc, err := parseServiceAdd([]string{
		"Docs Site", "--cmd", "npm run dev", "--port", "4321", "--cwd", "/srv/docs",
		"--env", "PATH=/opt/node/bin:/usr/bin", "--env", "TOKEN=a=b", "--auto-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := storage.Service{
		Name: "Docs Site", Alias: "docs-site", Command: "npm run dev", Port: 4321, Cwd: "/srv/docs",
		AutoStart: true, RestartPolicy: storage.RestartOnFailure,
		Env: map[string]string{"PATH": "/opt/node/bin:/usr/bin", "TOKEN": "a=b"},
	}
	if !reflect.DeepEqual(svc, want) {
		t.Errorf("got %+v\nwant %+v", svc, want)
	}
}

func TestParseServiceAddErrors(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"no args":        {nil, "name is required"},
		"help as a name": {[]string{"--cmd", "x"}, "name is required"},
		"no command":     {[]string{"web"}, "--cmd is required"},
		"bad env":        {[]string{"web", "--cmd", "x", "--env", "NOEQUALS"}, "KEY=VALUE"},
		"empty env key":  {[]string{"web", "--cmd", "x", "--env", "=v"}, "KEY=VALUE"},
		"port range":     {[]string{"web", "--cmd", "x", "--port", "70000"}, "--port"},
		"unquoted cmd":   {[]string{"web", "--cmd", "npm", "run", "dev"}, "quote the command"},
		"unknown flag":   {[]string{"web", "--cmd", "x", "-dir", "/tmp"}, "-dir"},
	}
	for name, tc := range cases {
		if _, err := parseServiceAdd(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", name, tc.want, err)
		}
	}
}

// 'prot add x --help' must show the usage, not complain about the missing --cmd.
func TestParseServiceAddHelpFlag(t *testing.T) {
	if _, err := parseServiceAdd([]string{"web", "--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("--help after the name should return flag.ErrHelp, got %v", err)
	}
	if _, err := parseServiceEdit([]string{"web", "-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h after the target should return flag.ErrHelp, got %v", err)
	}
}

func TestParseServiceEditOnlySetsGivenFields(t *testing.T) {
	edit, err := parseServiceEdit([]string{"web", "--cmd", "npm start", "--auto-start=false"})
	if err != nil {
		t.Fatal(err)
	}
	p := edit.patch
	if edit.target != "web" || p.Command == nil || *p.Command != "npm start" ||
		p.AutoStart == nil || *p.AutoStart {
		t.Errorf("unexpected patch: %+v", p)
	}
	if p.Name != nil || p.Alias != nil || p.Cwd != nil || p.Port != nil || p.AutoPort != nil ||
		p.RestartPolicy != nil || p.Env != nil || edit.touchesEnv() {
		t.Errorf("fields that were not given must stay unset: %+v", p)
	}
	if !edit.needsRestart {
		t.Error("changing the command needs a restart")
	}

	renamed, err := parseServiceEdit([]string{"web", "--alias", "site", "--auto-start"})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.needsRestart {
		t.Error("alias and auto-start apply without a restart")
	}
}

func TestParseServiceEditCanSetEmptyValues(t *testing.T) {
	edit, err := parseServiceEdit([]string{"web", "--cwd", "", "--port", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if edit.patch.Cwd == nil || *edit.patch.Cwd != "" || edit.patch.Port == nil || *edit.patch.Port != 0 {
		t.Errorf("an explicit empty cwd and port 0 must be sent: %+v", edit.patch)
	}
}

func TestParseServiceEditErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no target":      nil,
		"flag first":     {"--cmd", "x"},
		"nothing given":  {"web"},
		"bad env":        {"web", "--env", "oops"},
		"port range":     {"web", "--port", "99999"},
		"stray argument": {"web", "--cmd", "npm", "start"},
	} {
		if _, err := parseServiceEdit(args); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMergeEnv(t *testing.T) {
	current := map[string]string{"A": "1", "B": "2"}

	cases := map[string]struct {
		set   map[string]string
		unset []string
		clear bool
		want  map[string]string
	}{
		"set keeps the rest": {map[string]string{"C": "3"}, nil, false, map[string]string{"A": "1", "B": "2", "C": "3"}},
		"set overrides":      {map[string]string{"A": "9"}, nil, false, map[string]string{"A": "9", "B": "2"}},
		"unset":              {nil, []string{"A", "missing"}, false, map[string]string{"B": "2"}},
		"clear then set":     {map[string]string{"C": "3"}, nil, true, map[string]string{"C": "3"}},
		"clear alone":        {nil, nil, true, map[string]string{}},
		"unset beats set":    {map[string]string{"C": "3"}, []string{"C"}, false, map[string]string{"A": "1", "B": "2"}},
	}
	for name, tc := range cases {
		got := mergeEnv(current, tc.set, tc.unset, tc.clear)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
	if len(current) != 2 || current["A"] != "1" {
		t.Errorf("mergeEnv must not modify its input: %v", current)
	}
}
