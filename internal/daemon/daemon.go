package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/security"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// Version of the prot daemon.
const Version = "0.1.0-m0"

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
	auth       *security.Authenticator
	router     *proxy.Router
	proxySrv   *proxy.Server
	tunnelsMgr *tunnels.Manager
	adminSrv   *http.Server
	adminLn    net.Listener

	mu             sync.RWMutex
	discovered     []discovery.ProcessService
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

	tokenPath := filepath.Join(cfg.DataDir, "auth.token")
	auth, err := security.NewAuthenticator(security.Config{TokenPath: tokenPath})
	if err != nil {
		return nil, fmt.Errorf("initializing security: %w", err)
	}

	tunMgr, err := tunnels.NewManager()
	if err != nil {
		// Log or tolerate if ssh binary not found in restricted environments
		tunMgr = nil
	}

	router := proxy.NewRouter()
	proxySrv := proxy.NewServer(router, proxy.ServerOptions{
		ListenAddr: cfg.GatewayAddr,
	})

	d := &Daemon{
		cfg:        cfg,
		auth:       auth,
		router:     router,
		proxySrv:   proxySrv,
		tunnelsMgr: tunMgr,
	}

	return d, nil
}

// Start launches the reverse proxy, admin management API, and discovery worker.
func (d *Daemon) Start(ctx context.Context) error {
	// 1. Start Reverse Proxy
	if err := d.proxySrv.Start(); err != nil {
		return fmt.Errorf("starting proxy server: %w", err)
	}

	// 2. Setup loopback Admin Management API
	adminMux := http.NewServeMux()
	d.registerAdminRoutes(adminMux)

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

	// 3. Write daemon state metadata file (pid, ports, token path) for clients
	if err := d.writeStateFile(); err != nil {
		_ = d.Stop(ctx)
		return fmt.Errorf("writing daemon state file: %w", err)
	}

	// 4. Start background discovery ticker
	discCtx, cancel := context.WithCancel(ctx)
	d.discCancel = cancel
	d.discTickerDone = make(chan struct{})
	go d.discoveryLoop(discCtx)

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

	if d.adminSrv != nil {
		_ = d.adminSrv.Shutdown(ctx)
	}

	if d.proxySrv != nil {
		_ = d.proxySrv.Close(ctx)
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

func (d *Daemon) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "ok",
			"version":      Version,
			"pid":          os.Getpid(),
			"gateway_addr": d.ProxyAddr(),
			"admin_addr":   d.AdminAddr(),
			"timestamp":    time.Now().UTC(),
		})
	})

	mux.HandleFunc("GET /v1/discovery", func(w http.ResponseWriter, r *http.Request) {
		d.mu.RLock()
		services := d.discovered
		d.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"services": services,
			"count":    len(services),
		})
	})

	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, r *http.Request) {
		routes := d.router.ListRoutes()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"routes": routes,
			"count":  len(routes),
		})
	})

	mux.HandleFunc("POST /v1/routes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Alias      string `json:"alias"`
			TargetURL  string `json:"target_url"`
			LANExposed bool   `json:"lan_exposed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
			return
		}
		if err := d.router.SetRoute(req.Alias, req.TargetURL, req.LANExposed); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
			return
		}
		rt, _ := d.router.GetRoute(req.Alias)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(rt)
	})

	mux.HandleFunc("DELETE /v1/routes/{alias}", func(w http.ResponseWriter, r *http.Request) {
		alias := strings.TrimPrefix(r.URL.Path, "/v1/routes/")
		d.router.RemoveRoute(alias)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /v1/tunnels", func(w http.ResponseWriter, r *http.Request) {
		var list []tunnels.TunnelStatus
		if d.tunnelsMgr != nil {
			list = d.tunnelsMgr.List()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"tunnels": list,
			"count":   len(list),
		})
	})
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
	// Exclude our own daemon listeners from discovery
	var ignorePorts []uint32
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

	d.mu.Lock()
	d.discovered = services
	d.mu.Unlock()
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
