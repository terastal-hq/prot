package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/tunnels"
)

const tunnelUsage = `Usage: prot tunnel <command>

Commands:
  add <alias> --host [user@]host --remote-port N [options]   Save an SSH tunnel
  ls [--json]                                                List tunnels and their state
  edit <id_or_alias> [options]                               Change a tunnel's settings in place
  start <id_or_alias> [--no-wait]                            Connect a tunnel
  stop <id_or_alias>                                         Disconnect a tunnel
  rm <id_or_alias>                                           Disconnect and delete a tunnel
  password <id_or_alias> [--stdin | --clear]                 Set, replace or remove the stored password

add options:
  --host        SSH server: host, user@host, or a machine name from ~/.ssh/config ('prot ssh ls' lists them)
  --user        SSH user (alternative to user@host)
  --remote-port Port to reach on the remote side (required)
  --remote-host Address on the remote side (default 127.0.0.1)
  --local-port  Local port to listen on (default: a free port each time)
  --identity    Private key file
  --ssh-port    SSH server port
  --ssh-arg     Extra ssh argument, repeatable
  --auto-connect  Connect now and every time protd starts
  (see 'prot tunnel edit --help' for the options that change an existing tunnel)
  --password    Ask for an SSH password (or key passphrase) and keep it in your OS keyring
  --password-stdin  Read the password from standard input instead (for scripts)

The tunnel is reachable at http://<alias>.localhost.

ssh runs without a terminal, so it cannot ask anything. Authenticate with a key from ssh-agent, an unencrypted key
file, or a password or passphrase stored with --password (kept in the OS keyring, never in prot's files). Connect to
the host once from a terminal first so ssh trusts its host key: prot never accepts unknown host keys.`

// connectWait is how long start and add --auto-connect wait for a tunnel to come up before reporting.
const connectWait = 20 * time.Second

func cmdTunnel(args []string) {
	if len(args) == 0 {
		fatalf("%s", tunnelUsage)
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "add":
		tunnelAdd(rest)
	case "ls", "list":
		tunnelList(rest)
	case "start":
		tunnelStart(rest)
	case "stop":
		tunnelStop(rest)
	case "edit":
		tunnelEditCmd(rest)
	case "rm", "remove", "delete":
		tunnelRemove(rest)
	case "password", "passwd":
		tunnelPassword(rest)
	case "help", "--help", "-h":
		fmt.Println(tunnelUsage)
	default:
		fatalf("Unknown tunnel command %q\n\n%s", sub, tunnelUsage)
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, " ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// passwordSource says where `prot tunnel add` should get a password from.
type passwordSource int

const (
	passwordNone passwordSource = iota
	passwordPrompt
	passwordStdin
)

// parseTunnelAdd turns `prot tunnel add` arguments into a tunnel definition and the source of its password.
func parseTunnelAdd(args []string) (tunnels.TunnelConfig, passwordSource, error) {
	var cfg tunnels.TunnelConfig
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return cfg, passwordNone, errors.New(
			"usage: prot tunnel add <alias> --host [user@]host --remote-port N [options]")
	}

	fs := flag.NewFlagSet("tunnel add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	host := fs.String("host", "", "")
	user := fs.String("user", "", "")
	remoteHost := fs.String("remote-host", "", "")
	remotePort := fs.Uint("remote-port", 0, "")
	localPort := fs.Uint("local-port", 0, "")
	identity := fs.String("identity", "", "")
	sshPort := fs.Uint("ssh-port", 0, "")
	auto := fs.Bool("auto-connect", false, "")
	askPassword := fs.Bool("password", false, "")
	passwordFromStdin := fs.Bool("password-stdin", false, "")
	var extra stringList
	fs.Var(&extra, "ssh-arg", "")
	if err := fs.Parse(args[1:]); err != nil {
		return cfg, passwordNone, err
	}

	if *host == "" {
		return cfg, passwordNone, errors.New("--host is required")
	}
	if *remotePort == 0 || *remotePort > 65535 {
		return cfg, passwordNone, errors.New("--remote-port is required (1-65535)")
	}
	if *localPort > 65535 || *sshPort > 65535 {
		return cfg, passwordNone, errors.New("ports must be 1-65535")
	}
	if *askPassword && *passwordFromStdin {
		return cfg, passwordNone, errors.New("use either --password or --password-stdin, not both")
	}
	source := passwordNone
	switch {
	case *askPassword:
		source = passwordPrompt
	case *passwordFromStdin:
		source = passwordStdin
	}

	// "user@host" is the familiar spelling, so accept it.
	sshHost, sshUser := *host, *user
	if at := strings.LastIndex(sshHost, "@"); at >= 0 {
		if sshUser == "" {
			sshUser = sshHost[:at]
		}
		sshHost = sshHost[at+1:]
	}

	cfg = tunnels.TunnelConfig{
		Alias:        args[0],
		SSHHost:      sshHost,
		SSHUser:      sshUser,
		RemoteHost:   *remoteHost,
		RemotePort:   uint32(*remotePort),
		LocalPort:    uint32(*localPort),
		IdentityFile: *identity,
		AutoConnect:  *auto,
		ExtraArgs:    []string{},
	}
	if *sshPort > 0 {
		cfg.ExtraArgs = append(cfg.ExtraArgs, "-p", strconv.Itoa(int(*sshPort)))
	}
	cfg.ExtraArgs = append(cfg.ExtraArgs, extra...)
	return cfg, source, nil
}

// readPasswordLine reads one line from r for --password-stdin and password --stdin, without its line ending.
func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.New("no password received on standard input")
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("the password is empty")
	}
	return password, nil
}

