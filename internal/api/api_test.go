package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	var res map[string]any
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

func TestAPIUnknownServiceControlReturns404(t *testing.T) {
	srv, _, cleanup := setupTestServer(t)
	defer cleanup()

	for _, action := range []string{"start", "stop", "restart"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/services/ghost/"+action, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s of an unknown service: status %d, want 404 (%s)", action, rec.Code, rec.Body.String())
		}
	}
}

func TestAPIFollowLogsSendsHistoryOnceThenLiveLines(t *testing.T) {
	srv, reg, cleanup := setupTestServer(t)
	defer cleanup()
	defer reg.StopAll(2 * time.Second)

	ctx := context.Background()
	svc := storage.Service{ID: "svc-follow", Name: "Follow", Command: "echo first; sleep 0.3; echo second; sleep 30"}
	if _, err := reg.RegisterService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.StartService(ctx, "svc-follow"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if logs, _ := reg.GetLogs(ctx, "svc-follow", 0); strings.Contains(fmt.Sprint(logs), "first") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL+"/v1/services/svc-follow/logs?follow=true", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var seen []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			seen = append(seen, line)
		}
		if strings.Contains(line, `"second"`) {
			break
		}
	}
	all := strings.Join(seen, "\n")
	if strings.Count(all, `"first"`) != 1 || strings.Count(all, `"second"`) != 1 {
		t.Errorf("each line must appear exactly once:\n%s", all)
	}
}

func TestAPIFollowLogsUnknownServiceIs404(t *testing.T) {
	srv, _, cleanup := setupTestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/services/ghost/logs?follow=true", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
