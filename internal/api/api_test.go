package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
)

func setupTestServer(t *testing.T) (*Server, *registry.Registry, func()) {
	tempDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tempDir, "prot.db"))
	if err != nil {
		t.Fatalf("storage.Open failed: %v", err)
	}

	router := proxy.NewRouter()
	reg := registry.New(registry.Config{
		DB:          db,
		Router:      router,
		GatewayHost: "8080",
	})

	srv := NewServer(Config{
		Registry:    reg,
		Router:      router,
		Version:     "0.1.0-test",
		GatewayAddr: "127.0.0.1:8080",
	})

	cleanup := func() {
		_ = db.Close()
	}

	return srv, reg, cleanup
}

func TestAPIHealth(t *testing.T) {
	srv, _, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res map[string]interface{}
	_ = json.NewDecoder(rec.Body).Decode(&res)
	if res["status"] != "ok" || res["version"] != "0.1.0-test" {
		t.Errorf("unexpected health body: %+v", res)
	}
}

func TestAPIServicesCRUD(t *testing.T) {
	srv, _, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Create Service
	svcJSON := []byte(`{
		"id": "svc-test",
		"name": "Test Web",
		"alias": "web",
		"command": "sh -c 'sleep 10'",
		"port": 3000
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/services", bytes.NewReader(svcJSON))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Get Service
	req = httptest.NewRequest(http.MethodGet, "/v1/services/svc-test", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	// 3. List Services
	req = httptest.NewRequest(http.MethodGet, "/v1/services", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	// 4. Start Service
	req = httptest.NewRequest(http.MethodPost, "/v1/services/svc-test/start", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for start, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Stop Service
	req = httptest.NewRequest(http.MethodPost, "/v1/services/svc-test/stop", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for stop, got %d: %s", rec.Code, rec.Body.String())
	}

	// 6. Delete Service
	req = httptest.NewRequest(http.MethodDelete, "/v1/services/svc-test", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 NoContent, got %d", rec.Code)
	}
}

func TestAPIRoutes(t *testing.T) {
	srv, _, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Create Route
	payload := []byte(`{"alias": "api", "target_url": "http://127.0.0.1:4000", "lan_exposed": false}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/routes", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", rec.Code)
	}

	// 2. List Routes
	req = httptest.NewRequest(http.MethodGet, "/v1/routes", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	// 3. Delete Route
	req = httptest.NewRequest(http.MethodDelete, "/v1/routes/api", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 NoContent, got %d", rec.Code)
	}
}
