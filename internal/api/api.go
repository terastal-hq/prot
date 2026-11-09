package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/supervisor"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// Server provides the REST API handler for the prot daemon.
type Server struct {
	reg         *registry.Registry
	router      *proxy.Router
	version     string
	gatewayAddr string
	adminAddr   string
	sshPath     string
	sshConfig   string
	mux         *http.ServeMux
}

// Config configures the API server.
type Config struct {
	Registry    *registry.Registry
	Router      *proxy.Router
	Version     string
	GatewayAddr string
	AdminAddr   string
	// SSHPath is the ssh binary used to resolve machine names; empty disables resolving.
	SSHPath string
	// SSHConfigPath is the ssh client configuration to read machine names from; empty means ~/.ssh/config.
	SSHConfigPath string
}

// NewServer initializes the API routes and returns the server.
func NewServer(cfg Config) *Server {
	s := &Server{
		reg:         cfg.Registry,
		router:      cfg.Router,
		version:     cfg.Version,
		gatewayAddr: cfg.GatewayAddr,
		adminAddr:   cfg.AdminAddr,
		sshPath:     cfg.SSHPath,
		sshConfig:   cfg.SSHConfigPath,
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
	s.mux.HandleFunc("PATCH /v1/services/{id}", s.handleUpdateService)
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
	s.mux.HandleFunc("DELETE /v1/tunnels/{id}", s.handleDeleteTunnel)
	s.mux.HandleFunc("PUT /v1/tunnels/{id}/password", s.handleSetTunnelPassword)
	s.mux.HandleFunc("DELETE /v1/tunnels/{id}/password", s.handleClearTunnelPassword)

	// Machines defined in the user's ssh configuration
	s.mux.HandleFunc("GET /v1/ssh/hosts", s.handleListSSHHosts)
	s.mux.HandleFunc("GET /v1/ssh/hosts/{alias}", s.handleGetSSHHost)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"status":       "ok",
		"version":      s.version,
		"pid":          os.Getpid(),
		"gateway_addr": s.gatewayAddr,
		"admin_addr":   s.adminAddr,
		"timestamp":    time.Now().UTC(),
	})
}

// startSSE switches the response to a server-sent event stream and returns its flusher. It writes the error
// response itself and returns false when streaming is not supported.
func startSSE(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return nil, false
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return flusher, true
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := startSSE(w)
	if !ok {
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
	jsonOK(w, map[string]any{
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

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	var patch registry.ServicePatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request json")
		return
	}

	view, err := s.reg.UpdateService(r.Context(), r.PathValue("id"), patch)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		jsonErr(w, http.StatusNotFound, "service not found")
	case errors.Is(err, registry.ErrInvalidAlias), errors.Is(err, registry.ErrInvalidField):
		jsonErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, registry.ErrAliasTaken):
		jsonErr(w, http.StatusConflict, err.Error())
	case err != nil:
		jsonErr(w, http.StatusInternalServerError, err.Error())
	default:
		jsonOK(w, view)
	}
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
	if errors.Is(err, storage.ErrNotFound) {
		jsonErr(w, http.StatusNotFound, "service not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleStopService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := s.reg.StopService(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		jsonErr(w, http.StatusNotFound, "service not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleRestartService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := s.reg.RestartService(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		jsonErr(w, http.StatusNotFound, "service not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}

	if r.URL.Query().Get("follow") == "true" {
		s.streamLogs(w, r, id, limit)
		return
	}

	logs, err := s.reg.GetLogs(r.Context(), id, limit)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"service_id": id,
		"logs":       logs,
		"count":      len(logs),
	})
}

// streamLogs sends the recent history and then live lines as server-sent events until the client goes away.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, id string, limit int) {
	// Subscribe before streaming anything, and take history in the same step, so no line is lost or repeated.
	history, ch, unsub, err := s.reg.FollowLogs(r.Context(), id, limit)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	defer unsub()

	flusher, ok := startSSE(w)
	if !ok {
		return
	}

	for _, entry := range history {
		writeLogEvent(w, entry)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case entry, open := <-ch:
			if !open {
				return
			}
			writeLogEvent(w, entry)
			flusher.Flush()
		}
	}
}

func writeLogEvent(w http.ResponseWriter, entry supervisor.LogEntry) {
	if data, err := json.Marshal(entry); err == nil {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	}
}

func (s *Server) handleListDiscovery(w http.ResponseWriter, r *http.Request) {
	services := s.reg.ListDiscovered()
	jsonOK(w, map[string]any{
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

	view, err := s.reg.AdoptDiscovered(r.Context(), registry.AdoptOptions{
		DiscoveryID: id,
		Name:        req.Name,
		Alias:       req.Alias,
		Command:     req.Command,
	})
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonCreated(w, view)
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	routes := s.router.ListRoutes()
	jsonOK(w, map[string]any{
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

// writeTunnelError maps a tunnel operation's error to an HTTP status.
func writeTunnelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tunnels.ErrNotFound):
		jsonErr(w, http.StatusNotFound, "tunnel not found")
	case errors.Is(err, registry.ErrTunnelsUnavailable), errors.Is(err, registry.ErrPasswordsUnavailable):
		jsonErr(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, registry.ErrInvalidAlias), errors.Is(err, registry.ErrInvalidField):
		jsonErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, registry.ErrAliasTaken):
		jsonErr(w, http.StatusConflict, err.Error())
	default:
		jsonErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	list := s.reg.ListTunnels()
	jsonOK(w, map[string]any{
		"tunnels": list,
		"count":   len(list),
	})
}

// createTunnelRequest is a tunnel definition plus an optional write-only password (or key passphrase). The
// password goes to the OS keyring and is never returned by any endpoint.
type createTunnelRequest struct {
	tunnels.TunnelConfig
	Password string `json:"password,omitempty"`
}

func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req createTunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request json")
		return
	}

	view, err := s.reg.RegisterTunnelWithPassword(r.Context(), req.TunnelConfig, req.Password)
	if err != nil {
		writeTunnelError(w, err)
		return
	}
	jsonCreated(w, view)
}

func (s *Server) handleStartTunnel(w http.ResponseWriter, r *http.Request) {
	view, err := s.reg.StartTunnel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTunnelError(w, err)
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleStopTunnel(w http.ResponseWriter, r *http.Request) {
	view, err := s.reg.StopTunnel(r.PathValue("id"))
	if err != nil {
		writeTunnelError(w, err)
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleSetTunnelPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request json")
		return
	}
	view, err := s.reg.SetTunnelPassword(r.Context(), r.PathValue("id"), body.Password)
	if err != nil {
		writeTunnelError(w, err)
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleClearTunnelPassword(w http.ResponseWriter, r *http.Request) {
	view, err := s.reg.ClearTunnelPassword(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTunnelError(w, err)
		return
	}
	jsonOK(w, view)
}

func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	if err := s.reg.DeleteTunnel(r.Context(), r.PathValue("id")); err != nil {
		writeTunnelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func jsonCreated(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(data)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   http.StatusText(code),
		"message": msg,
		"code":    code,
	})
}
