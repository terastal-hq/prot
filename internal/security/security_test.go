package security

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthenticatorTokenLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	tokenPath := filepath.Join(tempDir, "auth.token")

	auth, err := NewAuthenticator(Config{TokenPath: tokenPath})
	if err != nil {
		t.Fatalf("NewAuthenticator failed: %v", err)
	}

	tok1 := auth.Token()
	if len(tok1) != 64 { // 32 bytes in hex = 64 chars
		t.Errorf("expected 64 char hex token, got %d", len(tok1))
	}

	// Verify file permissions
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 file permissions, got %o", info.Mode().Perm())
	}

	// Verify existing token loaded on second initialization
	auth2, err := NewAuthenticator(Config{TokenPath: tokenPath})
	if err != nil {
		t.Fatalf("second init failed: %v", err)
	}
	if auth2.Token() != tok1 {
		t.Errorf("expected identical token on reload, got %s vs %s", auth2.Token(), tok1)
	}

	// Verify verification function
	if !auth.VerifyToken(tok1) {
		t.Errorf("expected VerifyToken to return true for valid token")
	}
	if auth.VerifyToken("invalid_token") {
		t.Errorf("expected VerifyToken to return false for invalid token")
	}
}

func TestSecurityMiddleware(t *testing.T) {
	tempDir := t.TempDir()
	tokenPath := filepath.Join(tempDir, "auth.token")

	auth, _ := NewAuthenticator(Config{TokenPath: tokenPath})
	validToken := auth.Token()

	handlerCalled := false
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	secured := auth.Middleware(dummyHandler)

	// 1. Non-loopback IP blocked
	req := httptest.NewRequest(http.MethodGet, "http://localhost/v1/services", nil)
	req.RemoteAddr = "192.168.1.50:54321"
	rec := httptest.NewRecorder()
	handlerCalled = false
	secured.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || handlerCalled {
		t.Errorf("expected 403 Forbidden for non-loopback IP, got %d", rec.Code)
	}

	// 2. DNS Rebinding attempt blocked
	req = httptest.NewRequest(http.MethodGet, "http://evil.com/v1/services", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "evil.com"
	rec = httptest.NewRecorder()
	handlerCalled = false
	secured.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || handlerCalled {
		t.Errorf("expected 403 Forbidden for DNS rebinding host, got %d", rec.Code)
	}

	// 3. Loopback + valid host but missing token -> 401 Unauthorized
	req = httptest.NewRequest(http.MethodGet, "http://localhost/v1/services", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec = httptest.NewRecorder()
	handlerCalled = false
	secured.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || handlerCalled {
		t.Errorf("expected 401 Unauthorized for missing token, got %d", rec.Code)
	}

	// 4. Loopback + valid host + Bearer token -> 200 OK
	req = httptest.NewRequest(http.MethodGet, "http://localhost/v1/services", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer "+validToken)
	rec = httptest.NewRecorder()
	handlerCalled = false
	secured.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !handlerCalled {
		t.Errorf("expected 200 OK for valid bearer token, got %d", rec.Code)
	}

	// 5. /v1/health bypassed for loopback
	req = httptest.NewRequest(http.MethodGet, "http://localhost/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec = httptest.NewRecorder()
	handlerCalled = false
	secured.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !handlerCalled {
		t.Errorf("expected 200 OK for /v1/health, got %d", rec.Code)
	}
}

// If the token cannot be created the authenticator must refuse to exist, and an empty token never authenticates.
func TestAuthenticatorFailsClosed(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The token directory would have to be created underneath a regular file.
	if _, err := NewAuthenticator(Config{TokenPath: filepath.Join(blocker, "auth.token")}); err == nil {
		t.Error("expected an error when the token cannot be created")
	}

	auth, err := NewAuthenticator(Config{TokenPath: filepath.Join(t.TempDir(), "auth.token")})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", "   ", "wrong"} {
		if auth.VerifyToken(candidate) {
			t.Errorf("VerifyToken(%q) must be false", candidate)
		}
	}
	if !auth.VerifyToken(auth.Token()) {
		t.Error("the real token must verify")
	}
}
