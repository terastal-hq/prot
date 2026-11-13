package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/terastal-hq/prot/completions"
)

// fakeSource is what the daemon would have answered.
type fakeSource map[listKind][]candidate

func (f fakeSource) list(kind listKind) []candidate { return f[kind] }

var testSource = fakeSource{
	listServices:   {{Value: "web", Desc: "Web, running"}, {Value: "api", Desc: "API, stopped"}, {Value: "worker"}},
	listTunnels:    {{Value: "staging-db", Desc: "bastion:5432, connected"}, {Value: "stats"}},
	listDiscovered: {{Value: "disc_abc123_3000", Desc: "node on port 3000"}},
	listHosts:      {{Value: "prod-db"}, {Value: "prod-web"}, {Value: "bastion"}},
}

func values(found []candidate) []string {
	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.Value)
	}
	sort.Strings(out)
	return out
}

// line splits a typed command line into the words 'prot __complete' receives. A trailing space leaves an empty last
// word, which is the word under the cursor.
func line(typed string) []string { return strings.Split(typed, " ") }

func TestComplete(t *testing.T) {
	var all []string
	for _, spec := range commandSpecs {
		all = append(all, spec.Name)
	}
	sort.Strings(all)
	everyCommand := strings.Join(all, " ")

	// Each row is what is typed after "prot" and the suggestions expected, sorted. A trailing space means the cursor
	// is on a new, empty word.
	tests := []struct{ name, typed, want string }{
		{"nothing typed lists every command once", "", everyCommand},
		{"command prefix", "st", "start status stop"},
		{"prefix with one match", "disc", "discover"},
		{"alias only when nothing else matches", "rem", "remove"},
		{"canonical wins over alias", "r", "restart rm"},
		{"dash lists the top-level options", "-", "--help --version -h -v"},
		{"unknown command", "nope ", ""},
		{"service for start", "start ", "api web worker"},
		{"service prefix", "stop w", "web worker"},
		{"alias of a command resolves", "remove ", "api web worker"},
		{"only the first argument is a service", "start web ", ""},
		{"logs flags after the service", "logs web -", "--follow --lines -f -n"},
		{"logs flags on an empty word", "logs web ", "--follow --lines"},
		{"a given flag is not offered again", "logs web -f ", "--lines"},
		{"the value of a text flag is not completed", "logs web -n ", ""},
		{"after a flag value, flags again", "logs web -n 50 ", "--follow"},
		{"adopt takes discovered ids", "adopt ", "disc_abc123_3000"},
		{"add: the name is free text, so no flags yet", "add ", ""},
		{"add: flags after the name", "add api --a", "--alias --auto-port --auto-start"},
		{"tunnel subcommands", "tunnel ", "add edit help ls password rm start stop"},
		{"tunnels is tunnel", "tunnels s", "start stop"},
		{"tunnel add: the alias is free text", "tunnel add ", ""},
		{"tunnel start takes tunnels", "tunnel start ", "staging-db stats"},
		{"tunnel start flag", "tunnel start stats ", "--no-wait"},
		{"tunnel edit takes tunnels", "tunnel edit st", "staging-db stats"},
		{"host completes ssh machines", "tunnel add db --host ", "bastion prod-db prod-web"},
		{"host prefix", "tunnel add db --host prod-", "prod-db prod-web"},
		{"host with an attached value", "tunnel add db --host=pr", "--host=prod-db --host=prod-web"},
		{"host after bash split the equals", "tunnel add db --host = pr", "prod-db prod-web"},
		{"host right after the equals", "tunnel add db --host =", "bastion prod-db prod-web"},
		{"one-dash form of a flag", "tunnel edit stats -host ", "bastion prod-db prod-web"},
		{"ssh-arg can repeat", "tunnel add db --ssh-arg -v --ssh", "--ssh-arg --ssh-port"},
		{"ssh-arg value is not taken for a flag", "tunnel add db --ssh-arg -v --ssh-p", "--ssh-port"},
		{"tunnel password flags", "tunnel password stats --", "--clear --password-stdin --stdin"},
		{"ssh subcommands", "ssh ", "help ls"},
		{"completion shells", "completion ", "bash fish zsh"},
		{"completion shell prefix", "completion z", "zsh"},
		{"update flags", "update ", "--check --force"},
		{"a command with nothing to complete", "status ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dir := complete(line(tt.typed), testSource)
			if names := strings.Join(values(got), " "); names != tt.want {
				t.Errorf("complete(%q) = %q, want %q", tt.typed, names, tt.want)
			}
			if dir != directiveNone {
				t.Errorf("complete(%q) directive = %d, want none", tt.typed, dir)
			}
		})
	}

	if got, _ := complete(nil, testSource); strings.Join(values(got), " ") != everyCommand {
		t.Errorf("complete(nil) = %q, want every command", values(got))
	}
}

