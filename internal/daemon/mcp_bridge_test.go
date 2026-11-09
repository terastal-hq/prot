package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/mcp"
)

// bridgeSession drives the stdio bridge against a live daemon, one JSON-RPC call at a time.
type bridgeSession struct {
	t      *testing.T
	bridge *mcp.Bridge
	nextID int
}

func (s *bridgeSession) call(method, params string) map[string]interface{} {
	s.t.Helper()
	s.nextID++
	line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`+"\n", s.nextID, method, params)
	var out bytes.Buffer
	if err := s.bridge.ServeStdio(context.Background(), strings.NewReader(line), &out); err != nil {
		s.t.Fatalf("bridge: %v", err)
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &msg); err != nil {
		s.t.Fatalf("bad bridge output %q: %v", out.String(), err)
	}
	if msg["error"] != nil {
		s.t.Fatalf("%s returned a protocol error: %v", method, msg["error"])
	}
	return msg
}

func (s *bridgeSession) tool(name, args string) (string, bool) {
	s.t.Helper()
	msg := s.call("tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, args))
	result := msg["result"].(map[string]interface{})
	text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

func adminRequest(t *testing.T, d *Daemon, method, path string) []byte {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+d.AdminAddr()+path, nil)
	req.Header.Set("Authorization", "Bearer "+d.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, body)
	}
	return body
}

// An agent talking through "prot mcp" must see and control the same processes as the REST API.
func TestMCPBridgeSharesStateWithDaemon(t *testing.T) {
	d, err := New(Config{DataDir: t.TempDir(), GatewayAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = d.Stop(context.Background()) }()

	session := &bridgeSession{t: t, bridge: mcp.NewBridge(func() (mcp.Endpoint, error) {
		return mcp.Endpoint{URL: "http://" + d.AdminAddr() + "/mcp", Token: d.Token()}, nil
	})}
	session.call("initialize", `{"protocolVersion":"2024-11-05"}`)

	// A service created over REST is visible to the agent.
	body := `{"name":"Shared","alias":"shared","command":"sleep 60"}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+d.AdminAddr()+"/v1/services", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+d.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		t.Fatalf("create service: %v %v", err, resp)
	}
	resp.Body.Close()

	if list, _ := session.tool("list_services", `{}`); !strings.Contains(list, "Shared") {
		t.Fatalf("agent cannot see REST-created service: %s", list)
	}

	// Started by the agent, the process lives in the daemon: REST sees it and its logs.
	if text, isErr := session.tool("ensure_running", `{"id_or_alias":"shared"}`); isErr {
		t.Fatalf("ensure_running: %s", text)
	}
	var restView struct {
		Status string `json:"status"`
		PID    int    `json:"pid"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(adminRequest(t, d, http.MethodGet, "/v1/services/shared"), &restView); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if restView.PID > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if restView.PID == 0 {
		t.Fatalf("REST API does not see the process the agent started: %+v", restView)
	}
	restLogs := string(adminRequest(t, d, http.MethodGet, "/v1/services/shared/logs"))
	if !strings.Contains(restLogs, "Process started") {
		t.Errorf("REST logs missing agent-started process: %s", restLogs)
	}
	agentLogs, _ := session.tool("get_service_logs", `{"id_or_alias":"shared"}`)
	if !strings.Contains(agentLogs, "Process started") {
		t.Errorf("agent cannot read logs: %s", agentLogs)
	}

	// Stopped over REST, the agent observes it; the agent can then start it again.
	adminRequest(t, d, http.MethodPost, "/v1/services/shared/stop")
	view, _ := session.tool("get_service", `{"id_or_alias":"shared"}`)
	if !strings.Contains(view, `"status": "stopped"`) {
		t.Errorf("agent does not see REST stop: %s", view)
	}
	if text, isErr := session.tool("start_service", `{"id_or_alias":"shared"}`); isErr {
		t.Fatalf("restart via agent: %s", text)
	}
	if text, isErr := session.tool("stop_service", `{"id_or_alias":"shared"}`); isErr || !strings.Contains(text, "Stopped") {
		t.Errorf("agent stop: %q (err=%v)", text, isErr)
	}
}

// Without a token the daemon must refuse, and the bridge must surface that instead of hanging.
func TestMCPBridgeRejectedWithoutToken(t *testing.T) {
	d, err := New(Config{DataDir: t.TempDir(), GatewayAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = d.Stop(context.Background()) }()

	bridge := mcp.NewBridge(func() (mcp.Endpoint, error) {
		return mcp.Endpoint{URL: "http://" + d.AdminAddr() + "/mcp", Token: "wrong"}, nil
	})
	var out bytes.Buffer
	_ = bridge.ServeStdio(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if !strings.Contains(out.String(), "401") {
		t.Errorf("expected a 401 error from the bridge, got %q", out.String())
	}
}
