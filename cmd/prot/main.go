package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/terastal-hq/prot/internal/daemon"
	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/mcp"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/updater"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("prot version %s (%s/%s)\n", daemon.Version, runtime.GOOS, runtime.GOARCH)
	case "ls", "list":
		cmdList(args)
	case "inspect":
		cmdInspect(args)
	case "add":
		cmdAdd(args)
	case "start":
		cmdStart(args)
	case "stop":
		cmdStop(args)
	case "restart":
		cmdRestart(args)
	case "rm", "remove", "delete":
		cmdRemove(args)
	case "logs":
		cmdLogs(args)
	case "adopt":
		cmdAdopt(args)
	case "discover":
		cmdDiscover(args)
	case "alias":
		cmdAlias(args)
	case "tunnel", "tunnels":
		cmdTunnel(args)
	case "ssh":
		cmdSSH(args)
	case "status":
		cmdStatus(args)
	case "doctor":
		cmdDoctor(args)
	case "update", "upgrade":
		cmdUpdate(args)
	case "mcp":
		cmdMCP(args)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %q\nRun 'prot help' for usage.\n", cmd)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`prot — Your development servers, under control.

Usage:
  prot <command> [arguments]

Service Management:
  ls               List all registered and discovered services
  inspect <id>     Show detailed configuration and runtime state
  add <name>       Register a new managed service
  start <id>       Start a managed service
  stop <id>        Stop a running managed service
  restart <id>     Restart a managed service
  rm <id>          Remove a service registration
  logs <id>        View or stream service stdout/stderr logs
  adopt <disc_id>  Adopt an observed process into a managed service

Routing & Discovery:
  discover         Scan listening TCP processes and HTTP services
  alias <a> <url>  Map a .localhost alias to an upstream target URL
  status           Show protd daemon health and active routes
  tunnel <cmd>     Manage SSH tunnels (add, ls, edit, start, stop, rm, password)
  ssh ls           List the machines in your ~/.ssh/config

Integrations & Diagnostics:
  mcp              Run Model Context Protocol (MCP) stdio adapter
  doctor           Check environment, permissions, and toolchain
  update           Check for updates and upgrade prot in-place
  version          Print version information

