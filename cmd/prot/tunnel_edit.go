package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/tunnels"
)

const tunnelEditUsage = `Usage: prot tunnel edit <id_or_alias> [options]

Changes only what you give; everything else stays as it is. A connected tunnel is reconnected with the new settings.

Options:
  --alias        Rename the tunnel (its .localhost address changes with it)
  --host         SSH server: host, user@host, or a machine name from ~/.ssh/config
  --user         SSH user ("" clears it)
  --remote-host  Address on the remote side
  --remote-port  Port to reach on the remote side
  --local-port   Local port to listen on (0 picks a free one each time)
  --identity     Private key file ("" clears it)
  --ssh-port     SSH server port (0 goes back to the default)
  --ssh-arg      Extra ssh argument, repeatable; replaces the current extra arguments
  --auto-connect[=false]  Connect whenever protd starts

The stored password is not changed here: use 'prot tunnel password'.`

// tunnelEdit is a parsed 'prot tunnel edit' command line.
type tunnelEdit struct {
	target  string
	patch   registry.TunnelPatch
	sshPort *uint     // set when --ssh-port was given
	sshArgs *[]string // set when --ssh-arg was given
}

// parseTunnelEdit turns 'prot tunnel edit' arguments into a patch holding only what the user asked to change.
func parseTunnelEdit(args []string) (tunnelEdit, error) {
	var edit tunnelEdit
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return edit, errors.New("usage: prot tunnel edit <id_or_alias> [options]")
	}
	edit.target = args[0]

	fs := flag.NewFlagSet("tunnel edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	alias := fs.String("alias", "", "")
	host := fs.String("host", "", "")
	user := fs.String("user", "", "")
	remoteHost := fs.String("remote-host", "", "")
	remotePort := fs.Uint("remote-port", 0, "")
	localPort := fs.Uint("local-port", 0, "")
	identity := fs.String("identity", "", "")
	sshPort := fs.Uint("ssh-port", 0, "")
	auto := fs.Bool("auto-connect", false, "")
	var extra stringList
	fs.Var(&extra, "ssh-arg", "")
	if err := fs.Parse(args[1:]); err != nil {
		return edit, err
	}
	if fs.NArg() > 0 {
		return edit, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if len(given) == 0 {
		return edit, errors.New("nothing to change: give at least one option (see 'prot tunnel help')")
	}

	p := &edit.patch
	if given["alias"] {
		p.Alias = alias
	}
	if given["host"] {
		// "user@host" is the familiar spelling; an explicit --user wins over it.
		sshHost := *host
		if at := strings.LastIndex(sshHost, "@"); at >= 0 {
			if !given["user"] {
				hostUser := sshHost[:at]
				p.SSHUser = &hostUser
			}
			sshHost = sshHost[at+1:]
		}
		p.SSHHost = &sshHost
	}
	if given["user"] {
		p.SSHUser = user
	}
	if given["remote-host"] {
		p.RemoteHost = remoteHost
	}
	if given["remote-port"] {
		if *remotePort == 0 || *remotePort > 65535 {
			return edit, errors.New("--remote-port must be 1-65535")
		}
		port := uint32(*remotePort)
		p.RemotePort = &port
	}
	if given["local-port"] {
		if *localPort > 65535 {
			return edit, errors.New("--local-port must be 0-65535")
		}
		port := uint32(*localPort)
		p.LocalPort = &port
	}
	if given["identity"] {
		p.IdentityFile = identity
	}
	if given["ssh-port"] {
		if *sshPort > 65535 {
			return edit, errors.New("--ssh-port must be 0-65535")
		}
		edit.sshPort = sshPort
	}
	if given["ssh-arg"] {
		edit.sshArgs = (*[]string)(&extra)
	}
	if given["auto-connect"] {
		p.AutoConnect = auto
	}
	return edit, nil
}

// mergeExtraArgs builds the new extra ssh arguments. The SSH port lives in them as "-p N", so changing the port
// keeps the other arguments, and replacing the other arguments keeps the port unless it is changed as well.
func mergeExtraArgs(current []string, port *uint, extra *[]string) []string {
	var portArgs, rest []string
	for i := 0; i < len(current); i++ {
		if current[i] == "-p" && i+1 < len(current) {
			portArgs = []string{"-p", current[i+1]}
			i++
			continue
		}
		rest = append(rest, current[i])
	}

	if port != nil {
		portArgs = nil
		if *port > 0 {
			portArgs = []string{"-p", strconv.FormatUint(uint64(*port), 10)}
		}
	}
	if extra != nil {
		rest = *extra
	}
	return append(append([]string{}, portArgs...), rest...)
}

func tunnelEditCmd(args []string) {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println(tunnelEditUsage)
		return
	}
	edit, err := parseTunnelEdit(args)
	if err != nil {
		fatalf("%v\n\n%s", err, tunnelEditUsage)
	}

	client := connect()
	if edit.sshPort != nil || edit.sshArgs != nil {
		current, err := findTunnel(client, edit.target)
		if err != nil {
			fatalf("Failed to edit tunnel: %v", err)
		}
		merged := mergeExtraArgs(current.Config.ExtraArgs, edit.sshPort, edit.sshArgs)
		edit.patch.ExtraArgs = &merged
	}

	var view registry.TunnelView
	if err := client.call(http.MethodPatch, tunnelPath(edit.target), edit.patch, &view, http.StatusOK); err != nil {
		fatalf("Failed to edit tunnel: %v", err)
	}
	fmt.Printf("✓ Updated tunnel %q\n", view.Config.Alias)
	fmt.Printf("  Address: %s\n", view.CanonicalURL)
	// A tunnel that was live is being reconnected; one that is stopped, or already connected because the edit
	// changed nothing, needs no word about it.
	if view.State == tunnels.StateConnecting || view.State == tunnels.StateReconnecting {
		fmt.Println("  Reconnecting with the new settings.")
		reportConnection(client, view.Config.ID)
	}
}

// findTunnel returns the tunnel with this id or alias.
func findTunnel(client *daemonClient, idOrAlias string) (registry.TunnelView, error) {
	list, err := client.listTunnels()
	if err != nil {
		return registry.TunnelView{}, err
	}
	for _, t := range list {
		if t.Config.ID == idOrAlias || t.Config.Alias == idOrAlias {
			return t, nil
		}
	}
	return registry.TunnelView{}, fmt.Errorf("no tunnel %q", idOrAlias)
}
