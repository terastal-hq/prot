package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDaemonLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	cfg := Config{
		DataDir:     tempDir,
		GatewayAddr: "127.0.0.1:0", // dynamic free port
		AdminAddr:   "127.0.0.1:0", // dynamic free port
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New daemon failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := d.Start(ctx); err != nil {
		t.Fatalf("Daemon Start failed: %v", err)
	}

	// 1. Verify state file written
	stateFile := filepath.Join(tempDir, "daemon.json")
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("expected daemon.json to exist: %v", err)
	}

	// 2. Query /v1/health
	healthURL := fmt.Sprintf("http://%s/v1/health", d.AdminAddr())
	resp, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("GET /v1/health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	var healthRes map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&healthRes); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if healthRes["status"] != "ok" {
		t.Errorf("expected status ok, got %v", healthRes["status"])
	}

	// 3. Register route via Admin API
	// Spin up a dummy upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from upstream!"))
	}))
	defer upstream.Close()

	routePayload := []byte(fmt.Sprintf(`{"alias":"mytest","target_url":"%s","lan_exposed":false}`, upstream.URL))
	routesURL := fmt.Sprintf("http://%s/v1/routes", d.AdminAddr())
	req, _ := http.NewRequest(http.MethodPost, routesURL, bytes.NewReader(routePayload))
	req.Header.Set("Authorization", "Bearer "+d.Token())
	req.Header.Set("Content-Type", "application/json")

	respRoutes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/routes failed: %v", err)
	}
	respRoutes.Body.Close()
	if respRoutes.StatusCode != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d", respRoutes.StatusCode)
	}

	// 4. Access via proxy using alias.localhost
	proxyReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/", d.ProxyAddr()), nil)
	proxyReq.Host = "mytest.localhost"
	proxyResp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		t.Fatalf("proxy req failed: %v", err)
	}
	defer proxyResp.Body.Close()

	body, _ := io.ReadAll(proxyResp.Body)
	if string(body) != "hello from upstream!" {
		t.Errorf("expected 'hello from upstream!', got %q", string(body))
	}

	// 5. Graceful Stop
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	if err := d.Stop(stopCtx); err != nil {
		t.Fatalf("Daemon Stop failed: %v", err)
	}

	// Verify state file removed
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Errorf("expected state file to be removed after Stop")
	}
}
