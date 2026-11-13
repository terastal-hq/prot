package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/terastal-hq/prot/internal/sshconfig"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// sshSetupHint is appended to every add_tunnel failure. The tool cannot ask for anything, so setting the host up
// is the user's job, and the agent should say so instead of retrying.
const sshSetupHint = "add_tunnel only works for a host that ssh already reaches without any prompt: a key that " +
	"ssh-agent or ~/.ssh offers and the server accepts (ssh-copy-id), and a host key that is already trusted " +
	"(connect once from a terminal). Ask the user to set that up; passwords cannot be used here."

// listSSHHostsTool tells an agent which machine names add_tunnel can be given, so it does not have to guess.
func (s *Server) listSSHHostsTool() toolSpec {
	return toolSpec{
		tool: Tool{
			Name: "list_ssh_hosts",
			Description: "List the machine names defined in the user's ~/.ssh/config (the names add_tunnel accepts " +
				"as host). Being listed does not prove key login works. Pass host to see what ssh will use for one " +
				"machine: its real hostname, user, port, jump host and the identity files the configuration names. " +
				"Only reads configuration; it never connects.",
			InputSchema: objectSchema(nil, map[string]any{
				"host": map[string]string{
					"type":        "string",
					"description": "A name from the list, to see how ssh resolves it",
				},
			}),
		},
		run: s.runListSSHHosts,
	}
}

func (s *Server) runListSSHHosts(ctx context.Context, args map[string]any) (string, error) {
	path := s.sshConfig
	if path == "" {
		var err error
		if path, err = sshconfig.DefaultPath(); err != nil {
			return "", err
		}
	}
	hosts, err := sshconfig.Hosts(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	host, err := stringArg(args, "host")
	if err != nil {
		return "", err
	}
	if host == "" {
		return prettyJSON(map[string]any{"hosts": hosts, "count": len(hosts), "config_path": path})
	}

	if s.sshPath == "" {
		return "", errors.New("ssh is not installed, so a machine name cannot be resolved")
	}
	resolved, err := sshconfig.Resolver{SSHPath: s.sshPath, ConfigPath: s.sshConfig}.Resolve(ctx, host, hosts)
	if errors.Is(err, sshconfig.ErrUnknownHost) {
		return "", fmt.Errorf("%q is not defined in %s; list_ssh_hosts without host shows the names", host, path)
	}
	if err != nil {
		return "", err
	}
	return prettyJSON(resolved)
}

// addTunnelTool creates a tunnel and proves it works before keeping it.
//
// It takes none of the options that could widen what ssh does (extra ssh arguments, identity files, passwords):
// ssh runs with BatchMode, so a host that is not already set up fails instead of prompting.
func (s *Server) addTunnelTool() toolSpec {
	return toolSpec{
		tool: Tool{
			Name: "add_tunnel",
			Description: "Create an SSH tunnel that forwards a port on a remote machine to " +
				"http://<alias>.localhost, connect it and report the address. Only works for a host that ssh " +
				"already reaches without a prompt (key authentication set up, for example with ssh-copy-id, and " +
				"its host key already trusted); otherwise it fails and nothing is saved. Passwords and extra ssh " +
				"options are not accepted. A tunnel that connects is saved and reconnects whenever protd starts.",
			InputSchema: objectSchema([]string{"alias", "host", "remote_port"}, map[string]any{
				"alias": map[string]string{
					"type": "string",
					"description": "Name used for http://<alias>.localhost. Lowercase letters, digits and " +
						"hyphens; must not be used by another service or tunnel.",
				},
				"host": map[string]string{
					"type": "string",
					"description": "SSH server: a hostname, user@host, or a machine name from ~/.ssh/config " +
						"(list_ssh_hosts shows them; its User, Port and IdentityFile settings apply).",
				},
				"remote_port": map[string]string{
					"type": "integer", "description": "Port to reach on the remote side",
				},
				"remote_host": map[string]string{
					"type":        "string",
					"description": "Address to reach from the remote machine (default 127.0.0.1)",
				},
				"user": map[string]string{
					"type": "string", "description": "SSH user, when not part of host or ~/.ssh/config",
				},
				"local_port": map[string]string{
					"type":        "integer",
					"description": "Local port to listen on (default: a free port chosen at each connection)",
				},
			}),
		},
		run: s.runAddTunnel,
	}
}

func (s *Server) runAddTunnel(ctx context.Context, args map[string]any) (string, error) {
	var cfg tunnels.TunnelConfig
	var err error
	if cfg.Alias, err = stringArg(args, "alias"); err != nil {
		return "", err
	}
	if cfg.SSHHost, err = stringArg(args, "host"); err != nil {
		return "", err
	}
	if cfg.SSHUser, err = stringArg(args, "user"); err != nil {
		return "", err
	}
	if cfg.RemoteHost, err = stringArg(args, "remote_host"); err != nil {
		return "", err
	}
	if cfg.Alias == "" || cfg.SSHHost == "" {
		return "", errors.New("alias and host are required")
	}
	remotePort, ok, err := uintArg(args, "remote_port")
	if err != nil {
		return "", err
	}
	if !ok || remotePort == 0 {
		return "", errors.New("remote_port is required (1-65535)")
	}
	cfg.RemotePort = remotePort
	if v, ok, err := uintArg(args, "local_port"); err != nil {
		return "", err
	} else if ok {
		cfg.LocalPort = v
	}

	// "user@host" is the familiar spelling; an explicit user wins over it.
	if at := strings.LastIndex(cfg.SSHHost, "@"); at >= 0 {
		if cfg.SSHUser == "" {
			cfg.SSHUser = cfg.SSHHost[:at]
		}
		cfg.SSHHost = cfg.SSHHost[at+1:]
	}
	cfg.AutoConnect = true // registering connects it, which is the check

	view, err := s.reg.RegisterTunnel(ctx, cfg)
	if err != nil {
		return "", err
	}
	id := view.Config.ID

	connected, waitErr := s.reg.WaitForTunnel(ctx, id, tunnelConnectWait)
	if waitErr == nil && connected.State == tunnels.StateConnected {
		return fmt.Sprintf(
			"Tunnel %s is connected: %s (local port %d). It is saved and reconnects when protd starts.",
			connected.Config.Alias, connected.CanonicalURL, connected.AssignedLocalPort,
		), nil
	}

	reason := fmt.Sprintf("no connection within %s", tunnelConnectWait)
	switch {
	case waitErr != nil:
		reason = waitErr.Error()
	case connected.LastError != "":
		reason = connected.LastError
	}
	// Do not leave a tunnel behind that keeps retrying a host that was never set up. The request context may be
	// the very thing that ended the wait, so the cleanup must not depend on it.
	if err := s.reg.DeleteTunnel(context.WithoutCancel(ctx), id); err != nil {
		return "", fmt.Errorf("tunnel %s could not connect (%s) and could not be removed: %w", cfg.Alias, reason, err)
	}
	return "", fmt.Errorf("tunnel %s could not connect: %s. Nothing was saved. %s", cfg.Alias, reason, sshSetupHint)
}
