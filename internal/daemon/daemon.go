package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/terastal-hq/prot/internal/api"
	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/mcp"
	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/secrets"
	"github.com/terastal-hq/prot/internal/security"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// Version of the prot daemon.
const Version = "0.1.0"

// Config contains runtime configuration for protd.
type Config struct {
	DataDir     string `json:"data_dir"`
	GatewayAddr string `json:"gateway_addr"`       // Proxy listener e.g. 127.0.0.1:8080
	AdminAddr   string `json:"admin_addr"`         // Loopback API listener e.g. 127.0.0.1:4400
	SSHPath     string `json:"ssh_path,omitempty"` // ssh binary for tunnels; empty searches PATH

	// Secrets keeps tunnel passwords. Nil means the OS keyring.
	Secrets secrets.Store `json:"-"`
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

	// 3. Initialize OpenSSH Tunnel Manager. Without ssh the daemon still runs; tunnel calls say why they cannot.
	var tunMgr *tunnels.Manager
	if cfg.SSHPath != "" {
		tunMgr = tunnels.NewManagerWithSSH(cfg.SSHPath)
	} else if mgr, err := tunnels.NewManager(); err != nil {
		log.Printf("protd: SSH tunnels disabled: %v", err)
	} else {
		tunMgr = mgr
	}

	secretStore := cfg.Secrets
	if secretStore == nil {
		secretStore = secrets.Keyring{}
	}

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
		Secrets:     secretStore,
		GatewayHost: gwPort,
	})

	// 6. Initialize REST API Server & MCP Server
	apiSrv := api.NewServer(api.Config{
		Registry:    reg,
		Router:      router,
		Version:     Version,
		GatewayAddr: cfg.GatewayAddr,
		AdminAddr:   cfg.AdminAddr,
	})

	mcpSrv := mcp.NewServer(reg)
	mcpSrv.SetVersion(Version)

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
			if err := d.router.SetRoute(s.Alias, netutil.LoopbackURL(s.Port), s.LANExposed); err != nil {
				log.Printf("protd: restoring route %q for %q: %v", s.Alias, s.Name, err)
			}
		}

		// Auto-start services if configured. A failure must not stop the others, but it must be visible.
		if s.AutoStart && s.Command != "" {
			if _, err := d.reg.StartService(ctx, s.ID); err != nil {
				log.Printf("protd: autostart of %q failed: %v", s.Name, err)
			}
		}
	}

	// Saved SSH tunnels: routes first, then reconnect the ones marked auto_connect.
	d.reg.RestoreTunnels(ctx)
	return nil
}

// Stop gracefully shuts down all daemon components. It always tries every step and returns the failures joined.
func (d *Daemon) Stop(ctx context.Context) error {
	var errs []error

	if d.discCancel != nil {
		d.discCancel()
	}
	if d.discTickerDone != nil {
		<-d.discTickerDone
	}

	// Gracefully stop all running supervised child processes, and every ssh tunnel so none outlives the daemon
	if d.reg != nil {
		d.reg.StopAll(2 * time.Second)
		d.reg.StopAllTunnels()
	}

	if d.adminSrv != nil {
		if err := d.adminSrv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping admin API: %w", err))
		}
	}
	if d.proxySrv != nil {
		if err := d.proxySrv.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping proxy: %w", err))
		}
	}
	if d.db != nil {
		if err := d.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing database: %w", err))
		}
	}

	// Remove the state file so clients do not think a dead daemon is running.
	statePath := filepath.Join(d.cfg.DataDir, "daemon.json")
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("removing state file: %w", err))
	}

	return errors.Join(errs...)
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

// portOf extracts the numeric port from a host:port address.
func portOf(addr string) (uint32, bool) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0, false
	}
	return uint32(port), true
}

func (d *Daemon) runDiscovery(ctx context.Context) {
	// Prot's own listeners are not development services.
	ignorePorts := []uint32{}
	for _, addr := range []string{d.ProxyAddr(), d.AdminAddr()} {
		if port, ok := portOf(addr); ok {
			ignorePorts = append(ignorePorts, port)
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
