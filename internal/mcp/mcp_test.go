package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	unknown := JSONRPCRequest{JSONRPC: "2.0", ID: 7, Method: "nope"}
	if resp := srv.HandleRequest(context.Background(), unknown); resp == nil || resp.Error == nil ||
		resp.Error.Code != -32601 {
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

func callTool(t *testing.T, srv *Server, name, args string) (string, bool) {
	t.Helper()
	params := fmt.Sprintf(`{"name": %q, "arguments": %s}`, name, args)
	resp := srv.HandleRequest(context.Background(), JSONRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: json.RawMessage(params),
	})
	if resp.Error != nil {
		t.Fatalf("%s: protocol error %+v", name, resp.Error)
	}
	var res *ToolResult
	switch v := resp.Result.(type) {
	case *ToolResult:
		res = v
	case ToolResult:
		res = &v
	default:
		t.Fatalf("%s: unexpected result type %T", name, resp.Result)
	}
	return res.Content[0].Text, res.IsError
}

// Every tool that targets a service must advertise id_or_alias and accept the legacy "id" spelling.
func TestMCPServiceToolsShareArgumentName(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()

	for _, tool := range srv.listTools() {
		schema, _ := json.Marshal(tool.InputSchema)
		switch tool.Name {
		case "list_services":
			continue
		}
		if !strings.Contains(string(schema), `"id_or_alias"`) || strings.Contains(string(schema), `"id":`) {
			t.Errorf("%s schema should use id_or_alias: %s", tool.Name, schema)
		}
	}

	for _, args := range []string{`{}`, `{"id_or_alias": "  "}`} {
		text, isErr := callTool(t, srv, "stop_service", args)
		if !isErr || !strings.Contains(text, "id_or_alias is required") {
			t.Errorf("stop_service %s should demand id_or_alias, got %q (err=%v)", args, text, isErr)
		}
	}
}

func TestMCPControlToolsDriveTheSupervisor(t *testing.T) {
	srv, reg, cleanup := setupTestMCP(t)
	defer cleanup()
	defer reg.StopAll(2 * time.Second)

	ctx := context.Background()
	if _, err := reg.RegisterService(ctx, storage.Service{
		ID: "svc-sleep", Name: "Sleeper", Alias: "sleeper", Command: "sleep 60", Cwd: t.TempDir(),
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if text, isErr := callTool(t, srv, "start_service", `{"id_or_alias": "sleeper"}`); isErr {
		t.Fatalf("start_service: %s", text)
	}
	logs, isErr := callTool(t, srv, "get_service_logs", `{"id_or_alias": "sleeper"}`)
	if isErr || !strings.Contains(logs, "Process started") {
		t.Errorf("logs should show the start line, got %q (err=%v)", logs, isErr)
	}

	// Legacy "id" still works for control tools.
	if text, isErr := callTool(t, srv, "restart_service", `{"id": "svc-sleep"}`); isErr {
		t.Fatalf("restart_service: %s", text)
	}
	text, isErr := callTool(t, srv, "stop_service", `{"id_or_alias": "sleeper"}`)
	if isErr || !strings.Contains(text, "Stopped") {
		t.Fatalf("stop_service: %q (err=%v)", text, isErr)
	}
}

func TestMCPSetAliasRenamesAndRoutes(t *testing.T) {
	srv, reg, cleanup := setupTestMCP(t)
	defer cleanup()

	ctx := context.Background()
	svc := storage.Service{ID: "svc-a", Name: "A", Alias: "old", Port: 5001}
	if _, err := reg.RegisterService(ctx, svc); err != nil {
		t.Fatalf("register: %v", err)
	}

	text, isErr := callTool(t, srv, "set_alias", `{"id_or_alias": "old", "alias": "fresh"}`)
	if isErr || !strings.Contains(text, "fresh.localhost") {
		t.Fatalf("set_alias: %q (err=%v)", text, isErr)
	}
	if _, err := reg.GetService(ctx, "fresh"); err != nil {
		t.Errorf("service should resolve by the new alias: %v", err)
	}

	for _, bad := range []string{`{"id_or_alias": "fresh", "alias": "Not Valid!"}`, `{"id_or_alias": "fresh"}`} {
		if text, isErr := callTool(t, srv, "set_alias", bad); !isErr {
			t.Errorf("set_alias %s should fail, got %q", bad, text)
		}
	}
}
