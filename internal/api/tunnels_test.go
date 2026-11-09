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
	"github.com/terastal-hq/prot/internal/secrets"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// tunnelServer is an API server whose tunnel manager runs a fake ssh that just stays alive.
func tunnelServer(t *testing.T) *Server {
	t.Helper()
	return tunnelServerWithSecrets(t, nil)
}

func tunnelServerWithSecrets(t *testing.T, store secrets.Store) *Server {
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
		DB: db, Router: router, Tunnels: tunnels.NewManagerWithSSH(ssh), Secrets: store, GatewayHost: "8080",
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

const withPassword = `{"alias":"db","ssh_host":"bastion.example","remote_port":5432,"local_port":39556,` +
	`"password":"hunter2-do-not-leak"}`

// The password is write-only: stored in the keyring, flagged in responses, never echoed.
func TestAPITunnelPasswordIsWriteOnly(t *testing.T) {
	store := secrets.NewMemory()
	srv := tunnelServerWithSecrets(t, store)

	created := do(t, srv, http.MethodPost, "/v1/tunnels", withPassword)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	body := decode(t, created)
	if body["config"].(map[string]any)["has_password"] != true {
		t.Errorf("has_password should be true: %v", body)
	}
	if store.Len() != 1 {
		t.Errorf("keyring holds %d secrets, want 1", store.Len())
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"create": created,
		"list":   do(t, srv, http.MethodGet, "/v1/tunnels", ""),
	} {
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), `"password"`) {
			t.Errorf("%s response exposes the password: %s", name, rec.Body)
		}
	}
}

func TestAPISetAndClearTunnelPassword(t *testing.T) {
	store := secrets.NewMemory()
	srv := tunnelServerWithSecrets(t, store)
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	rec := do(t, srv, http.MethodPut, "/v1/tunnels/db/password", `{"password":"s3cret"}`)
	if rec.Code != http.StatusOK || decode(t, rec)["config"].(map[string]any)["has_password"] != true {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Error("set response exposes the password")
	}
	if store.Len() != 1 {
		t.Errorf("keyring holds %d secrets after set", store.Len())
	}

	rec = do(t, srv, http.MethodDelete, "/v1/tunnels/db/password", "")
	if rec.Code != http.StatusOK || decode(t, rec)["config"].(map[string]any)["has_password"] != false {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if store.Len() != 0 {
		t.Errorf("keyring still holds %d secrets after clear", store.Len())
	}
}

func TestAPITunnelPasswordErrorStatuses(t *testing.T) {
	srv := tunnelServerWithSecrets(t, secrets.NewMemory())
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"empty password", http.MethodPut, "/v1/tunnels/db/password", `{"password":""}`, http.StatusBadRequest},
		{"line break", http.MethodPut, "/v1/tunnels/db/password", `{"password":"a\nb"}`, http.StatusBadRequest},
		{"bad json", http.MethodPut, "/v1/tunnels/db/password", `{nope`, http.StatusBadRequest},
		{"unknown set", http.MethodPut, "/v1/tunnels/ghost/password", `{"password":"x"}`, http.StatusNotFound},
		{"unknown clear", http.MethodDelete, "/v1/tunnels/ghost/password", "", http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := do(t, srv, c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}
}

// No keyring means no passwords: say so and save nothing, rather than silently dropping the password.
func TestAPIPasswordWithoutKeyringIsNotImplemented(t *testing.T) {
	for name, store := range map[string]secrets.Store{"no store": nil, "broken keyring": brokenStore()} {
		srv := tunnelServerWithSecrets(t, store)
		rec := do(t, srv, http.MethodPost, "/v1/tunnels", withPassword)
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "keyring") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if list := decode(t, do(t, srv, http.MethodGet, "/v1/tunnels", "")); list["count"] != float64(0) {
			t.Errorf("%s: a tunnel was saved although its password could not be: %v", name, list)
		}
	}
}

func brokenStore() secrets.Store {
	m := secrets.NewMemory()
	m.Err = secrets.ErrUnavailable
	return m
}

func TestAPIEditTunnel(t *testing.T) {
	srv := tunnelServer(t)
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	const other = `{"alias":"other","ssh_host":"h","remote_port":1}`
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels", other); rec.Code != http.StatusCreated {
		t.Fatalf("create other: %d %s", rec.Code, rec.Body)
	}

	// Only the fields in the body change, and the tunnel is found by alias.
	rec := do(t, srv, http.MethodPatch, "/v1/tunnels/db", `{"alias":"warehouse","remote_port":6000,"ssh_user":"deploy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	got := decode(t, rec)
	cfg := got["config"].(map[string]any)
	if cfg["alias"] != "warehouse" || cfg["remote_port"] != float64(6000) || cfg["ssh_user"] != "deploy" ||
		cfg["ssh_host"] != "bastion.example" || cfg["local_port"] != float64(39555) {
		t.Errorf("config after patch: %v", cfg)
	}
	if got["canonical_url"] != "http://warehouse.localhost:8080" {
		t.Errorf("canonical_url = %v", got["canonical_url"])
	}

	// The old alias is gone, the new one works.
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels/db/start", ""); rec.Code != http.StatusNotFound {
		t.Errorf("old alias: %d, want 404", rec.Code)
	}
	if rec := do(t, srv, http.MethodPost, "/v1/tunnels/warehouse/stop", ""); rec.Code != http.StatusOK {
		t.Errorf("new alias: %d %s", rec.Code, rec.Body)
	}
}

func TestAPIEditTunnelErrorStatuses(t *testing.T) {
	srv := tunnelServer(t)
	do(t, srv, http.MethodPost, "/v1/tunnels", newTunnel)
	do(t, srv, http.MethodPost, "/v1/tunnels", `{"alias":"other","ssh_host":"h","remote_port":1}`)

	cases := []struct {
		name, path, body string
		want             int
	}{
		{"unknown tunnel", "/v1/tunnels/nope", `{}`, http.StatusNotFound},
		{"alias taken", "/v1/tunnels/db", `{"alias":"other"}`, http.StatusConflict},
		{"invalid alias", "/v1/tunnels/db", `{"alias":"Not Valid!"}`, http.StatusBadRequest},
		{"option-like host", "/v1/tunnels/db", `{"ssh_host":"-oProxyCommand=x"}`, http.StatusBadRequest},
		{"no remote port", "/v1/tunnels/db", `{"remote_port":0}`, http.StatusBadRequest},
		{"not json", "/v1/tunnels/db", `nope`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := do(t, srv, http.MethodPatch, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}

	// None of the refused edits changed anything.
	list := decode(t, do(t, srv, http.MethodGet, "/v1/tunnels", ""))
	for _, item := range list["tunnels"].([]any) {
		cfg := item.(map[string]any)["config"].(map[string]any)
		if cfg["alias"] == "db" && (cfg["ssh_host"] != "bastion.example" || cfg["remote_port"] != float64(5432)) {
			t.Errorf("a refused edit changed the tunnel: %v", cfg)
		}
	}
}
