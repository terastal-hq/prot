package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *daemonClient {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &daemonClient{adminAddr: strings.TrimPrefix(ts.URL, "http://"), token: "tok"}
}

func TestServicePathEscapesIdentifiers(t *testing.T) {
	cases := []struct {
		id     string
		suffix []string
		want   string
	}{
		{"web", nil, "/v1/services/web"},
		{"web", []string{"start"}, "/v1/services/web/start"},
		{"a/b", []string{"stop"}, "/v1/services/a%2Fb/stop"},
		{"x?y=1", []string{"logs"}, "/v1/services/x%3Fy=1/logs"},
		{"../routes", nil, "/v1/services/..%2Froutes"},
	}
	for _, tc := range cases {
		if got := servicePath(tc.id, tc.suffix...); got != tc.want {
			t.Errorf("servicePath(%q, %v) = %q, want %q", tc.id, tc.suffix, got, tc.want)
		}
	}
}

func TestCallDecodesSuccessAndSendsToken(t *testing.T) {
	var auth, contentType, method string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth, contentType, method = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"echo": body["name"]})
	})

	var out struct {
		Echo string `json:"echo"`
	}
	body := map[string]string{"name": "web"}
	if err := client.call(http.MethodPost, "/v1/services", body, &out, http.StatusCreated); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Echo != "web" || auth != "Bearer tok" || contentType != "application/json" || method != http.MethodPost {
		t.Errorf("got echo=%q auth=%q ct=%q method=%q", out.Echo, auth, contentType, method)
	}
}

func TestCallTurnsDaemonErrorsIntoReadableMessages(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"error":"Not Found","message":"service not found"}`))
	})

	err := client.call(http.MethodPost, "/v1/services/x/stop", nil, nil, http.StatusOK)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Message != "service not found" {
		t.Fatalf("got %#v, want a 404 apiError carrying the daemon's message", err)
	}
	if strings.Contains(err.Error(), "{") {
		t.Errorf("error should not leak raw JSON: %q", err)
	}
}

func TestCallFallsBackToBodyForNonJSONErrors(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "plain failure", http.StatusBadGateway)
	})
	err := client.call(http.MethodGet, "/v1/x", nil, nil, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "plain failure") {
		t.Errorf("got %v", err)
	}
}

func TestCallReportsUnreachableDaemon(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(ts.URL, "http://")
	ts.Close()

	client := &daemonClient{adminAddr: addr, token: "x"}
	if err := client.call(http.MethodGet, "/v1/health", nil, nil, http.StatusOK); err == nil ||
		!strings.Contains(err.Error(), "daemon request failed") {
		t.Errorf("got %v", err)
	}
}

func TestGetDaemonClientReadsStateFromDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROT_DATA_DIR", dir)

	if _, err := getDaemonClient(); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("without daemon.json expected 'not running', got %v", err)
	}

	_ = os.WriteFile(filepath.Join(dir, "daemon.json"), []byte(`{"pid":1,"admin_addr":"127.0.0.1:4400"}`), 0o600)
	if _, err := getDaemonClient(); err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Errorf("without a token expected an auth token error, got %v", err)
	}

	_ = os.WriteFile(filepath.Join(dir, "auth.token"), []byte("secret\n"), 0o600)
	client, err := getDaemonClient()
	if err != nil || client.adminAddr != "127.0.0.1:4400" || client.token != "secret" {
		t.Errorf("got %+v, %v", client, err)
	}

	_ = os.WriteFile(filepath.Join(dir, "daemon.json"), []byte("{broken"), 0o600)
	if _, err := getDaemonClient(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("corrupt state should be reported, got %v", err)
	}
}
