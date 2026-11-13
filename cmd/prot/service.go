package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
)

const addUsage = `Usage: prot add <name> --cmd "<command>" [options]

Registers a service. Nothing runs until 'prot start'. The command runs through 'sh -c' with the environment protd
has, with PATH taken from your login shell (unless protd runs with -login-shell-path=false).

Options:
  --cmd            Command to run (required)
  --alias          .localhost alias (default: the name, lowercased, spaces as hyphens)
  --port           Port the service listens on; the alias routes to it, and it is passed to the command as $PORT
  --auto-port      Pick a free port at every start and pass it as $PORT (instead of --port)
  --cwd            Working directory
  --env KEY=VALUE  Environment variable for the command, repeatable
  --restart-policy no, on-failure (default) or always
  --auto-start     Start the service whenever protd starts

Change a registered service later with 'prot edit'.`

const editUsage = `Usage: prot edit <id_or_alias> [options]

Changes only what you give; everything else stays as it is. A running service keeps its old command, working
directory, port, environment and restart policy until 'prot restart'. A new alias or name applies at once.

Options:
  --name           New display name
  --alias          New .localhost alias
  --cmd            Command to run
  --port           Port the service listens on
  --auto-port[=false]   Pick a free port at every start
  --cwd            Working directory ("" clears it)
  --env KEY=VALUE  Set an environment variable, repeatable; the others are kept
  --unset-env KEY  Remove an environment variable, repeatable
  --clear-env      Remove every environment variable first (then --env adds the ones you list)
  --restart-policy no, on-failure or always
  --auto-start[=false]  Start the service whenever protd starts`

// envList is a repeatable KEY=VALUE flag.
type envList []string

func (e *envList) String() string     { return strings.Join(*e, " ") }
func (e *envList) Set(v string) error { *e = append(*e, v); return nil }

// parseEnvPairs turns KEY=VALUE arguments into a map. The value may be empty and may itself contain "=".
func parseEnvPairs(pairs []string) (map[string]string, error) {
	env := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--env %q must look like KEY=VALUE", pair)
		}
		env[key] = value
	}
	return env, nil
}

// parseServiceAdd turns 'prot add' arguments into a service definition.
func parseServiceAdd(args []string) (storage.Service, error) {
	var svc storage.Service
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return svc, errors.New("a service name is required first")
	}
	svc.Name = args[0]

	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cmd := fs.String("cmd", "", "")
	alias := fs.String("alias", "", "")
	port := fs.Uint("port", 0, "")
	cwd := fs.String("cwd", "", "")
	autoPort := fs.Bool("auto-port", false, "")
	autoStart := fs.Bool("auto-start", false, "")
	policy := fs.String("restart-policy", storage.RestartOnFailure, "")
	var envPairs envList
	fs.Var(&envPairs, "env", "")
	if err := fs.Parse(args[1:]); err != nil {
		return svc, err
	}
	if fs.NArg() > 0 {
		return svc, fmt.Errorf("unexpected argument %q (quote the command: --cmd \"npm run dev\")", fs.Arg(0))
	}

	if *cmd == "" {
		return svc, errors.New("--cmd is required")
	}
	if *port > 65535 {
		return svc, errors.New("--port must be 0-65535")
	}
	env, err := parseEnvPairs(envPairs)
	if err != nil {
		return svc, err
	}

	if *alias == "" {
		*alias = strings.ToLower(strings.Join(strings.Fields(svc.Name), "-"))
	}
	svc.Command = *cmd
	svc.Alias = *alias
	svc.Port = uint32(*port)
	svc.Cwd = *cwd
	svc.AutoPort = *autoPort
	svc.AutoStart = *autoStart
	svc.RestartPolicy = *policy
	if len(env) > 0 {
		svc.Env = env
	}
	return svc, nil
}

func cmdAdd(args []string) {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println(addUsage)
		return
	}
	svc, err := parseServiceAdd(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println(addUsage)
		return
	}
	if err != nil {
		fatalf("Error: %v\n\n%s", err, addUsage)
	}

	var created registry.ServiceView
	if err := connect().call(http.MethodPost, "/v1/services", svc, &created, http.StatusCreated); err != nil {
		fatalf("Failed to register service: %v", err)
	}
	fmt.Printf("✓ Registered service %q (ID: %s)\n", created.Name, created.ID)
	fmt.Printf("  Alias:     http://%s.localhost\n", created.Alias)
	fmt.Printf("  Command:   %s\n", created.Command)
	fmt.Printf("  Run 'prot start %s' to start.\n", created.ID)
}

