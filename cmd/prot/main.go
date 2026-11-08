package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/terastal-hq/prot/internal/daemon"
	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/mcp"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
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
	case "status":
		cmdStatus(args)
	case "doctor":
		cmdDoctor(args)
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
  alias <aort> <u> Map a .localhost alias to an upstream target URL
  status           Show protd daemon health and active routes

Integrations & Diagnostics:
  mcp              Run Model Context Protocol (MCP) stdio adapter
  doctor           Check environment, permissions, and toolchain
  version          Print version information

Options:
  --json           Format output as JSON where supported`)
}

type daemonClient struct {
	adminAddr string
	token     string
}

func getDaemonClient() (*daemonClient, error) {
	dataDir := os.Getenv("PROT_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(home, ".prot")
	}

	statePath := filepath.Join(dataDir, "daemon.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("daemon is not running (start with 'protd')")
	}

	var state struct {
		PID       int    `json:"pid"`
		AdminAddr string `json:"admin_addr"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("corrupt daemon state file: %w", err)
	}

	tokenPath := filepath.Join(dataDir, "auth.token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("reading auth token: %w", err)
	}

	return &daemonClient{
		adminAddr: state.AdminAddr,
		token:     strings.TrimSpace(string(tokenBytes)),
	}, nil
}

func (c *daemonClient) request(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	url := fmt.Sprintf("http://%s%s", c.adminAddr, path)
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return http.DefaultClient.Do(req)
}