// obtainPassword gets a password from the terminal (typed without echo) or from standard input.
func obtainPassword(source passwordSource, confirm bool) (string, error) {
	if source == passwordStdin {
		return readPasswordLine(os.Stdin)
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no terminal to ask on; pipe the password in with --password-stdin")
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		typed, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(typed), err
	}

	password, err := ask("SSH password or key passphrase (kept in your OS keyring): ")
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("the password is empty")
	}
	if confirm {
		again, err := ask("Confirm: ")
		if err != nil {
			return "", err
		}
		if again != password {
			return "", errors.New("the two entries do not match")
		}
	}
	return password, nil
}

// newTunnelRequest is the body of POST /v1/tunnels: the definition plus an optional write-only password.
type newTunnelRequest struct {
	tunnels.TunnelConfig
	Password string `json:"password,omitempty"`
}

func tunnelAdd(args []string) {
	cfg, source, err := parseTunnelAdd(args)
	if err != nil {
		fatalf("%v", err)
	}

	// Ask before touching the daemon, so a typo cannot leave a half-created tunnel.
	password := ""
	if source != passwordNone {
		if password, err = obtainPassword(source, true); err != nil {
			fatalf("Error: %v", err)
		}
	}

	client := connect()
	var view registry.TunnelView
	body := newTunnelRequest{TunnelConfig: cfg, Password: password}
	if err := client.call(http.MethodPost, "/v1/tunnels", body, &view, http.StatusCreated); err != nil {
		fatalf("Failed to add tunnel: %v", err)
	}
	fmt.Printf("✓ Saved tunnel %q (ID: %s)\n", view.Config.Alias, view.Config.ID)
	fmt.Printf("  Address: %s\n", view.CanonicalURL)
	noteConfiguredHost(client, view.Config.SSHHost)
	if view.Config.HasPassword {
		fmt.Println("  Password stored in your OS keyring.")
	}

	if !view.Config.AutoConnect {
		fmt.Printf("  Run 'prot tunnel start %s' to connect.\n", view.Config.Alias)
		return
	}
	reportConnection(client, view.Config.ID)
}

// tunnelLister is what waiting for a connection needs from the daemon.
type tunnelLister interface {
	listTunnels() ([]registry.TunnelView, error)
}

func (c *daemonClient) listTunnels() ([]registry.TunnelView, error) {
	var data struct {
		Tunnels []registry.TunnelView `json:"tunnels"`
	}
	if err := c.call(http.MethodGet, "/v1/tunnels", nil, &data, http.StatusOK); err != nil {
		return nil, err
	}
	return data.Tunnels, nil
}

// waitForTunnel polls until the tunnel is connected, or until it has failed an attempt (so the reason is known),
// or the timeout passes. It returns the last status it saw.
func waitForTunnel(c tunnelLister, id string, timeout, interval time.Duration) (registry.TunnelView, error) {
	var last registry.TunnelView
	deadline := time.Now().Add(timeout)
	for {
		list, err := c.listTunnels()
		if err != nil {
			return last, err
		}
		for _, v := range list {
			if v.Config.ID != id {
				continue
			}
			last = v
			if v.State == tunnels.StateConnected || (v.State == tunnels.StateReconnecting && v.LastError != "") {
				return v, nil
			}
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		time.Sleep(interval)
	}
}

// reportConnection waits for a tunnel and prints the outcome. A tunnel that cannot connect keeps retrying in the
// daemon, so the failure message says how to stop it.
func reportConnection(client *daemonClient, id string) {
	fmt.Print("  Connecting")
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
				fmt.Print(".")
			}
		}
	}()
	view, err := waitForTunnel(client, id, connectWait, 300*time.Millisecond)
	close(stop)
	fmt.Println()

	switch {
	case err != nil:
		fatalf("Error: %v", err)
	case view.State == tunnels.StateConnected:
		fmt.Printf("✓ Connected: %s (local port %d)\n", view.CanonicalURL, view.AssignedLocalPort)
	case view.LastError != "":
		fatalf("✗ Could not connect: %s\n  It keeps retrying in the background; 'prot tunnel stop %s' stops it.",
			view.LastError, view.Config.Alias)
	default:
		fmt.Printf("… Still connecting after %s. Check 'prot tunnel ls'.\n", connectWait)
	}
}