func TestCompleteDirectives(t *testing.T) {
	tests := []struct {
		typed string
		want  directive
	}{
		{"add api --cwd ", directiveDirs},
		{"tunnel add db --identity ", directiveFiles},
		{"tunnel edit stats --identity ", directiveFiles},
		{"tunnel add db --identity=", directiveNone}, // a shell cannot complete a path attached to the flag
		{"add api --cmd ", directiveNone},
	}
	for _, tt := range tests {
		got, dir := complete(line(tt.typed), testSource)
		if dir != tt.want || len(got) != 0 {
			t.Errorf("complete(%q) = %q, directive %d; want no candidates and directive %d",
				tt.typed, values(got), dir, tt.want)
		}
	}
}

func TestCompleteWithoutDaemon(t *testing.T) {
	// A daemon that is not there yields no suggestions, and the commands and flags still work.
	none := fakeSource{}
	if got, _ := complete([]string{"start", ""}, none); len(got) != 0 {
		t.Errorf("services without a daemon = %q, want none", values(got))
	}
	if got, _ := complete([]string{"st"}, none); len(got) != 3 {
		t.Errorf("commands without a daemon = %q, want 3", values(got))
	}
	if got, _ := complete([]string{"logs", "x", "--f"}, none); len(got) != 1 {
		t.Errorf("flags without a daemon = %q, want --follow", values(got))
	}
}

func TestCompleteDescriptions(t *testing.T) {
	got, _ := complete([]string{"start", "w"}, testSource)
	if len(got) != 2 || got[0].Desc != "Web, running" {
		t.Errorf("service descriptions = %+v", got)
	}
	got, _ = complete([]string{"sta"}, testSource)
	for _, c := range got {
		if c.Desc == "" {
			t.Errorf("command %q has no description", c.Value)
		}
	}
}

func TestWriteCompletion(t *testing.T) {
	var buf bytes.Buffer
	err := writeCompletion(&buf, []candidate{{Value: "web", Desc: "Web, running"}, {Value: "api"}}, directiveFiles)
	if err != nil {
		t.Fatal(err)
	}
	if want := "web\tWeb, running\napi\n:1\n"; buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	_ = writeCompletion(&buf, nil, directiveNone)
	if buf.String() != ":0\n" {
		t.Errorf("empty output = %q, want only the directive", buf.String())
	}
}

func TestSafeCandidates(t *testing.T) {
	got := safeCandidates([]candidate{
		{Value: "web", Desc: "Web\tApp\nrunning"},
		{Value: "my host"},
		{Value: "$(reboot)"},
		{Value: "a'b"},
		{Value: "*"},
		{Value: ""},
		{Value: "prod-db.internal"},
	})
	if names := strings.Join(values(got), " "); names != "prod-db.internal web" {
		t.Errorf("kept %q, want only the plain names", names)
	}
	for _, c := range got {
		if c.Value == "web" && c.Desc != "Web App running" {
			t.Errorf("description = %q, want one flat line", c.Desc)
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, shell := range completions.Shells {
		script, ok := completions.Script(shell)
		if !ok || !strings.Contains(script, "__complete") {
			t.Errorf("no usable script for %q", shell)
		}
	}
	if _, ok := completions.Script("powershell"); ok {
		t.Error("a script for powershell should not exist")
	}
}

// Each script has to be valid for its shell. Shells that are not installed are skipped.
func TestCompletionScriptSyntax(t *testing.T) {
	checks := map[string][]string{
		"bash": {"bash", "-n"},
		"zsh":  {"zsh", "-n"},
		"fish": {"fish", "--no-execute"},
	}
	for shell, argv := range checks {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			t.Logf("%s is not installed, skipping its syntax check", shell)
			continue
		}
		dir := t.TempDir()
		script, _ := completions.Script(shell)
		file := filepath.Join(dir, "script")
		if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(path, append(argv[1:], file)...).CombinedOutput()
		if err != nil {
			t.Errorf("%s rejects its completion script: %v\n%s", shell, err, out)
		}
	}
}

// The table in completion_spec.go has to describe the code that parses the command line. These tests read the
// source and fail when a command, subcommand or flag exists in one and not in the other, so a new flag cannot
// silently go without completion.

var (
	flagLiteral = regexp.MustCompile(`^--?[a-zA-Z][a-zA-Z-]*$`) // "--json" or "-f" as written in a comparison
	flagName    = regexp.MustCompile(`^[a-z][a-z-]*$`)          // "json" as given to a flag.FlagSet
)

func parseSource(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	return funcs
}

func unquote(lit *ast.BasicLit) (string, bool) {
	if lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// addFlagLiterals records the "--flag" and "-f" strings among exprs.
func addFlagLiterals(found map[string]bool, exprs ...ast.Expr) {
	for _, expr := range exprs {
		if lit, ok := expr.(*ast.BasicLit); ok {
			if s, ok := unquote(lit); ok && flagLiteral.MatchString(s) {
				found[s] = true
			}
		}
	}
}

// flagsIn returns the flags a function handles: the names it registers on a flag.FlagSet and the "--flag" and "-f"
// strings it compares arguments with. A "-p" that is only passed along to ssh is not compared, so it does not count.
func flagsIn(fn *ast.FuncDecl) []string {
	found := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				break
			}
			if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "fs" {
				break
			}
			for _, arg := range n.Args {
				if lit, ok := arg.(*ast.BasicLit); ok {
					if name, ok := unquote(lit); ok && flagName.MatchString(name) {
						found["--"+name] = true
						break
					}
				}
			}
		case *ast.BinaryExpr:
			if n.Op == token.EQL || n.Op == token.NEQ {
				addFlagLiterals(found, n.X, n.Y)
			}
		case *ast.CaseClause:
			addFlagLiterals(found, n.List...)
		}
		return true
	})
	out := make([]string, 0, len(found))
	for flag := range found {
		out = append(out, flag)
	}
	sort.Strings(out)
	return out
}