Options:
  --json           Format output as JSON where supported`)
}

func cmdList(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	var data struct {
		Services []registry.ServiceView `json:"services"`
	}
	if err := connect().call(http.MethodGet, "/v1/services", nil, &data, http.StatusOK); err != nil {
		fatalf("Error: %v", err)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(data.Services)
		return
	}

	if len(data.Services) == 0 {
		fmt.Println("No services registered. Add one with 'prot add' or see 'prot discover'.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATUS\tNAME\tALIAS\tPORT\tPID\tURL")
	_, _ = fmt.Fprintln(w, "------\t----\t-----\t----\t---\t---")

	for _, s := range data.Services {
		pidStr := "-"
		if s.PID > 0 {
			pidStr = strconv.Itoa(s.PID)
		}
		portStr := "-"
		if s.Port > 0 {
			portStr = strconv.Itoa(int(s.Port))
		}
		aliasStr := s.Alias
		if aliasStr == "" {
			aliasStr = "-"
		}
		urlStr := s.CanonicalURL
		if urlStr == "" {
			urlStr = s.DirectURL
		}
		if urlStr == "" {
			urlStr = "-"
		}

		statusBadge := s.Status
		switch s.Status {
		case storage.StatusRunning:
			statusBadge = "● running"
		case storage.StatusStopped:
			statusBadge = "○ stopped"
		case storage.StatusStarting:
			statusBadge = "◌ starting"
		case storage.StatusCrashed:
			statusBadge = "✕ crashed"
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", statusBadge, s.Name, aliasStr, portStr, pidStr, urlStr)
	}
	_ = w.Flush()
}

func cmdInspect(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot inspect <id_or_alias>")
	}
	target := args[0]

	var svc registry.ServiceView
	if err := connect().call(http.MethodGet, servicePath(target), nil, &svc, http.StatusOK); err != nil {
		fatalf("Service %q: %v", target, err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(svc)
}

func cmdAdd(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot add <name> --cmd \"<command>\" " +
			"[--alias <alias>] [--port <port>] [--cwd <path>] [--auto-port]")
		os.Exit(1)
	}

	name := args[0]
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	cmdFlag := fs.String("cmd", "", "Command to run")
	aliasFlag := fs.String("alias", "", "Local .localhost alias")
	portFlag := fs.Uint("port", 0, "Service port")
	cwdFlag := fs.String("cwd", "", "Working directory")
	autoPortFlag := fs.Bool("auto-port", false, "Automatically assign free port")
	autoStartFlag := fs.Bool("auto-start", false, "Start the service whenever protd starts")

	_ = fs.Parse(args[1:])

	if *cmdFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --cmd is required\n")
		os.Exit(1)
	}

	if *aliasFlag == "" {
		*aliasFlag = strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	}

	payload := storage.Service{
		Name:          name,
		Command:       *cmdFlag,
		Alias:         *aliasFlag,
		Port:          uint32(*portFlag),
		Cwd:           *cwdFlag,
		AutoPort:      *autoPortFlag,
		AutoStart:     *autoStartFlag,
		RestartPolicy: storage.RestartOnFailure,
	}

	var created registry.ServiceView
	if err := connect().call(http.MethodPost, "/v1/services", payload, &created, http.StatusCreated); err != nil {
		fatalf("Failed to register service: %v", err)
	}
	fmt.Printf("✓ Registered service %q (ID: %s)\n", created.Name, created.ID)
	fmt.Printf("  Alias:     http://%s.localhost\n", created.Alias)
	fmt.Printf("  Command:   %s\n", created.Command)
	fmt.Printf("  Run 'prot start %s' to start.\n", created.ID)
}

func cmdStart(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot start <id_or_alias>")
	}

	var view registry.ServiceView
	if err := connect().call(http.MethodPost, servicePath(args[0], "start"), nil, &view, http.StatusOK); err != nil {
		fatalf("Failed to start: %v", err)
	}

	fmt.Printf("✓ Started %q (status: %s, PID: %d)\n", view.Name, view.Status, view.PID)
	if view.CanonicalURL != "" {
		fmt.Printf("  URL: %s\n", view.CanonicalURL)
	}
}

func cmdStop(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot stop <id_or_alias>")
	}

	if err := connect().call(http.MethodPost, servicePath(args[0], "stop"), nil, nil, http.StatusOK); err != nil {
		fatalf("Failed to stop: %v", err)
	}
	fmt.Printf("✓ Stopped %q\n", args[0])
}

func cmdRestart(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot restart <id_or_alias>")
	}

	var view registry.ServiceView
	if err := connect().call(http.MethodPost, servicePath(args[0], "restart"), nil, &view, http.StatusOK); err != nil {
		fatalf("Failed to restart: %v", err)
	}
	fmt.Printf("✓ Restarted %q (PID: %d)\n", view.Name, view.PID)
}

func cmdRemove(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot rm <id_or_alias>")
	}

	if err := connect().call(http.MethodDelete, servicePath(args[0]), nil, nil, http.StatusNoContent); err != nil {
		fatalf("Failed to remove: %v", err)
	}
	fmt.Printf("✓ Removed service %q\n", args[0])
}

func cmdLogs(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot logs <id_or_alias> [-n <lines>] [-f]")
		os.Exit(1)
	}

	id := args[0]
	follow := false
	lines := 100

	for i := 1; i < len(args); i++ {
		if args[i] == "-f" || args[i] == "--follow" {
			follow = true
		} else if (args[i] == "-n" || args[i] == "--lines") && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				lines = n
			}
			i++
		}
	}

	client := connect()
	path := fmt.Sprintf("%s?limit=%d", servicePath(id, "logs"), lines)

	// Following streams indefinitely, so it must not be subject to the ordinary request timeout.
	var resp *http.Response
	var err error
	if follow {
		resp, err = client.stream(path + "&follow=true")
	} else {
		resp, err = client.request(http.MethodGet, path, nil)
	}
	if err != nil {
		fatalf("Logs request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fatalf("Failed: %v", readAPIError(resp))
	}

	if follow {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data: ") {
				var entry struct {
					Timestamp time.Time `json:"timestamp"`
					Stream    string    `json:"stream"`
					Message   string    `json:"message"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &entry); err == nil {
					fmt.Printf("[%s] %s\n", entry.Timestamp.Format("15:04:05"), entry.Message)
				}
			}
		}
		return
	}

	var data struct {
		Logs []struct {
			Timestamp time.Time `json:"timestamp"`
			Stream    string    `json:"stream"`
			Message   string    `json:"message"`
		} `json:"logs"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&data)

	for _, entry := range data.Logs {
		fmt.Printf("[%s] %s\n", entry.Timestamp.Format("15:04:05"), entry.Message)
	}
}

func cmdAdopt(args []string) {
	if len(args) < 1 {
		fatalf("Usage: prot adopt <discovery_id> [--alias <alias>] [--name <name>] [--cmd <cmd>]")
	}

	discID := args[0]
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	nameFlag := fs.String("name", "", "Service name")
	aliasFlag := fs.String("alias", "", "Service alias")
	cmdFlag := fs.String("cmd", "", "Launch command")
	_ = fs.Parse(args[1:])

	payload := map[string]string{"name": *nameFlag, "alias": *aliasFlag, "command": *cmdFlag}
	path := "/v1/discovery/" + url.PathEscape(discID) + "/adopt"

	var view registry.ServiceView
	if err := connect().call(http.MethodPost, path, payload, &view, http.StatusCreated); err != nil {
		fatalf("Adopt failed: %v", err)
	}
	fmt.Printf("✓ Adopted service %q (ID: %s)\n", view.Name, view.ID)
	fmt.Printf("  Alias: %s\n", view.CanonicalURL)
}

// cmdMCP runs the MCP stdio adapter. It is a thin bridge to the running daemon: all tool calls execute inside
// protd, so agents share services, logs and process control with the CLI and the desktop app.
func cmdMCP(args []string) {
	bridge := mcp.NewBridge(func() (mcp.Endpoint, error) {
		client, err := getDaemonClient()
		if err != nil {
			return mcp.Endpoint{}, err
		}
		return mcp.Endpoint{URL: fmt.Sprintf("http://%s/mcp", client.adminAddr), Token: client.token}, nil
	})

	// Fail early and visibly (stderr only, stdout is the protocol channel) if the daemon is not up. The bridge
	// keeps retrying per message, so an agent that started first still works once protd comes up.
	if _, err := getDaemonClient(); err != nil {
		fmt.Fprintf(os.Stderr, "prot mcp: %v\n", err)
		fmt.Fprintln(os.Stderr, "prot mcp: waiting for the daemon; start it with 'systemctl --user start protd'")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bridge.ServeStdio(ctx, os.Stdin, os.Stdout); err != nil && err != io.EOF && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "MCP bridge exited: %v\n", err)
		os.Exit(1)
	}
}

func cmdDiscover(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	var services []discovery.ProcessService

	// If daemon is running, retrieve cached discovery from daemon (excluding internal ports)
	if client, err := getDaemonClient(); err == nil {
		resp, err := client.request(http.MethodGet, "/v1/discovery", nil)
		if err == nil {
			defer resp.Body.Close()
			var data struct {
				Services []discovery.ProcessService `json:"services"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
				services = data.Services
			}
		}
	}

	if services == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cfg := discovery.DefaultConfig()
		var err error
		services, err = discovery.Discover(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Discovery error: %v\n", err)
			os.Exit(1)
		}
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(services)
		return
	}

	if len(services) == 0 {
		fmt.Println("No listening TCP development services discovered.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tPORT\tPID\tPROCESS\tHTTP\tFRAMEWORK/TITLE\tENDPOINT")
	_, _ = fmt.Fprintln(w, "--\t----\t---\t-------\t----\t---------------\t--------")

	for _, s := range services {
		httpStr := "no"
		if s.IsHTTP {
			httpStr = fmt.Sprintf("HTTP %d", s.HTTPStatus)
		}

		proc := s.Name
		if proc == "" && s.PID > 0 {
			proc = fmt.Sprintf("PID %d", s.PID)
		} else if proc == "" {
			proc = "-"
		}

		fwOrTitle := s.Framework
		if s.PageTitle != "" {
			if fwOrTitle != "" {
				fwOrTitle = fmt.Sprintf("%s (%s)", fwOrTitle, truncate(s.PageTitle, 25))
			} else {
				fwOrTitle = truncate(s.PageTitle, 30)
			}
		}
		if fwOrTitle == "" {
			fwOrTitle = "-"
		}

		endpoint := fmt.Sprintf("%s:%d", s.IP, s.Port)
		pidStr := "-"
		if s.PID > 0 {
			pidStr = strconv.Itoa(int(s.PID))
		}

		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Port, pidStr, proc, httpStr, fwOrTitle, endpoint)
	}
	_ = w.Flush()
}

