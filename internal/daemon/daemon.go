package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/terastal-hq/prot/internal/api"
	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/mcp"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/security"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// Version of the prot daemon.
const Version = "0.1.0"

// Config contains runtime configuration for protd.
type Config struct {
	DataDir     string `json:"data_dir"`
	GatewayAddr string `json:"gateway_addr"` // Proxy listener e.g. 127.0.0.1:8080
	AdminAddr   string `json:"admin_addr"`   // Loopback API listener e.g. 127.0.0.1:4400
}

// DefaultConfig returns standard paths and loopback listeners.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:     filepath.Join(home, ".prot"),
		GatewayAddr: "127.0.0.1:8080",
		AdminAddr:   "127.0.0.1:4400",
	}
}

// Daemon represents the long-running prot background daemon.
type Daemon struct {
	cfg        Config
	db         *storage.DB
	auth       *security.Authenticator
	router     *proxy.Router
	proxySrv   *proxy.Server
	tunnelsMgr *tunnels.Manager
	reg        *registry.Registry
	apiSrv     *api.Server
	mcpSrv     *mcp.Server
	adminSrv   *http.Server
	adminLn    net.Listener

	mu             sync.RWMutex
	discCancel     context.CancelFunc
	discTickerDone chan struct{}
}

// New constructs and initializes a new Daemon instance.
func New(cfg Config) (*Daemon, error) {
	if cfg.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("user home dir: %w", err)
		}
		cfg.DataDir = filepath.Join(home, ".prot")
	}

	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	// 1. Initialize SQLite Database (WAL mode)
	dbPath := filepath.Join(cfg.DataDir, "prot.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database at %q: %w", dbPath, err)
	}

	// 2. Initialize Security Authenticator
	tokenPath := filepath.Join(cfg.DataDir, "auth.token")
	auth, err := security.NewAuthenticator(security.Config{TokenPath: tokenPath})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initializing security: %w", err)
	}

	// 3. Initialize OpenSSH Tunnel Manager
	tunMgr, _ := tunnels.NewManager()

	// 4. Initialize Dynamic Reverse Proxy Router & Server
	router := proxy.NewRouter()
	proxySrv := proxy.NewServer(router, proxy.ServerOptions{
		ListenAddr: cfg.GatewayAddr,
	})

	// Extract gateway port for canonical URLs
	_, gwPort, _ := net.SplitHostPort(cfg.GatewayAddr)
	if gwPort == "" {
		gwPort = "8080"
	}

	// 5. Initialize Central Unified Registry
	reg := registry.New(registry.Config{
		DB:          db,
		Router:      router,
		Tunnels:     tunMgr,
		GatewayHost: gwPort,
	})

	// 6. Initialize REST API Server & MCP Server
	apiSrv := api.NewServer(api.Config{
		Registry:    reg,
		Router:      router,
		Tunnels:     tunMgr,
		Version:     Version,
		GatewayAddr: cfg.GatewayAddr,
		AdminAddr:   cfg.AdminAddr,
	})

	mcpSrv := mcp.NewServer(reg)

	d := &Daemon{
		cfg:        cfg,
		db:         db,
		auth:       auth,
		router:     router,
		proxySrv:   proxySrv,
		tunnelsMgr: tunMgr,
		reg:        reg,
		apiSrv:     apiSrv,
		mcpSrv:     mcpSrv,
	}

	return d, nil
}

// Start launches the reverse proxy, admin management API, and discovery worker.
func (d *Daemon) Start(ctx context.Context) error {
	// 1. Start Reverse Proxy
	if err := d.proxySrv.Start(); err != nil {
		return fmt.Errorf("starting proxy server: %w", err)
	}

	// 2. Recover persisted services and routes from SQLite
	if err := d.recoverState(ctx); err != nil {
		// Log but don't prevent startup
		fmt.Fprintf(os.Stderr, "Warning during state recovery: %v\n", err)
	}

	// 3. Setup loopback Admin Management API (combining REST API and /mcp)
	adminMux := http.NewServeMux()

	// Mount REST API
	adminMux.Handle("/v1/", d.apiSrv.Handler())

	// Mount Streamable HTTP MCP
	adminMux.HandleFunc("POST /mcp", d.mcpSrv.ServeHTTP)

	// Apply loopback enforcement, DNS rebinding protection, and token authentication
	securedHandler := d.auth.Middleware(adminMux)

	adminLn, err := net.Listen("tcp", d.cfg.AdminAddr)
	if err != nil {
		_ = d.proxySrv.Close(ctx)
		return fmt.Errorf("admin listener failed on %s: %w", d.cfg.AdminAddr, err)
	}
	d.adminLn = adminLn
	d.cfg.AdminAddr = adminLn.Addr().String()

	d.adminSrv = &http.Server{
		Handler: securedHandler,
	}

	go func() {
		_ = d.adminSrv.Serve(adminLn)
	}()

	// 4. Write daemon state metadata file (pid, ports, token) for clients
	if err := d.writeStateFile(); err != nil {
		_ = d.Stop(ctx)
		return fmt.Errorf("writing daemon state file: %w", err)
	}

	// 5. Start background discovery ticker
	discCtx, cancel := context.WithCancel(ctx)
	d.discCancel = cancel
	d.discTickerDone = make(chan struct{})
	go d.discoveryLoop(discCtx)

	return nil
}