// casesIn returns the command names a function switches on.
func casesIn(fn *ast.FuncDecl) []string {
	var found []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if clause, ok := n.(*ast.CaseClause); ok {
			for _, expr := range clause.List {
				if lit, ok := expr.(*ast.BasicLit); ok {
					if s, ok := unquote(lit); ok {
						found = append(found, s)
					}
				}
			}
		}
		return true
	})
	sort.Strings(found)
	return found
}

func specNames(specs []commandSpec) []string {
	var names []string
	for _, spec := range specs {
		names = append(names, spec.Name)
		names = append(names, spec.Aliases...)
	}
	sort.Strings(names)
	return names
}

func specFlags(spec commandSpec) []string {
	var flags []string
	for _, f := range spec.Flags {
		flags = append(flags, f.Name)
		if f.Short != "" {
			flags = append(flags, f.Short)
		}
	}
	sort.Strings(flags)
	return flags
}

func without(list []string, drop string) []string {
	var kept []string
	for _, s := range list {
		if s != drop {
			kept = append(kept, s)
		}
	}
	return kept
}

func TestSpecMatchesCommands(t *testing.T) {
	funcs := parseSource(t)
	groups := []struct {
		fn    string
		specs []commandSpec
		skip  string // a command that exists but is not completed
	}{
		{"main", commandSpecs, "__complete"},
		{"cmdTunnel", tunnelSpecs, ""},
		{"cmdSSH", sshSpecs, ""},
	}
	for _, g := range groups {
		fn := funcs[g.fn]
		if fn == nil {
			t.Fatalf("%s no longer exists; update the completion table and this test", g.fn)
		}
		got, want := without(casesIn(fn), g.skip), specNames(g.specs)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s handles %q but completion_spec.go lists %q", g.fn, got, want)
		}
	}
}

func TestSpecMatchesFlags(t *testing.T) {
	funcs := parseSource(t)
	tunnel := func(name string) commandSpec { return *findCommand(tunnelSpecs, name) }
	cases := []struct {
		fn   string
		spec commandSpec
	}{
		{"cmdList", *findCommand(commandSpecs, "ls")},
		{"cmdDiscover", *findCommand(commandSpecs, "discover")},
		{"parseServiceAdd", *findCommand(commandSpecs, "add")},
		{"parseServiceEdit", *findCommand(commandSpecs, "edit")},
		{"cmdAdopt", *findCommand(commandSpecs, "adopt")},
		{"cmdLogs", *findCommand(commandSpecs, "logs")},
		{"cmdUpdate", *findCommand(commandSpecs, "update")},
		{"parseTunnelAdd", tunnel("add")},
		{"parseTunnelEdit", tunnel("edit")},
		{"tunnelList", tunnel("ls")},
		{"tunnelStart", tunnel("start")},
		{"tunnelPassword", tunnel("password")},
	}
	for _, c := range cases {
		fn := funcs[c.fn]
		if fn == nil {
			t.Fatalf("%s no longer exists; update the completion table and this test", c.fn)
		}
		got, want := flagsIn(fn), specFlags(c.spec)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s accepts %q but completion_spec.go lists %q for %q", c.fn, got, want, c.spec.Name)
		}
	}

	// Commands that take no flags must not be given any in the table by mistake.
	for _, name := range []string{"inspect", "start", "stop", "restart", "rm", "alias", "status", "doctor", "mcp"} {
		if flags := specFlags(*findCommand(commandSpecs, name)); len(flags) > 0 {
			t.Errorf("%s lists flags %q in the completion table but the code has none", name, flags)
		}
	}
}

func TestSpecIsComplete(t *testing.T) {
	var walk func(path string, specs []commandSpec)
	walk = func(path string, specs []commandSpec) {
		for _, spec := range specs {
			if spec.Help == "" {
				t.Errorf("%s%s has no description", path, spec.Name)
			}
			for _, f := range spec.Flags {
				if f.Help == "" {
					t.Errorf("%s%s %s has no description", path, spec.Name, f.Name)
				}
				if !strings.HasPrefix(f.Name, "--") || (f.Short != "" && strings.HasPrefix(f.Short, "--")) {
					t.Errorf("%s%s: flag %q or %q is written wrong", path, spec.Name, f.Name, f.Short)
				}
			}
			walk(path+spec.Name+" ", spec.Subs)
		}
	}
	walk("", commandSpecs)
}