func cmdList(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.request(http.MethodGet, "/v1/services", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Daemon request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var data struct {
		Services []registry.ServiceView `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "Decoding error: %v\n", err)
		os.Exit(1)
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
		case "running":
			statusBadge = "● running"
		case "stopped":
			statusBadge = "○ stopped"
		case "starting":
			statusBadge = "◌ starting"
		case "crashed":
			statusBadge = "✕ crashed"
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", statusBadge, s.Name, aliasStr, portStr, pidStr, urlStr)
	}
	_ = w.Flush()
}

func cmdInspect(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot inspect <id_or_alias>")
		os.Exit(1)
	}
	target := args[0]

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.request(http.MethodGet, "/v1/services/"+target, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Service %q not found\n", target)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var svc registry.ServiceView
	_ = json.NewDecoder(resp.Body).Decode(&svc)

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

	_ = fs.Parse(args[1:])

	if *cmdFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --cmd is required\n")
		os.Exit(1)
	}

	if *aliasFlag == "" {
		*aliasFlag = strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	}

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	payload := storage.Service{
		Name:          name,
		Command:       *cmdFlag,
		Alias:         *aliasFlag,
		Port:          uint32(*portFlag),
		Cwd:           *cwdFlag,
		AutoPort:      *autoPortFlag,
		RestartPolicy: "on-failure",
	}

	resp, err := client.request(http.MethodPost, "/v1/services", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register service: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		var created registry.ServiceView
		_ = json.NewDecoder(resp.Body).Decode(&created)
		fmt.Printf("✓ Registered service %q (ID: %s)\n", created.Name, created.ID)
		fmt.Printf("  Alias:     http://%s.localhost\n", created.Alias)
		fmt.Printf("  Command:   %s\n", created.Command)
		fmt.Printf("  Run 'prot start %s' to start.\n", created.ID)
	} else {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed: %s\n", string(b))
		os.Exit(1)
	}
}

func cmdStart(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot start <id_or_alias>")
		os.Exit(1)
	}
	id := args[0]

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.request(http.MethodPost, fmt.Sprintf("/v1/services/%s/start", id), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var view registry.ServiceView
		_ = json.NewDecoder(resp.Body).Decode(&view)
		fmt.Printf("✓ Started %q (status: %s, PID: %d)\n", view.Name, view.Status, view.PID)
		if view.CanonicalURL != "" {
			fmt.Printf("  URL: %s\n", view.CanonicalURL)
		}
	} else {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed to start: %s\n", string(b))
		os.Exit(1)
	}
}

func cmdStop(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot stop <id_or_alias>")
		os.Exit(1)
	}
	id := args[0]

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.request(http.MethodPost, fmt.Sprintf("/v1/services/%s/stop", id), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		fmt.Printf("✓ Stopped %q\n", id)
	} else {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed to stop: %s\n", string(b))
		os.Exit(1)
	}
}

func cmdRestart(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot restart <id_or_alias>")
		os.Exit(1)
	}
	id := args[0]

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.request(http.MethodPost, fmt.Sprintf("/v1/services/%s/restart", id), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var view registry.ServiceView
		_ = json.NewDecoder(resp.Body).Decode(&view)
		fmt.Printf("✓ Restarted %q (PID: %d)\n", view.Name, view.PID)
	} else {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed to restart: %s\n", string(b))
		os.Exit(1)
	}
}

func cmdRemove(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prot rm <id_or_alias>")
		os.Exit(1)
	}
	id := args[0]

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.request(http.MethodDelete, fmt.Sprintf("/v1/services/%s", id), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		fmt.Printf("✓ Removed service %q\n", id)
	} else {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed to remove: %s\n", string(b))
		os.Exit(1)
	}
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

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	path := fmt.Sprintf("/v1/services/%s/logs?limit=%d", id, lines)
	if follow {
		path += "&follow=true"
	}

	resp, err := client.request(http.MethodGet, path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Logs request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed: %s\n", string(b))
		os.Exit(1)
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
		fmt.Println("Usage: prot adopt <discovery_id> [--alias <alias>] [--name <name>] [--cmd <cmd>]")
		os.Exit(1)
	}

	discID := args[0]
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	nameFlag := fs.String("name", "", "Service name")
	aliasFlag := fs.String("alias", "", "Service alias")
	cmdFlag := fs.String("cmd", "", "Launch command")
	_ = fs.Parse(args[1:])

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	payload := map[string]string{
		"name":    *nameFlag,
		"alias":   *aliasFlag,
		"command": *cmdFlag,
	}

	resp, err := client.request(http.MethodPost, fmt.Sprintf("/v1/discovery/%s/adopt", discID), payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Adopt failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		var view registry.ServiceView
		_ = json.NewDecoder(resp.Body).Decode(&view)
		fmt.Printf("✓ Adopted service %q (ID: %s)\n", view.Name, view.ID)
		fmt.Printf("  Alias: %s\n", view.CanonicalURL)
	} else {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Adopt failed: %s\n", string(b))
		os.Exit(1)
	}
}

func cmdMCP(args []string) {
	// If daemon is running, start MCP server hooked to local registry
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed getting home: %v\n", err)
		os.Exit(1)
	}

	dataDir := os.Getenv("PROT_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".prot")
	}

	dbPath := filepath.Join(dataDir, "prot.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open prot database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	router := proxy.NewRouter()
	reg := registry.New(registry.Config{
		DB:          db,
		Router:      router,
		GatewayHost: "8080",
	})

	srv := mcp.NewServer(reg)
	ctx := context.Background()
	if err := srv.ServeStdio(ctx, os.Stdin, os.Stdout); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "MCP server exited: %v\n", err)
	}
}

func cmdDiscover(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := discovery.DefaultConfig()
	services, err := discovery.Discover(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Discovery error: %v\n", err)
		os.Exit(1)
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

	var health map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&health)

	fmt.Printf("prot daemon (protd) is running:\n")
	fmt.Printf("  • Version:      %v\n", health["version"])
	fmt.Printf("  • PID:          %v\n", health["pid"])
	fmt.Printf("  • Gateway URL:  http://%v\n", health["gateway_addr"])
	fmt.Printf("  • Admin API:    http://%v\n", health["admin_addr"])

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
		fmt.Println("Usage: prot alias <alias> <upstream_url>")
		fmt.Println("Example: prot alias myapp http://localhost:3000")
		os.Exit(1)
	}

	alias := args[0]
	upstream := args[1]
	if !strings.HasPrefix(upstream, "http://") && !strings.HasPrefix(upstream, "https://") {
		upstream = "http://" + upstream
	}

	client, err := getDaemonClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\nStart the daemon with 'protd' first.\n", err)
		os.Exit(1)
	}

	payload := map[string]interface{}{
		"alias":       alias,
		"target_url":  upstream,
		"lan_exposed": false,
	}

	resp, err := client.request(http.MethodPost, "/v1/routes", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		fmt.Printf("✓ Alias registered: http://%s.localhost -> %s\n", alias, upstream)
	} else {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed to register alias: %s\n", string(body))
		os.Exit(1)
	}
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
		fmt.Println("      Tier 2 (Technical Preview): Core proxy supported; process discovery supported via gopsutil.")
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
