package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	case "discover":
		cmdDiscover(args)
	case "doctor":
		cmdDoctor(args)
	case "alias":
		cmdAlias(args)
	case "status":
		cmdStatus(args)
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

Core Commands:
  discover         Discover listening TCP processes and HTTP services
  alias            Map a .localhost alias to an upstream URL
  status           Check daemon status and registered routes
  doctor           Inspect environment, port permissions, and platform capabilities
  version          Print version information

Options:
  --json           Format output as JSON where supported

For full daemon service, run 'protd'.`)
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
		return nil, fmt.Errorf("daemon is not running (state file not found at %s)", statePath)
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

func cmdDiscover(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	// Try running discovery
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
	_, _ = fmt.Fprintln(w, "PORT\tPID\tPROCESS\tHTTP\tFRAMEWORK/TITLE\tENDPOINT")
	_, _ = fmt.Fprintln(w, "----\t---\t-------\t----\t---------------\t--------")

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

		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", s.Port, pidStr, proc, httpStr, fwOrTitle, endpoint)
	}
	_ = w.Flush()
}

func cmdStatus(args []string) {
	client, err := getDaemonClient()
	if err != nil {
		fmt.Printf("Daemon: Not running (%v)\n", err)
		return
	}

	// Fetch health
	resp, err := http.Get(fmt.Sprintf("http://%s/v1/health", client.adminAddr))
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

	// Fetch routes
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/v1/routes", client.adminAddr), nil)
	req.Header.Set("Authorization", "Bearer "+client.token)
	routesResp, err := http.DefaultClient.Do(req)
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

	payload, _ := json.Marshal(map[string]interface{}{
		"alias":       alias,
		"target_url":  upstream,
		"lan_exposed": false,
	})

	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/v1/routes", client.adminAddr), bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
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

	// 1. OS & Architecture
	fmt.Printf("[1/6] Operating System:\n")
	fmt.Printf("      OS:           %s\n", runtime.GOOS)
	fmt.Printf("      Architecture: %s\n", runtime.GOARCH)
	fmt.Printf("      Go Version:   %s\n", runtime.Version())

	// 2. Process inspection & permissions
	fmt.Printf("\n[2/6] Process Inspection:\n")
	pids, err := discovery.Discover(context.Background(), discovery.Config{CheckHTTP: false})
	if err != nil {
		fmt.Printf("      ✗ Socket inspection: failed (%v)\n", err)
	} else {
		fmt.Printf("      ✓ Socket inspection: OK (%d listening TCP sockets found)\n", len(pids))
	}

	// 3. Port binding capabilities
	fmt.Printf("\n[3/6] Port Binding Capabilities:\n")
	// Port 80
	ln80, err := net.Listen("tcp", "127.0.0.1:80")
	if err != nil {
		fmt.Printf("      • Port 80 (standard HTTP): privileged / restricted (%v)\n", err)
		fmt.Printf("        (prot will use unprivileged gateway port 8080 by default)\n")
	} else {
		_ = ln80.Close()
		fmt.Printf("      ✓ Port 80 (standard HTTP): available without privilege escalation\n")
	}
	// Port 8080
	ln8080, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		fmt.Printf("      • Port 8080: currently in use (%v)\n", err)
	} else {
		_ = ln8080.Close()
		fmt.Printf("      ✓ Port 8080: available for unprivileged gateway\n")
	}

	// 4. SSH binary for tunnels
	fmt.Printf("\n[4/6] SSH Tunnel Capability:\n")
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Printf("      ✗ OpenSSH client: not found in PATH\n")
	} else {
		fmt.Printf("      ✓ OpenSSH client: found at %s\n", sshPath)
	}

	// 5. Node.js & development toolchain
	fmt.Printf("\n[5/6] Development Toolchain:\n")
	nodePath, err := exec.LookPath("node")
	if err == nil {
		out, _ := exec.Command(nodePath, "--version").Output()
		fmt.Printf("      ✓ Node.js: found (%s)\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Printf("      • Node.js: not found in PATH\n")
	}

	// 6. Support matrix verdict
	fmt.Printf("\n[6/6] Support Matrix Evaluation:\n")
	switch runtime.GOOS {
	case "linux", "darwin":
		fmt.Println("      Tier 1 (Full Support): All discovery, proxy, and tunnel features supported.")
	case "windows":
		fmt.Println("      Tier 2 (Technical Preview): Core proxy supported; process discovery supported via gopsutil.")
	default:
		fmt.Printf("      Untested platform: %s\n", runtime.GOOS)
	}

	fmt.Println("\nAll core M0 engineering foundation checks completed.")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
