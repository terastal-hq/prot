package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// tunnelServer is an API server whose tunnel manager runs a fake ssh that just stays alive.
func tunnelServer(t *testing.T) *Server {
	t.Helper()
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := proxy.NewRouter()
	reg := registry.New(registry.Config{
		DB: db, Router: router, Tunnels: tunnels.NewManagerWithSSH(ssh), GatewayHost: "8080",
	})
	t.Cleanup(reg.StopAllTunnels)
	return NewServer(Config{Registry: reg, Router: router, Version: "test"})
}

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %q", rec.Body.String())
	}
	return out
}

const newTunnel = `{"alias":"db","ssh_host":"bastion.example","remote_port":5432,"local_port":39555}`

func TestAPITunnelLifecycle(t *testing.T) {
	srv := tunnelServer(t)

	rec := do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode(t, rec)
	id := created["config"].(map[string]any)["id"].(string)
	if created["canonical_url"] != "http://db.localhost:8080" || !strings.HasPrefix(id, "tun_") {
		t.Errorf("unexpected create response: %v", created)
	}

	list := decode(t, do(t, srv, http.MethodGet, "/v1/tunnels", ""))
	if list["count"] != float64(1) {
		t.Errorf("list: %v", list)
	}

	// start and stop work by alias as well as by id
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels/db/start", ""); rec.Code != http.StatusOK {
		t.Fatalf("start by alias: %d %s", rec.Code, rec.Body)
	}
	stopped := do(t, srv, http.MethodPost, "/v1/tunnels/"+id+"/stop", "")
	if stopped.Code != http.StatusOK || decode(t, stopped)["state"] != "disconnected" {
		t.Errorf("stop by id: %d %s", stopped.Code, stopped.Body)
	}

	if rec := do(t, srv, http.MethodDelete, "/v1/tunnels/db", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if list := decode(t, do(t, srv, http.MethodGet, "/v1/tunnels", "")); list["count"] != float64(0) {
		t.Errorf("tunnel still listed after delete: %v", list)
	}
}

func TestAPITunnelErrorStatuses(t *testing.T) {
	srv := tunnelServer(t)
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	const post = http.MethodPost
	type tc struct {
		name, method, path, body string
		want                     int
	}
	cases := []tc{
		{"duplicate alias", post, "/v1/tunnels", `{"alias":"db","ssh_host":"h","remote_port":1}`, http.StatusConflict},
		{"invalid alias", post, "/v1/tunnels", `{"alias":"No Good!","ssh_host":"h","remote_port":1}`, http.StatusBadRequest},
		{"missing host", post, "/v1/tunnels", `{"alias":"x","remote_port":1}`, http.StatusBadRequest},
		{"option-like host", post, "/v1/tunnels", `{"alias":"x","ssh_host":"-oEvil","remote_port":1}`, http.StatusBadRequest},
		{"bad json", post, "/v1/tunnels", `{nope`, http.StatusBadRequest},
		{"unknown start", post, "/v1/tunnels/ghost/start", "", http.StatusNotFound},
		{"unknown stop", post, "/v1/tunnels/ghost/stop", "", http.StatusNotFound},
		{"unknown delete", http.MethodDelete, "/v1/tunnels/ghost", "", http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := do(t, srv, c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}
}

func TestAPITunnelsWithoutSSHAreNotImplemented(t *testing.T) {
	srv, _, cleanup := setupTestServer(t) // no tunnel manager
	defer cleanup()

	rec := do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "no ssh binary") {
		t.Errorf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodGet, "/v1/tunnels", ""); rec.Code != http.StatusOK ||
		decode(t, rec)["count"] != float64(0) {
		t.Errorf("listing must still work and be empty: %d %s", rec.Code, rec.Body)
	}
}
