package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
)

// Route defines an upstream target for a given alias/hostname.
type Route struct {
	Alias      string    `json:"alias"`
	Host       string    `json:"host"`        // e.g. "frontend.localhost"
	TargetURL  string    `json:"target_url"`  // e.g. "http://127.0.0.1:3000"
	LANExposed bool      `json:"lan_exposed"` // Whether exposed beyond loopback
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	parsedURL  *url.URL
}

// Router provides thread-safe dynamic route resolution.
type Router struct {
	mu     sync.RWMutex
	routes map[string]*Route // key: normalized alias
}

// NewRouter creates an empty dynamic router.
func NewRouter() *Router {
	return &Router{
		routes: make(map[string]*Route),
	}
}

// SetRoute registers or updates an upstream route for an alias.
func (r *Router) SetRoute(alias, targetURL string, lanExposed bool) error {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return fmt.Errorf("alias cannot be empty")
	}

	u, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("target URL scheme must be http or https, got %q", u.Scheme)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Routes are immutable once published: request handlers hold the *Route that Match returned while SetRoute
	// may run concurrently, so an update swaps in a new value instead of editing the shared one.
	now := time.Now().UTC()
	createdAt := now
	if existing, ok := r.routes[alias]; ok {
		createdAt = existing.CreatedAt
	}
	r.routes[alias] = &Route{
		Alias:      alias,
		Host:       alias + ".localhost",
		TargetURL:  targetURL,
		LANExposed: lanExposed,
		CreatedAt:  createdAt,
		UpdatedAt:  now,
		parsedURL:  u,
	}
	return nil
}

// RemoveRoute unregisters an alias.
func (r *Router) RemoveRoute(alias string) {
	alias = strings.ToLower(strings.TrimSpace(alias))
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, alias)
}

// GetRoute returns the route for an alias if registered.
func (r *Router) GetRoute(alias string) (*Route, bool) {
	alias = strings.ToLower(strings.TrimSpace(alias))
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.routes[alias]
	if !ok {
		return nil, false
	}
	routeCopy := *rt
	return &routeCopy, true
}

// ListRoutes returns all current routes.
func (r *Router) ListRoutes() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Route, 0, len(r.routes))
	for _, rt := range r.routes {
		list = append(list, *rt)
	}
	return list
}

// Match resolves a Host header to a Route.
// Supports "alias.localhost", "alias.localhost:8080", or direct alias headers.
func (r *Router) Match(hostHeader string) (*Route, bool) {
	// Strip port if present
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Check if ending with .localhost
	if strings.HasSuffix(host, ".localhost") {
		subdomain := strings.TrimSuffix(host, ".localhost")
		if rt, ok := r.routes[subdomain]; ok {
			return rt, true
		}
	}

	// Check direct alias match
	if rt, ok := r.routes[host]; ok {
		return rt, true
	}

	return nil, false
}

// Server is the prot local reverse proxy.
type Server struct {
	router     *Router
	proxy      *httputil.ReverseProxy
	httpServer *http.Server
	listenAddr string
	listener   net.Listener
}

// ServerOptions configures the proxy server.
type ServerOptions struct {
	ListenAddr string // e.g. "127.0.0.1:8080" or ":8080"
	// ReadTimeout and WriteTimeout default to none: a write timeout would cut off SSE streams and WebSockets.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// NewServer constructs the reverse proxy server.
func NewServer(router *Router, opts ServerOptions) *Server {
	if opts.ListenAddr == "" {
		opts.ListenAddr = "127.0.0.1:8080"
	}

	s := &Server{
		router:     router,
		listenAddr: opts.ListenAddr,
	}

	rp := &httputil.ReverseProxy{
		Rewrite: s.rewrite,
		// FlushInterval = -1 enables immediate streaming / SSE / HMR
		FlushInterval: -1,
		ErrorHandler:  s.errorHandler,
		// Upstreams are always loopback ports, so no HTTP(S)_PROXY handling.
		Transport: &http.Transport{
			// Upstreams are registered as 127.0.0.1:port, but a dev server bound to "localhost" may only be
			// listening on ::1, so the dial falls back to it.
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return netutil.DialLoopback(ctx, &net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}, network, addr)
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	s.proxy = rp
	s.httpServer = &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           s,
		ReadHeaderTimeout: 15 * time.Second, // slow-header clients must not hold connections forever
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
	}

	return s
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type routeContextKey struct{}

// ServeHTTP resolves the route once, enforces the LAN policy and hands the request to the reverse proxy. Every
// path belongs to the upstream app: the management API lives on its own authenticated listener, never here.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := s.router.Match(r.Host)
	if !ok {
		renderPage(w, http.StatusNotFound, notFoundPage, pageData{Host: r.Host})
		return
	}

	// LAN exposure policy enforcement
	if !route.LANExposed && !isLoopback(r.RemoteAddr) {
		msg := `{"error": "forbidden", "message": "Service is private and not exposed to the local network"}`
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	ctx := context.WithValue(r.Context(), routeContextKey{}, route)
	s.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// rewrite points the outbound request at the matched route's upstream.
func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	route, ok := pr.In.Context().Value(routeContextKey{}).(*Route)
	if !ok {
		return // unreachable: ServeHTTP always attaches the route
	}

	pr.SetURL(route.parsedURL) // joins paths and queries and sets the upstream Host

	// Keep the client chain (X-Forwarded-For) a trusted upstream proxy sent us and append this hop.
	if prior := pr.In.Header.Values("X-Forwarded-For"); len(prior) > 0 {
		pr.Out.Header["X-Forwarded-For"] = prior
	}
	pr.SetXForwarded()

	// A proxy in front of prot (for example a tunnel) knows the real scheme and host; trust it.
	for _, name := range []string{"X-Forwarded-Proto", "X-Forwarded-Host"} {
		if v := pr.In.Header.Get(name); v != "" {
			pr.Out.Header.Set(name, v)
		}
	}
}

func (s *Server) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	target := "upstream"
	if route, ok := r.Context().Value(routeContextKey{}).(*Route); ok {
		target = route.TargetURL
	}
	renderPage(w, http.StatusBadGateway, badGatewayPage, pageData{Target: target, Error: err.Error()})
}

// Start begins listening.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("proxy failed to listen on %q: %w", s.listenAddr, err)
	}
	s.listener = ln
	s.listenAddr = ln.Addr().String()

	go func() {
		_ = s.httpServer.Serve(ln)
	}()
	return nil
}

// Addr returns the listening network address.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.listenAddr
}

// Close gracefully stops the proxy server.
func (s *Server) Close(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
