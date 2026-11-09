package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
)

func setupTestMCP(t *testing.T) (*Server, *registry.Registry, func()) {
	tempDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tempDir, "prot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	router := proxy.NewRouter()
	reg := registry.New(registry.Config{
		DB:          db,
		Router:      router,
		GatewayHost: "8080",
	})

	srv := NewServer(reg)
	cleanup := func() { _ = db.Close() }

	return srv, reg, cleanup
}

func TestMCPInitializeAndToolsList(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()

	ctx := context.Background()

	// 1. initialize
	initReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	}
	resp := srv.HandleRequest(ctx, initReq)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	resMap := resp.Result.(map[string]interface{})
	if resMap["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected 2024-11-05, got %v", resMap["protocolVersion"])
	}

	// 2. tools/list
	listReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}
	resp = srv.HandleRequest(ctx, listReq)
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	toolsMap := resp.Result.(map[string]interface{})
	tools := toolsMap["tools"].([]Tool)
	if len(tools) < 5 {
		t.Errorf("expected at least 5 tools, got %d", len(tools))
	}
}

func TestMCPToolCalls(t *testing.T) {
	srv, reg, cleanup := setupTestMCP(t)
	defer cleanup()

	ctx := context.Background()

	// Register a service directly in registry first
	_, err := reg.RegisterService(ctx, storage.Service{
		ID:       "svc-api",
		Name:     "Backend API",
		Alias:    "api",
		Port:     5000,
		AutoPort: false,
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// 1. tools/call: list_services
	callReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name": "list_services", "arguments": {}}`),
	}
	resp := srv.HandleRequest(ctx, callReq)
	if resp.Error != nil {
		t.Fatalf("tools/call list_services error: %+v", resp.Error)
	}
	res := resp.Result.(*ToolResult)
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "Backend API") {
		t.Errorf("list_services response didn't contain 'Backend API': %+v", res)
	}

	// 2. tools/call: get_service_url
	callReq2 := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      4,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name": "get_service_url", "arguments": {"id_or_alias": "api"}}`),
	}
	resp2 := srv.HandleRequest(ctx, callReq2)
	if resp2.Error != nil {
		t.Fatalf("tools/call get_service_url error: %+v", resp2.Error)
	}
	res2 := resp2.Result.(*ToolResult)
	if !strings.Contains(res2.Content[0].Text, "api.localhost") {
		t.Errorf("expected canonical URL in response, got %s", res2.Content[0].Text)
	}
}

func TestMCPStdioRoundTrip(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()

	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"
	inBuf := bytes.NewBufferString(input)
	outBuf := &bytes.Buffer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = srv.ServeStdio(ctx, inBuf, outBuf)

	var resp JSONRPCResponse
	if err := json.Unmarshal(outBuf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode stdio response: %v, raw=%s", err, outBuf.String())
	}
	if resp.ID != float64(1) {
		t.Errorf("expected ID 1, got %v", resp.ID)
	}
}

func TestMCPNotificationsGetNoResponse(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()

	for _, method := range []string{"notifications/initialized", "notifications/cancelled"} {
		if resp := srv.HandleRequest(context.Background(), JSONRPCRequest{JSONRPC: "2.0", Method: method}); resp != nil {
			t.Errorf("%s: expected no response, got %+v", method, resp)
		}
	}
	if resp := srv.HandleRequest(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 7, Method: "nope"}); resp == nil ||
		resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown request method should return -32601, got %+v", resp)
	}
}

func TestMCPReportsConfiguredVersion(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()
	srv.SetVersion("9.9.9")

	resp := srv.HandleRequest(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize"})
	data, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(data), `"version":"9.9.9"`) {
		t.Errorf("version missing from initialize result: %s", data)
	}
}
