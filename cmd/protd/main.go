package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/terastal-hq/prot/internal/askpass"
	"github.com/terastal-hq/prot/internal/daemon"
)

func main() {
	// ssh runs protd as its askpass helper for tunnels with a stored password (see package askpass). That is a
	// one-shot job: answer the prompt and exit instead of starting a daemon.
	if askpass.Requested(os.Getenv) {
		os.Exit(askpass.Run(os.Args[1:], os.Getenv, os.Stdout))
	}

	gatewayAddr := flag.String("gateway", "127.0.0.1:8080", "Proxy gateway address to listen on")
	adminAddr := flag.String("admin", "127.0.0.1:4400", "Loopback management API address")
	dataDir := flag.String("data-dir", "", "Configuration and state directory (default $PROT_DATA_DIR, then ~/.prot)")
	sshConfig := flag.String("ssh-config", "", "ssh client configuration to read machine names from (default ~/.ssh/config)")
	versionFlag := flag.Bool("version", false, "Print daemon version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("protd version %s\n", daemon.Version)
		os.Exit(0)
	}

	cfg := daemon.DefaultConfig()
	if *gatewayAddr != "" {
		cfg.GatewayAddr = *gatewayAddr
	}
	if *adminAddr != "" {
		cfg.AdminAddr = *adminAddr
	}
	// Same precedence the CLI uses, so prot and protd always agree on where state lives:
	// the -data-dir flag, then PROT_DATA_DIR, then ~/.prot.
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	} else if env := os.Getenv("PROT_DATA_DIR"); env != "" {
		cfg.DataDir = env
	}

	cfg.SSHConfigPath = *sshConfig

	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatalf("protd initialization error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if err := d.Start(ctx); err != nil {
		log.Fatalf("protd failed to start: %v", err)
	}

	fmt.Printf("prot daemon (protd) v%s started\n", daemon.Version)
	fmt.Printf("  • Gateway Reverse Proxy: http://%s\n", d.ProxyAddr())
	fmt.Printf("  • Management Loopback API: http://%s\n", d.AdminAddr())
	fmt.Printf("  • PID: %d\n", os.Getpid())
	fmt.Println("Press Ctrl+C to terminate.")

	sig := <-sigChan
	fmt.Printf("\nReceived %s, shutting down gracefully...\n", sig)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	if err := d.Stop(stopCtx); err != nil {
		log.Printf("error during shutdown: %v", err)
	}
	fmt.Println("protd stopped.")
}