func (d *Daemon) recoverState(ctx context.Context) error {
	savedServices, err := d.db.ListServices(ctx)
	if err != nil {
		return err
	}

	for _, s := range savedServices {
		// Re-register route in proxy router if port and alias exist
		if s.Alias != "" && s.Port > 0 {
			targetURL := fmt.Sprintf("http://127.0.0.1:%d", s.Port)
			_ = d.router.SetRoute(s.Alias, targetURL, s.LANExposed)
		}

		// Auto-start services if configured
		if s.AutoStart && s.Command != "" {
			_, _ = d.reg.StartService(ctx, s.ID)
		}
	}
	return nil
}

// Stop gracefully shuts down all daemon components.
func (d *Daemon) Stop(ctx context.Context) error {
	if d.discCancel != nil {
		d.discCancel()
	}
	if d.discTickerDone != nil {
		<-d.discTickerDone
	}

	// Gracefully stop all running supervised child processes
	if d.reg != nil {
		d.reg.StopAll(2 * time.Second)
	}

	if d.adminSrv != nil {
		_ = d.adminSrv.Shutdown(ctx)
	}

	if d.proxySrv != nil {
		_ = d.proxySrv.Close(ctx)
	}

	if d.db != nil {
		_ = d.db.Close()
	}

	// Remove state file
	statePath := filepath.Join(d.cfg.DataDir, "daemon.json")
	_ = os.Remove(statePath)

	return nil
}

// Router returns the dynamic proxy router.
func (d *Daemon) Router() *proxy.Router {
	return d.router
}

// Registry returns the unified registry.
func (d *Daemon) Registry() *registry.Registry {
	return d.reg
}

// ProxyAddr returns the bound proxy address.
func (d *Daemon) ProxyAddr() string {
	return d.proxySrv.Addr()
}

// AdminAddr returns the bound admin API address.
func (d *Daemon) AdminAddr() string {
	if d.adminLn != nil {
		return d.adminLn.Addr().String()
	}
	return d.cfg.AdminAddr
}

// Token returns the current auth token.
func (d *Daemon) Token() string {
	return d.auth.Token()
}

func (d *Daemon) discoveryLoop(ctx context.Context) {
	defer close(d.discTickerDone)

	// Initial immediate discovery run
	d.runDiscovery(ctx)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runDiscovery(ctx)
		}
	}
}

func (d *Daemon) runDiscovery(ctx context.Context) {
	ignorePorts := []uint32{}
	if _, portStr, err := net.SplitHostPort(d.ProxyAddr()); err == nil {
		var p uint32
		if _, err := fmt.Sscanf(portStr, "%d", &p); err == nil {
			ignorePorts = append(ignorePorts, p)
		}
	}
	if _, portStr, err := net.SplitHostPort(d.AdminAddr()); err == nil {
		var p uint32
		if _, err := fmt.Sscanf(portStr, "%d", &p); err == nil {
			ignorePorts = append(ignorePorts, p)
		}
	}

	cfg := discovery.DefaultConfig()
	cfg.IgnorePorts = ignorePorts
	cfg.ExcludePids = []int32{int32(os.Getpid())}

	services, err := discovery.Discover(ctx, cfg)
	if err != nil {
		return
	}

	d.reg.SetDiscovered(services)
}

type daemonState struct {
	PID         int       `json:"pid"`
	GatewayAddr string    `json:"gateway_addr"`
	AdminAddr   string    `json:"admin_addr"`
	Version     string    `json:"version"`
	StartedAt   time.Time `json:"started_at"`
}

func (d *Daemon) writeStateFile() error {
	state := daemonState{
		PID:         os.Getpid(),
		GatewayAddr: d.ProxyAddr(),
		AdminAddr:   d.AdminAddr(),
		Version:     Version,
		StartedAt:   time.Now().UTC(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	statePath := filepath.Join(d.cfg.DataDir, "daemon.json")
	return os.WriteFile(statePath, data, 0600)
}