func tunnelStateBadge(state tunnels.State) string {
	switch state {
	case tunnels.StateConnected:
		return "● connected"
	case tunnels.StateConnecting:
		return "◌ connecting"
	case tunnels.StateReconnecting:
		return "↻ retrying"
	case tunnels.StateFailed:
		return "✕ failed"
	default:
		return "○ " + string(state)
	}
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func tunnelList(args []string) {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}

	list, err := connect().listTunnels()
	if err != nil {
		fatalf("Error: %v", err)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(list)
		return
	}
	if len(list) == 0 {
		fmt.Println("No tunnels. Add one with 'prot tunnel add <alias> --host user@host --remote-port N'.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATE\tALIAS\tREMOTE\tAUTH\tLOCAL\tURL")
	_, _ = fmt.Fprintln(w, "-----\t-----\t------\t----\t-----\t---")
	var problems []string
	for _, t := range list {
		local := "-"
		if t.AssignedLocalPort > 0 {
			local = strconv.Itoa(int(t.AssignedLocalPort))
		} else if t.Config.LocalPort > 0 {
			local = strconv.Itoa(int(t.Config.LocalPort))
		}
		remote := fmt.Sprintf("%s:%d via %s", t.Config.RemoteHost, t.Config.RemotePort, t.Config.SSHHost)
		auth := "key/agent"
		if t.Config.HasPassword {
			auth = "password"
		}
		_, _ = fmt.Fprintf(
			w, "%s\t%s\t%s\t%s\t%s\t%s\n", tunnelStateBadge(t.State), t.Config.Alias, remote, auth, local, t.CanonicalURL,
		)
		if t.LastError != "" && t.State != tunnels.StateConnected {
			problems = append(problems, fmt.Sprintf("  %s: %s", t.Config.Alias, t.LastError))
		}
	}
	_ = w.Flush()

	// Reasons go below the table: a long message inside it would stretch the first columns.
	if len(problems) > 0 {
		fmt.Println("\nLast errors:")
		for _, p := range problems {
			fmt.Println(truncateText(p, 160))
		}
	}
}

func tunnelStart(args []string) {
	wait := true
	var target string
	for _, a := range args {
		switch {
		case a == "--no-wait":
			wait = false
		case !strings.HasPrefix(a, "-") && target == "":
			target = a
		}
	}
	if target == "" {
		fatalf("Usage: prot tunnel start <id_or_alias> [--no-wait]")
	}

	client := connect()
	var view registry.TunnelView
	if err := client.call(http.MethodPost, tunnelPath(target, "start"), nil, &view, http.StatusOK); err != nil {
		fatalf("Failed to start tunnel: %v", err)
	}
	fmt.Printf("✓ Starting tunnel %q\n", view.Config.Alias)
	if wait {
		reportConnection(client, view.Config.ID)
	}
}

func tunnelStop(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot tunnel stop <id_or_alias>")
	}
	if err := connect().call(http.MethodPost, tunnelPath(args[0], "stop"), nil, nil, http.StatusOK); err != nil {
		fatalf("Failed to stop tunnel: %v", err)
	}
	fmt.Printf("✓ Stopped tunnel %q\n", args[0])
}

func tunnelPassword(args []string) {
	var target string
	source, clear := passwordPrompt, false
	for _, a := range args {
		switch {
		case a == "--clear":
			clear = true
		case a == "--stdin" || a == "--password-stdin":
			source = passwordStdin
		case !strings.HasPrefix(a, "-") && target == "":
			target = a
		default:
			fatalf("Unknown option %q\nUsage: prot tunnel password <id_or_alias> [--stdin | --clear]", a)
		}
	}
	if target == "" {
		fatalf("Usage: prot tunnel password <id_or_alias> [--stdin | --clear]")
	}

	client := connect()
	var view registry.TunnelView
	if clear {
		err := client.call(http.MethodDelete, tunnelPath(target, "password"), nil, &view, http.StatusOK)
		if err != nil {
			fatalf("Failed to remove the password: %v", err)
		}
		fmt.Printf("✓ Removed the stored password of %q\n", view.Config.Alias)
		return
	}

	password, err := obtainPassword(source, true)
	if err != nil {
		fatalf("Error: %v", err)
	}
	body := map[string]string{"password": password}
	if err := client.call(http.MethodPut, tunnelPath(target, "password"), body, &view, http.StatusOK); err != nil {
		fatalf("Failed to store the password: %v", err)
	}
	fmt.Printf("✓ Password stored in your OS keyring for %q.\n", view.Config.Alias)
	fmt.Println("  It is used from the next connection attempt (a retrying tunnel picks it up by itself).")
}

func tunnelRemove(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot tunnel rm <id_or_alias>")
	}
	if err := connect().call(http.MethodDelete, tunnelPath(args[0]), nil, nil, http.StatusNoContent); err != nil {
		fatalf("Failed to remove tunnel: %v", err)
	}
	fmt.Printf("✓ Removed tunnel %q\n", args[0])
}
