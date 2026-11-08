package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// Server provides the REST API handler for the prot daemon.
type Server struct {
	reg         *registry.Registry
	router      *proxy.Router
	tunnelsMgr  *tunnels.Manager
	version     string
	gatewayAddr string
	adminAddr   string
	mux         *http.ServeMux
}

// Config configures the API server.
type Config struct {
	Registry    *registry.Registry
	Router      *proxy.Router
	Tunnels     *tunnels.Manager
	Version     string
	GatewayAddr string
	AdminAddr   string
}

// NewServer initializes the API routes and returns the server.
func NewServer(cfg Config) *Server {
	s := &Server{
		reg:         cfg.Registry,
		router:      cfg.Router,
		tunnelsMgr:  cfg.Tunnels,
		version:     cfg.Version,
		gatewayAddr: cfg.GatewayAddr,
		adminAddr:   cfg.AdminAddr,
		mux:         http.NewServeMux(),
	}

	s.routes()
	return s
}

// Handler returns the underlying http.Handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/events", s.handleEvents)

	// Services
	s.mux.HandleFunc("GET /v1/services", s.handleListServices)
	s.mux.HandleFunc("POST /v1/services", s.handleCreateService)
	s.mux.HandleFunc("GET /v1/services/{id}", s.handleGetService)
	s.mux.HandleFunc("DELETE /v1/services/{id}", s.handleDeleteService)
	s.mux.HandleFunc("POST /v1/services/{id}/start", s.handleStartService)
	s.mux.HandleFunc("POST /v1/services/{id}/stop", s.handleStopService)
	s.mux.HandleFunc("POST /v1/services/{id}/restart", s.handleRestartService)
	s.mux.HandleFunc("GET /v1/services/{id}/logs", s.handleGetLogs)

	// Discovery
	s.mux.HandleFunc("GET /v1/discovery", s.handleListDiscovery)
	s.mux.HandleFunc("POST /v1/discovery/{id}/adopt", s.handleAdoptDiscovery)

	// Routes
	s.mux.HandleFunc("GET /v1/routes", s.handleListRoutes)
	s.mux.HandleFunc("POST /v1/routes", s.handleCreateRoute)
	s.mux.HandleFunc("DELETE /v1/routes/{alias}", s.handleDeleteRoute)

	// Tunnels
	s.mux.HandleFunc("GET /v1/tunnels", s.handleListTunnels)
	s.mux.HandleFunc("POST /v1/tunnels", s.handleCreateTunnel)
	s.mux.HandleFunc("POST /v1/tunnels/{id}/start", s.handleStartTunnel)
	s.mux.HandleFunc("POST /v1/tunnels/{id}/stop", s.handleStopTunnel)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]interface{}{
		"status":       "ok",
		"version":      s.version,
		"pid":          os.Getpid(),
		"gateway_addr": s.gatewayAddr,
		"admin_addr":   s.adminAddr,
		"timestamp":    time.Now().UTC(),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	evCh, unsub := s.reg.Subscribe()
	defer unsub()

	// Initial ping
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-evCh:
			data, err := json.Marshal(ev)
			if err == nil {
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, string(data))
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	services, err := s.reg.ListServices(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{
		"services": services,
		"count":    len(services),
	})
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var svc storage.Service
	if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request json")
		return
	}

	view, err := s.reg.RegisterService(r.Context(), svc)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonCreated(w, view)
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc, err := s.reg.GetService(r.Context(), id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "service not found")
		return
	}
	jsonOK(w, svc)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reg.DeleteService(r.Context(), id); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := s.reg.StartService(r.Context(), id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleStopService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := s.reg.StopService(r.Context(), id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleRestartService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := s.reg.RestartService(r.Context(), id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	follow := r.URL.Query().Get("follow") == "true"
	if follow {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		ch, unsub, err := s.reg.FollowLogs(id)
		if err != nil {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		defer unsub()

		// Stream existing history first
		history, _ := s.reg.GetLogs(id, limit)
		for _, entry := range history {
			data, _ := json.Marshal(entry)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", string(data))
		}
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case entry, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(entry)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(data))
				flusher.Flush()
			}
		}
	}

	logs, err := s.reg.GetLogs(id, limit)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{
		"service_id": id,
		"logs":       logs,
		"count":      len(logs),
	})
}

func (s *Server) handleListDiscovery(w http.ResponseWriter, r *http.Request) {
	services := s.reg.ListDiscovered()
	jsonOK(w, map[string]interface{}{
		"services": services,
		"count":    len(services),
	})
}

func (s *Server) handleAdoptDiscovery(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name    string `json:"name"`
		Alias   string `json:"alias"`
		Command string `json:"command"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	view, err := s.reg.AdoptDiscovered(r.Context(), id, req.Name, req.Alias, req.Command)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonCreated(w, view)
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	routes := s.router.ListRoutes()
	jsonOK(w, map[string]interface{}{
		"routes": routes,
		"count":  len(routes),
	})
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Alias      string `json:"alias"`
		TargetURL  string `json:"target_url"`
		LANExposed bool   `json:"lan_exposed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request json")
		return
	}

	if err := s.router.SetRoute(req.Alias, req.TargetURL, req.LANExposed); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	rt, _ := s.router.GetRoute(req.Alias)
	jsonCreated(w, rt)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	alias := r.PathValue("alias")
	s.router.RemoveRoute(alias)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	var list []tunnels.TunnelStatus
	if s.tunnelsMgr != nil {
		list = s.tunnelsMgr.List()
	}
	jsonOK(w, map[string]interface{}{
		"tunnels": list,
		"count":   len(list),
	})
}

func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	if s.tunnelsMgr == nil {
		jsonErr(w, http.StatusNotImplemented, "ssh tunnels not supported in this environment")
		return
	}

	var cfg tunnels.TunnelConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request json")
		return
	}

	st, err := s.tunnelsMgr.Register(cfg)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if cfg.AutoConnect {
		_, _ = s.tunnelsMgr.Start(r.Context(), cfg.ID)
	}

	jsonCreated(w, st)
}

func (s *Server) handleStartTunnel(w http.ResponseWriter, r *http.Request) {
	if s.tunnelsMgr == nil {
		jsonErr(w, http.StatusNotImplemented, "ssh tunnels not supported in this environment")
		return
	}

	id := r.PathValue("id")
	st, err := s.tunnelsMgr.Start(r.Context(), id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, st)
}

func (s *Server) handleStopTunnel(w http.ResponseWriter, r *http.Request) {
	if s.tunnelsMgr == nil {
		jsonErr(w, http.StatusNotImplemented, "ssh tunnels not supported in this environment")
		return
	}

	id := r.PathValue("id")
	if err := s.tunnelsMgr.Stop(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func jsonCreated(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(data)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(code),
		"message": msg,
		"code":    code,
	})
}
