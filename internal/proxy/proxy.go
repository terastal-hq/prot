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
)

// Route defines an upstream target for a given alias/hostname.
type Route struct {
	Alias       string    `json:"alias"`
	Host        string    `json:"host"`         // e.g. "frontend.localhost"
	TargetURL   string    `json:"target_url"`   // e.g. "http://127.0.0.1:3000"
	LANExposed  bool      `json:"lan_exposed"`  // Whether exposed beyond loopback
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	parsedURL   *url.URL
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

	now := time.Now().UTC()
	existing, ok := r.routes[alias]
	if ok {
		existing.TargetURL = targetURL
		existing.parsedURL = u
		existing.LANExposed = lanExposed
		existing.UpdatedAt = now
		return nil
	}

	route := &Route{
		Alias:      alias,
		Host:       fmt.Sprintf("%s.localhost", alias),
		TargetURL:  targetURL,
		LANExposed: lanExposed,
		CreatedAt:  now,
		UpdatedAt:  now,
		parsedURL:  u,
	}
	r.routes[alias] = route
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
	// Return shallow copy
	copy := *rt
	return &copy, true
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
	ListenAddr   string        // e.g. "127.0.0.1:8080" or ":8080"
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

	// ReverseProxy director and error handling
	rp := &httputil.ReverseProxy{
		Director: s.director,
		// FlushInterval = -1 enables immediate streaming / SSE / HMR
		FlushInterval: -1,
		ErrorHandler:  s.errorHandler,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	s.proxy = rp
	s.httpServer = &http.Server{
		Addr:    opts.ListenAddr,
		Handler: s,
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

// ServeHTTP handles requests, routes them to upstreams or renders diagnostics.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Management isolation check:
	// Do not allow the dev proxy to reach daemon management endpoints
	if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/mcp" {
		msg := `{"error": "forbidden", "message": "Management endpoints are isolated and not accessible via reverse proxy"}`
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	route, ok := s.router.Match(r.Host)
	if !ok {
		// Friendly prot route not found page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>prot — Route Not Found</title><style>
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  background: #0d1117;
  color: #c9d1d9;
  padding: 40px;
}
h1 { color: #58a6ff; }
.card {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 24px;
  max-width: 600px;
  margin-top: 20px;
}
code {
  background: #21262d;
  padding: 2px 6px;
  border-radius: 4px;
  font-family: monospace;
  color: #f0883e;
}
</style></head>
<body>
<h1>prot &mdash; Route Not Found</h1>
<div class="card">
  <p>No upstream service is configured for host: <code>%s</code></p>
  <p>To register an alias with prot, run:</p>
  <pre><code>prot alias &lt;service&gt; &lt;alias&gt;</code></pre>
</div>
</body></html>`, r.Host)
		return
	}

	// LAN exposure policy enforcement
	if !route.LANExposed && !isLoopback(r.RemoteAddr) {
		msg := `{"error": "forbidden", "message": "Service is private and not exposed to the local network"}`
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	// Pass to reverse proxy
	s.proxy.ServeHTTP(w, r)
}

func (s *Server) director(req *http.Request) {
	route, ok := s.router.Match(req.Host)
	if !ok {
		return
	}

	target := route.parsedURL
	targetQuery := target.RawQuery

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)

	if targetQuery == "" || req.URL.RawQuery == "" {
		req.URL.RawQuery = targetQuery + req.URL.RawQuery
	} else {
		req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
	}

	// Preserve incoming X-Forwarded headers while adding standard proxy headers
	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		prior := req.Header.Get("X-Forwarded-For")
		if prior != "" {
			req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			req.Header.Set("X-Forwarded-For", clientIP)
		}
	}

	if req.Header.Get("X-Forwarded-Proto") == "" {
		if req.TLS != nil {
			req.Header.Set("X-Forwarded-Proto", "https")
		} else {
			req.Header.Set("X-Forwarded-Proto", "http")
		}
	}

	if req.Header.Get("X-Forwarded-Host") == "" {
		req.Header.Set("X-Forwarded-Host", req.Host)
	}

	// Set upstream Host header
	req.Host = target.Host
}

func (s *Server) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	route, _ := s.router.Match(r.Host)
	targetDesc := "upstream"
	if route != nil {
		targetDesc = route.TargetURL
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>prot — 502 Bad Gateway</title><style>
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  background: #0d1117;
  color: #c9d1d9;
  padding: 40px;
}
h1 { color: #f85149; }
.card {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 24px;
  max-width: 600px;
  margin-top: 20px;
}
code {
  background: #21262d;
  padding: 2px 6px;
  border-radius: 4px;
  font-family: monospace;
  color: #79c0ff;
}
</style></head>
<body>
<h1>prot &mdash; 502 Bad Gateway</h1>
<div class="card">
  <p>The upstream server at <code>%s</code> is not responding.</p>
  <p>Error: <code>%v</code></p>
  <p>Check if your development service is running or inspect logs:</p>
  <pre><code>prot logs &lt;service&gt;</code></pre>
</div>
</body></html>`, targetDesc, err)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
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