func cmdStatus(args []string) {
	client, err := getDaemonClient()
	if err != nil {
		fmt.Printf("Daemon: Not running (%v)\n", err)
		return
	}

	resp, err := client.request(http.MethodGet, "/v1/health", nil)
	if err != nil {
		fmt.Printf("Daemon: Unreachable (%v)\n", err)
		return
	}
	defer resp.Body.Close()

	var health struct {
		Status      string `json:"status"`
		Version     string `json:"version"`
		PID         int    `json:"pid"`
		GatewayAddr string `json:"gateway_addr"`
		AdminAddr   string `json:"admin_addr"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&health)

	fmt.Printf("prot daemon (protd) is running:\n")
	fmt.Printf("  • Version:      %s\n", health.Version)
	fmt.Printf("  • PID:          %d\n", health.PID)
	fmt.Printf("  • Gateway URL:  http://%s\n", health.GatewayAddr)
	fmt.Printf("  • Admin API:    http://%s\n", health.AdminAddr)

	routesResp, err := client.request(http.MethodGet, "/v1/routes", nil)
	if err == nil {
		defer routesResp.Body.Close()
		var routesData struct {
			Routes []struct {
				Alias      string `json:"alias"`
				Host       string `json:"host"`
				TargetURL  string `json:"target_url"`
				LANExposed bool   `json:"lan_exposed"`
			} `json:"routes"`
		}
		if err := json.NewDecoder(routesResp.Body).Decode(&routesData); err == nil && len(routesData.Routes) > 0 {
			fmt.Printf("\nActive Routes (%d):\n", len(routesData.Routes))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			_, _ = fmt.Fprintln(w, "  ALIAS\tHOST\tUPSTREAM\tLAN")
			_, _ = fmt.Fprintln(w, "  -----\t----\t--------\t---")
			for _, r := range routesData.Routes {
				lan := "private"
				if r.LANExposed {
					lan = "exposed"
				}
				_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", r.Alias, r.Host, r.TargetURL, lan)
			}
			_ = w.Flush()
		}
	}
}

func cmdAlias(args []string) {
	if len(args) < 2 {
		fatalf("Usage: prot alias <alias> <upstream_url>\nExample: prot alias myapp http://localhost:3000")
	}

	alias := args[0]
	upstream := args[1]
	if !strings.HasPrefix(upstream, "http://") && !strings.HasPrefix(upstream, "https://") {
		upstream = "http://" + upstream
	}

	payload := map[string]any{"alias": alias, "target_url": upstream, "lan_exposed": false}
	if err := connect().call(http.MethodPost, "/v1/routes", payload, nil, http.StatusCreated); err != nil {
		fatalf("Failed to register alias: %v", err)
	}
	fmt.Printf("✓ Alias registered: http://%s.localhost -> %s\n", alias, upstream)
}

func cmdDoctor(args []string) {
	fmt.Printf("prot doctor — Diagnostic and Support Matrix Check\n\n")

	fmt.Printf("[1/6] Operating System:\n")
	fmt.Printf("      OS:           %s\n", runtime.GOOS)
	fmt.Printf("      Architecture: %s\n", runtime.GOARCH)
	fmt.Printf("      Go Version:   %s\n", runtime.Version())

	fmt.Printf("\n[2/6] Process Inspection:\n")
	pids, err := discovery.Discover(context.Background(), discovery.Config{CheckHTTP: false})
	if err != nil {
		fmt.Printf("      ✗ Socket inspection: failed (%v)\n", err)
	} else {
		fmt.Printf("      ✓ Socket inspection: OK (%d listening TCP sockets found)\n", len(pids))
	}

	fmt.Printf("\n[3/6] Port Binding Capabilities:\n")
	ln80, err := net.Listen("tcp", "127.0.0.1:80")
	if err != nil {
		fmt.Printf("      • Port 80 (standard HTTP): privileged / restricted (%v)\n", err)
		fmt.Printf("        (prot will use unprivileged gateway port 8080 by default)\n")
	} else {
		_ = ln80.Close()
		fmt.Printf("      ✓ Port 80 (standard HTTP): available without privilege escalation\n")
	}
	ln8080, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		fmt.Printf("      • Port 8080: currently in use (%v)\n", err)
	} else {
		_ = ln8080.Close()
		fmt.Printf("      ✓ Port 8080: available for unprivileged gateway\n")
	}

	fmt.Printf("\n[4/6] SSH Tunnel Capability:\n")
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Printf("      ✗ OpenSSH client: not found in PATH\n")
	} else {
		fmt.Printf("      ✓ OpenSSH client: found at %s\n", sshPath)
	}

	fmt.Printf("\n[5/6] Development Toolchain:\n")
	nodePath, err := exec.LookPath("node")
	if err == nil {
		out, _ := exec.Command(nodePath, "--version").Output()
		fmt.Printf("      ✓ Node.js: found (%s)\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Printf("      • Node.js: not found in PATH\n")
	}

	fmt.Printf("\n[6/6] Support Matrix Evaluation:\n")
	switch runtime.GOOS {
	case "linux", "darwin":
		fmt.Println("      Tier 1 (Full Support): All discovery, proxy, and tunnel features supported.")
	case "windows":
		fmt.Println("      Not supported: the supervisor and tunnels need a Unix-like OS for now.")
	default:
		fmt.Printf("      Untested platform: %s\n", runtime.GOOS)
	}

	fmt.Println("\nAll core M0/M1 engineering foundation checks completed.")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func cmdUpdate(args []string) {
	checkOnly := false
	force := false

	for _, a := range args {
		switch a {
		case "--check", "-c":
			checkOnly = true
		case "--force", "-f":
			force = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	owner := updater.PackageOwner(ctx)
	if owner != nil && !checkOnly {
		fmt.Fprintln(os.Stderr, owner.Message())
		fmt.Fprintln(os.Stderr, owner.UpdateHint())
		os.Exit(1)
	}

	fmt.Println("Checking for updates...")
	rel, hasUpdate, err := updater.CheckUpdate(ctx, "", daemon.Version)
	if err != nil {
		if errors.Is(err, updater.ErrNoReleases) {
			fmt.Printf("No official releases published yet (running development version %s).\n", daemon.Version)
			return
		}
		fmt.Fprintf(os.Stderr, "Error checking for updates: %v\n", err)
		os.Exit(1)
	}

	if checkOnly {
		if hasUpdate {
			fmt.Printf("Update available: %s (current: %s)\n", rel.TagName, daemon.Version)
			if owner != nil {
				fmt.Println(owner.Message())
				fmt.Println(owner.UpdateHint())
			} else {
				fmt.Println("Run 'prot update' to upgrade.")
			}
		} else {
			fmt.Printf("prot is up to date (version %s)\n", daemon.Version)
		}
		return
	}

	if !hasUpdate && !force {
		fmt.Printf("prot is already up to date (version %s)\n", daemon.Version)
		return
	}

	applyRelease(ctx, rel)
}

// applyRelease downloads, verifies and installs the release, then restarts the daemon. It exits on failure.
func applyRelease(ctx context.Context, rel *updater.ReleaseInfo) {
	fmt.Printf("Updating prot from %s to %s...\n", daemon.Version, rel.TagName)
	asset, err := updater.FindAssetForPlatform(rel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}

	wantSum, err := updater.ExpectedChecksum(ctx, rel, asset.Name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Downloading %s (%d bytes)...\n", asset.Name, asset.Size)
	protBytes, protdBytes, err := updater.DownloadAsset(ctx, asset.DownloadURL, wantSum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Checksum verified. Applying update in-place...")
	if err := updater.ApplyUpdate(protBytes, protdBytes, ""); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to apply update: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Restarting background daemon...")
	if err := updater.RestartDaemon(ctx); err != nil {
		fmt.Printf("Notice: could not automatically restart daemon: %v\n", err)
	}

	fmt.Printf("✓ Successfully updated prot to %s!\n", rel.TagName)
}
