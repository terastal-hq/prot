package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Authenticator provides token-based authentication and loopback verification.
type Authenticator struct {
	mu         sync.RWMutex
	token      string
	tokenPath  string
	allowEmpty bool
}

// Config configures security rules.
type Config struct {
	TokenPath  string // Custom path for token file, e.g. ~/.prot/auth.token
	AllowEmpty bool   // For development/testing only
}

// NewAuthenticator creates or loads an authentication token.
func NewAuthenticator(cfg Config) (*Authenticator, error) {
	auth := &Authenticator{
		tokenPath:  cfg.TokenPath,
		allowEmpty: cfg.AllowEmpty,
	}

	if cfg.TokenPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("getting user home dir: %w", err)
		}
		auth.tokenPath = filepath.Join(home, ".prot", "auth.token")
	}

	if err := auth.ensureToken(); err != nil {
		if cfg.AllowEmpty {
			return auth, nil
		}
		return nil, err
	}

	return auth, nil
}

// Token returns the current auth token.
func (a *Authenticator) Token() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.token
}

func (a *Authenticator) ensureToken() error {
	dir := filepath.Dir(a.tokenPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating auth directory: %w", err)
	}

	if data, err := os.ReadFile(a.tokenPath); err == nil {
		token := strings.TrimSpace(string(data))
		if len(token) >= 32 {
			a.token = token
			return nil
		}
	}

	// Generate new 32-byte (256-bit) cryptographically secure random token
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generating secure token: %w", err)
	}
	token := hex.EncodeToString(b)

	// Write with strictly 0600 permissions
	if err := os.WriteFile(a.tokenPath, []byte(token), 0600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}

	a.token = token
	return nil
}

// VerifyToken verifies a candidate token using constant-time comparison.
func (a *Authenticator) VerifyToken(candidate string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.token == "" && a.allowEmpty {
		return true
	}

	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(a.token), []byte(candidate)) == 1
}

// Middleware creates an HTTP middleware that enforces:
// 1. Loopback origin only (prevent LAN/remote access to management API)
// 2. DNS rebinding protection (Host header must be localhost or 127.0.0.1 or [::1])
// 3. Bearer token authentication
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Loopback check
		remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			remoteHost = r.RemoteAddr
		}
		ip := net.ParseIP(remoteHost)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, `{"error":"forbidden","message":"Management endpoints are loopback only"}`, http.StatusForbidden)
			return
		}

		// 2. DNS Rebinding protection:
		// Reject any Host header that is not localhost or loopback IP
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.TrimSpace(host))
		if !isAllowedManagementHost(host) {
			http.Error(w, `{"error":"forbidden","message":"DNS rebinding attempt blocked"}`, http.StatusForbidden)
			return
		}

		// Allow health endpoint without token if from loopback
		if r.URL.Path == "/v1/health" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		// 3. Token authentication
		token := extractToken(r)
		if !a.VerifyToken(token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="prot"`)
			http.Error(w, `{"error":"unauthorized","message":"Invalid or missing management token"}`, http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isAllowedManagementHost(host string) bool {
	isLocalhost := host == "localhost"
	isIPv4Loopback := host == "127.0.0.1"
	isIPv6Loopback := host == "::1" || host == "[::1]"
	return isLocalhost || isIPv4Loopback || isIPv6Loopback
}

func extractToken(r *http.Request) string {
	// Check Authorization header
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// Check X-Prot-Key header
	if key := r.Header.Get("X-Prot-Key"); key != "" {
		return key
	}

	// For WebSockets / EventSource where custom headers cannot be set in browser:
	if r.Header.Get("Upgrade") == "websocket" || strings.HasPrefix(r.Header.Get("Accept"), "text/event-stream") {
		if token := r.URL.Query().Get("token"); token != "" {
			return token
		}
	}

	return ""
}

// GenerateNewToken generates a fresh token and saves it.
func (a *Authenticator) GenerateNewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("entropy error")
	}
	token := hex.EncodeToString(b)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.tokenPath != "" {
		if err := os.WriteFile(a.tokenPath, []byte(token), 0600); err != nil {
			return "", err
		}
	}
	a.token = token
	return token, nil
}
