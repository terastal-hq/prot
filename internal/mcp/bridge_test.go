package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func staticEndpoint(url, token string) Resolver {
	return func() (Endpoint, error) { return Endpoint{URL: url, Token: token}, nil }
}

func runBridge(t *testing.T, resolve Resolver, input string) []map[string]interface{} {
	t.Helper()
	var out bytes.Buffer
	if err := NewBridge(resolve).ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	var msgs []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bridge wrote invalid JSON %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// The bridge must run requests inside the real MCP server, with the token attached.
func TestBridgeForwardsToServerWithToken(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()

	var sawAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		srv.ServeHTTP(w, r)
	}))
	defer ts.Close()

	msgs := runBridge(t, staticEndpoint(ts.URL, "s3cret"), strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`,
		``,
	}, "\n"))

	if sawAuth != "Bearer s3cret" {
		t.Errorf("Authorization header = %q", sawAuth)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 replies (notification is silent), got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["id"] != float64(1) || msgs[1]["id"] != "abc" {
		t.Errorf("ids not preserved: %v %v", msgs[0]["id"], msgs[1]["id"])
	}
	if tools := msgs[1]["result"].(map[string]interface{})["tools"].([]interface{}); len(tools) == 0 {
		t.Error("tools/list returned no tools")
	}
}

func TestBridgeReportsUnavailableDaemon(t *testing.T) {
	resolve := func() (Endpoint, error) { return Endpoint{}, errors.New("daemon is not running (start with 'protd')") }
	msgs := runBridge(t, resolve, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`+"\n"+
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	if len(msgs) != 1 {
		t.Fatalf("expected one error reply, got %v", msgs)
	}
	errObj := msgs[0]["error"].(map[string]interface{})
	if errObj["code"] != float64(codeDaemonUnavailable) || !strings.Contains(errObj["message"].(string), "protd") {
		t.Errorf("unexpected error: %v", errObj)
	}
	if msgs[0]["id"] != float64(5) {
		t.Errorf("error must echo the request id, got %v", msgs[0]["id"])
	}
}

func TestBridgeReportsConnectionRefused(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // nothing listens any more

	msgs := runBridge(t, staticEndpoint(url, "x"), `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	if len(msgs) != 1 || msgs[0]["error"] == nil {
		t.Fatalf("expected error reply, got %v", msgs)
	}
}

func TestBridgeReportsRejectedToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer ts.Close()

	msgs := runBridge(t, staticEndpoint(ts.URL, "bad"), `{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	errObj := msgs[0]["error"].(map[string]interface{})
	if errObj["code"] != float64(codeDaemonRejected) || !strings.Contains(errObj["message"].(string), "401") {
		t.Errorf("unexpected error: %v", errObj)
	}
}

func TestBridgeParseError(t *testing.T) {
	msgs := runBridge(t, staticEndpoint("http://127.0.0.1:1", ""), "not json\n")
	if len(msgs) != 1 || msgs[0]["error"].(map[string]interface{})["code"] != float64(-32700) {
		t.Fatalf("expected parse error, got %v", msgs)
	}
	if id, ok := msgs[0]["id"]; !ok || id != nil {
		t.Errorf("parse error id must be null, got %v (present=%v)", id, ok)
	}
}

// A daemon that was down for the first message and up for the second must be picked up.
func TestBridgeRecoversWhenDaemonStarts(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer ts.Close()

	calls := 0
	resolve := func() (Endpoint, error) {
		calls++
		if calls == 1 {
			return Endpoint{}, errors.New("daemon is not running")
		}
		return Endpoint{URL: ts.URL}, nil
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
	msgs := runBridge(t, resolve, input)
	if len(msgs) != 2 || msgs[0]["error"] == nil || msgs[1]["result"] == nil {
		t.Fatalf("expected error then success, got %v", msgs)
	}
}