// serviceEdit is a parsed 'prot edit' command line.
type serviceEdit struct {
	target string
	patch  registry.ServicePatch

	// The environment is edited key by key, so it is merged with the stored one before it is sent.
	envSet   map[string]string
	envUnset []string
	envClear bool

	needsRestart bool // a setting a running service only picks up when it restarts was changed
}

func (e serviceEdit) touchesEnv() bool {
	return e.envClear || len(e.envSet) > 0 || len(e.envUnset) > 0
}

// parseServiceEdit turns 'prot edit' arguments into a patch holding only what the user asked to change.
func parseServiceEdit(args []string) (serviceEdit, error) {
	var edit serviceEdit
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return edit, errors.New("a service id or alias is required first")
	}
	edit.target = args[0]

	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	alias := fs.String("alias", "", "")
	cmd := fs.String("cmd", "", "")
	port := fs.Uint("port", 0, "")
	cwd := fs.String("cwd", "", "")
	autoPort := fs.Bool("auto-port", false, "")
	autoStart := fs.Bool("auto-start", false, "")
	policy := fs.String("restart-policy", "", "")
	clearEnv := fs.Bool("clear-env", false, "")
	var envPairs, unset envList
	fs.Var(&envPairs, "env", "")
	fs.Var(&unset, "unset-env", "")
	if err := fs.Parse(args[1:]); err != nil {
		return edit, err
	}
	if fs.NArg() > 0 {
		return edit, fmt.Errorf("unexpected argument %q (quote the command: --cmd \"npm run dev\")", fs.Arg(0))
	}

	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if len(given) == 0 {
		return edit, errors.New("nothing to change: give at least one option")
	}

	p := &edit.patch
	if given["name"] {
		p.Name = name
	}
	if given["alias"] {
		p.Alias = alias
	}
	if given["cmd"] {
		p.Command = cmd
		edit.needsRestart = true
	}
	if given["port"] {
		if *port > 65535 {
			return edit, errors.New("--port must be 0-65535")
		}
		v := uint32(*port)
		p.Port = &v
		edit.needsRestart = true
	}
	if given["cwd"] {
		p.Cwd = cwd
		edit.needsRestart = true
	}
	if given["auto-port"] {
		p.AutoPort = autoPort
		edit.needsRestart = true
	}
	if given["auto-start"] {
		p.AutoStart = autoStart
	}
	if given["restart-policy"] {
		p.RestartPolicy = policy
		edit.needsRestart = true
	}

	env, err := parseEnvPairs(envPairs)
	if err != nil {
		return edit, err
	}
	edit.envSet, edit.envUnset, edit.envClear = env, unset, *clearEnv
	if edit.touchesEnv() {
		edit.needsRestart = true
	}
	return edit, nil
}

// mergeEnv applies an environment edit to the stored environment: clear first, then set, then unset.
func mergeEnv(current map[string]string, set map[string]string, unset []string, clear bool) map[string]string {
	merged := make(map[string]string, len(current)+len(set))
	if !clear {
		for k, v := range current {
			merged[k] = v
		}
	}
	for k, v := range set {
		merged[k] = v
	}
	for _, k := range unset {
		delete(merged, k)
	}
	return merged
}

func cmdEdit(args []string) {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println(editUsage)
		return
	}
	edit, err := parseServiceEdit(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println(editUsage)
		return
	}
	if err != nil {
		fatalf("Error: %v\n\n%s", err, editUsage)
	}

	client := connect()
	if edit.touchesEnv() {
		var current registry.ServiceView
		if err := client.call(http.MethodGet, servicePath(edit.target), nil, &current, http.StatusOK); err != nil {
			fatalf("Failed to edit service: %v", err)
		}
		merged := mergeEnv(current.Env, edit.envSet, edit.envUnset, edit.envClear)
		edit.patch.Env = &merged
	}

	var view registry.ServiceView
	if err := client.call(http.MethodPatch, servicePath(edit.target), edit.patch, &view, http.StatusOK); err != nil {
		fatalf("Failed to edit service: %v", err)
	}
	fmt.Printf("✓ Updated service %q\n", view.Name)
	fmt.Printf("  Alias:     http://%s.localhost\n", view.Alias)
	fmt.Printf("  Command:   %s\n", view.Command)
	if len(view.Env) > 0 {
		keys := make([]string, 0, len(view.Env))
		for k := range view.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  Env:       %s\n", strings.Join(keys, ", "))
	}
	if edit.needsRestart && view.Status == storage.StatusRunning {
		fmt.Printf("  It is running with the old settings. Run 'prot restart %s' to apply them.\n", view.Alias)
	}
}
