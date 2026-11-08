package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStorageServicesCRUD(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "prot.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	svc := Service{
		ID:            "svc-web",
		Name:          "Frontend Web",
		Runtime:       "managed",
		Status:        "stopped",
		Alias:         "frontend",
		Command:       "npm run dev",
		Args:          []string{"--", "--port", "3000"},
		Cwd:           "/home/user/project",
		Env:           map[string]string{"NODE_ENV": "development"},
		Port:          3000,
		AutoPort:      true,
		AutoStart:     false,
		RestartPolicy: "on-failure",
		LANExposed:    false,
	}

	// 1. Save
	if err := db.SaveService(ctx, svc); err != nil {
		t.Fatalf("SaveService failed: %v", err)
	}

	// 2. Get
	retrieved, err := db.GetService(ctx, "svc-web")
	if err != nil {
		t.Fatalf("GetService failed: %v", err)
	}
	if retrieved.Name != "Frontend Web" || retrieved.Port != 3000 || len(retrieved.Args) != 3 {
		t.Errorf("Retrieved service fields mismatch: %+v", retrieved)
	}
	if retrieved.Env["NODE_ENV"] != "development" {
		t.Errorf("Env map not preserved: %+v", retrieved.Env)
	}

	// 3. Update status
	if err := db.UpdateServiceStatus(ctx, "svc-web", "running"); err != nil {
		t.Fatalf("UpdateServiceStatus failed: %v", err)
	}
	retrieved2, _ := db.GetService(ctx, "svc-web")
	if retrieved2.Status != "running" {
		t.Errorf("expected status 'running', got %s", retrieved2.Status)
	}

	// 4. List
	list, err := db.ListServices(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListServices error: %v, len=%d", err, len(list))
	}

	// 5. Delete
	if err := db.DeleteService(ctx, "svc-web"); err != nil {
		t.Fatalf("DeleteService failed: %v", err)
	}
	_, err = db.GetService(ctx, "svc-web")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestStorageTunnelsAndEvents(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "prot.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	tun := Tunnel{
		ID:          "tun-db",
		Alias:       "remote-postgres",
		SSHHost:     "ssh.example.com",
		SSHUser:     "ubuntu",
		RemoteHost:  "127.0.0.1",
		RemotePort:  5432,
		LocalPort:   15432,
		AutoConnect: true,
	}

	if err := db.SaveTunnel(ctx, tun); err != nil {
		t.Fatalf("SaveTunnel failed: %v", err)
	}

	tuns, err := db.ListTunnels(ctx)
	if err != nil || len(tuns) != 1 {
		t.Fatalf("ListTunnels error: %v, len=%d", err, len(tuns))
	}
	if tuns[0].Alias != "remote-postgres" || !tuns[0].AutoConnect {
		t.Errorf("Tunnel fields mismatch: %+v", tuns[0])
	}

	// Test event logs
	_ = db.LogEvent(ctx, "service.start", "svc-web", `{"port": 3000}`)
	_ = db.LogEvent(ctx, "service.stop", "svc-web", `{"exit_code": 0}`)

	events, err := db.GetRecentEvents(ctx, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("GetRecentEvents error: %v, len=%d", err, len(events))
	}
	// Most recent first
	if events[0].Type != "service.stop" {
		t.Errorf("expected most recent event first, got %s", events[0].Type)
	}
}
