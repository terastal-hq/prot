package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/storage"
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

	var healthRes map[string]any
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

func TestPortOf(t *testing.T) {
	cases := map[string]struct {
		port uint32
		ok   bool
	}{
		"127.0.0.1:8080": {8080, true},
		"[::1]:4400":     {4400, true},
		"127.0.0.1":      {0, false},
		"host:notaport":  {0, false},
		"host:99999":     {0, false},
	}
	for addr, want := range cases {
		if port, ok := portOf(addr); port != want.port || ok != want.ok {
			t.Errorf("portOf(%q) = %d, %v; want %d, %v", addr, port, ok, want.port, want.ok)
		}
	}
}

func TestAutostartFailureIsLogged(t *testing.T) {
	d, err := New(Config{DataDir: t.TempDir(), GatewayAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = d.Stop(context.Background()) }()

	broken := storage.Service{
		ID: "svc-broken", Name: "Broken App", Command: "sleep 1", Cwd: "/nonexistent/prot-test-dir",
		AutoStart: true, RestartPolicy: storage.RestartNever,
	}
	healthy := storage.Service{ID: "svc-ok", Name: "Healthy App", Command: "sleep 30", AutoStart: true}
	for _, svc := range []storage.Service{broken, healthy} {
		if err := d.db.SaveService(context.Background(), svc); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	if err := d.recoverState(context.Background()); err != nil {
		t.Fatalf("recoverState: %v", err)
	}
	if !strings.Contains(logged.String(), `autostart of "Broken App" failed`) {
		t.Errorf("autostart failure was not logged: %q", logged.String())
	}
	if st, ok := d.reg.Supervisor().GetState("svc-ok"); !ok || st.PID == 0 {
		t.Errorf("one failing service must not prevent the others from starting: %+v", st)
	}
}
