package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
)

func TestAddServiceRegistersAndDefaultsAlias(t *testing.T) {
	srv, reg, cleanup := setupTestMCP(t)
	defer cleanup()

	text, isErr := callTool(t, srv, "add_service", `{
		"name": "Docs Site", "cmd": "npm run dev", "cwd": "/tmp", "port": 4321,
		"env": {"PATH": "/opt/node/bin:/usr/bin"}, "auto_start": true
	}`)
	if isErr {
		t.Fatalf("add_service: %s", text)
	}
	if !strings.Contains(text, "docs-site.localhost") {
		t.Errorf("result should show the default alias URL, got %s", text)
	}

	svc, err := reg.GetService(context.Background(), "docs-site")
	if err != nil {
		t.Fatalf("service not registered under its default alias: %v", err)
	}
	if svc.Command != "npm run dev" || svc.Port != 4321 || !svc.AutoStart ||
		svc.Env["PATH"] != "/opt/node/bin:/usr/bin" || svc.RestartPolicy != storage.RestartOnFailure {
		t.Errorf("unexpected stored service: %+v", svc)
	}
}

func TestAddServiceRejectsBadInput(t *testing.T) {
	srv, _, cleanup := setupTestMCP(t)
	defer cleanup()

	if _, isErr := callTool(t, srv, "add_service", `{"name": "A", "cmd": "sleep 1", "alias": "taken"}`); isErr {
		t.Fatal("first registration should succeed")
	}

	cases := map[string]struct{ args, want string }{
		"missing command":   {`{"name": "B"}`, "command cannot be empty"},
		"missing name":      {`{"cmd": "x"}`, "name cannot be empty"},
		"duplicate alias":   {`{"name": "C", "cmd": "x", "alias": "taken"}`, "already used"},
		"invalid alias":     {`{"name": "D", "cmd": "x", "alias": "Not_Valid"}`, "alias must be"},
		"port out of range": {`{"name": "E", "cmd": "x", "port": 70000}`, "port"},
		"fractional port":   {`{"name": "F", "cmd": "x", "port": 80.5}`, "port"},
		"non-string env":    {`{"name": "G", "cmd": "x", "env": {"A": 1}}`, "must be a string"},
		"bad env name":      {`{"name": "H", "cmd": "x", "env": {"A=B": "1"}}`, "environment variable"},
		"bad policy":        {`{"name": "I", "cmd": "x", "restart_policy": "sometimes"}`, "restart_policy"},
		"wrong type":        {`{"name": "J", "cmd": "x", "auto_port": "yes"}`, "auto_port"},
	}
	for name, tc := range cases {
		text, isErr := callTool(t, srv, "add_service", tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s: want error containing %q, got %q (err=%v)", name, tc.want, text, isErr)
		}
	}
}

func TestAddServiceCanStartImmediately(t *testing.T) {
	srv, reg, cleanup := setupTestMCP(t)
	defer cleanup()
	defer reg.StopAll(2 * time.Second)

	text, isErr := callTool(t, srv, "add_service", `{"name": "Sleeper", "cmd": "sleep 60", "cwd": "`+t.TempDir()+`", "start": true}`)
	if isErr || !strings.Contains(text, "started") {
		t.Fatalf("add_service with start: %q (err=%v)", text, isErr)
	}
	svc, err := reg.GetService(context.Background(), "sleeper")
	if err != nil || svc.PID == 0 {
		t.Errorf("service should be running, got %+v (err=%v)", svc, err)
	}
}

func TestUpdateServiceChangesOnlyGivenFields(t *testing.T) {
	srv, reg, cleanup := setupTestMCP(t)
	defer cleanup()

	if text, isErr := callTool(t, srv, "add_service",
		`{"name": "Web", "cmd": "old", "port": 3000, "env": {"A": "1"}}`); isErr {
		t.Fatal(text)
	}

	text, isErr := callTool(t, srv, "update_service",
		`{"id_or_alias": "web", "cmd": "new --flag", "env": {"B": "2"}, "port": 3001}`)
	if isErr {
		t.Fatalf("update_service: %s", text)
	}
	svc, err := reg.GetService(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Command != "new --flag" || svc.Port != 3001 || svc.Name != "Web" ||
		len(svc.Env) != 1 || svc.Env["B"] != "2" {
		t.Errorf("unexpected service after update: %+v", svc)
	}

	if text, isErr := callTool(t, srv, "update_service", `{"id_or_alias": "web", "env": {}}`); isErr {
		t.Fatal(text)
	}
	if svc, _ = reg.GetService(context.Background(), "web"); len(svc.Env) != 0 {
		t.Errorf("empty env object should clear the environment, got %v", svc.Env)
	}

	if text, isErr := callTool(t, srv, "update_service", `{"id_or_alias": "nope", "cmd": "x"}`); !isErr {
		t.Errorf("unknown service should be an error, got %q", text)
	}
	if text, isErr := callTool(t, srv, "update_service", `{"id_or_alias": "web", "cmd": ""}`); !isErr ||
		!strings.Contains(text, "command cannot be empty") {
		t.Errorf("emptying the command should be rejected, got %q (err=%v)", text, isErr)
	}
}

func TestAgentChangesAreAttributedInTheEventLog(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(registry.New(registry.Config{DB: db, Router: proxy.NewRouter(), GatewayHost: "8080"}))

	callTool(t, srv, "add_service", `{"name": "Web", "cmd": "serve", "env": {"TOKEN": "hunter2"}}`)
	callTool(t, srv, "update_service", `{"id_or_alias": "web", "cmd": "serve --fast", "env": {"TOKEN": "hunter3"}}`)

	events, err := db.GetRecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var registered, updated string
	for _, e := range events {
		switch e.Type {
		case "service.registered":
			registered = e.Payload
		case "service.updated":
			updated = e.Payload
		}
	}
	for name, payload := range map[string]string{"registered": registered, "updated": updated} {
		if !strings.Contains(payload, `"source":"mcp"`) {
			t.Errorf("%s event should be attributed to mcp, got %q", name, payload)
		}
		if strings.Contains(payload, "hunter") {
			t.Errorf("%s event must not contain environment values: %q", name, payload)
		}
	}
	if !strings.Contains(updated, "serve --fast") || !strings.Contains(updated, "TOKEN") {
		t.Errorf("update event should list what changed, got %q", updated)
	}
}
