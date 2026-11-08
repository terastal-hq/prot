package registry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/storage"
)

func TestRegistryLifecycleAndRouting(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "prot.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	router := proxy.NewRouter()

	reg := New(Config{
		DB:          db,
		Router:      router,
		GatewayHost: "8080",
	})

	ctx := context.Background()

	// Subscribe to events
	evCh, unsub := reg.Subscribe()
	defer unsub()

	// 1. Register a managed service
	svc := storage.Service{
		ID:       "svc-worker",
		Name:     "Background Worker",
		Alias:    "worker",
		Command:  "sh -c 'sleep 5'",
		Port:     9100,
		AutoPort: false,
	}

	view, err := reg.RegisterService(ctx, svc)
	if err != nil {
		t.Fatalf("RegisterService failed: %v", err)
	}
	if view.CanonicalURL != "http://worker.localhost:8080" {
		t.Errorf("CanonicalURL unexpected: %s", view.CanonicalURL)
	}

	// Verify route created in proxy router
	rt, ok := router.Match("worker.localhost")
	if !ok || rt.TargetURL != "http://127.0.0.1:9100" {
		t.Errorf("expected router to have worker route, got %+v", rt)
	}

	// Verify event received
	select {
	case ev := <-evCh:
		if ev.Type != "service.registered" {
			t.Errorf("expected event type service.registered, got %s", ev.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for event")
	}

	// 2. Start service
	started, err := reg.StartService(ctx, "svc-worker")
	if err != nil {
		t.Fatalf("StartService failed: %v", err)
	}
	if started.Status != "starting" && started.Status != "running" {
		t.Errorf("expected status starting/running, got %s", started.Status)
	}

	// 3. Stop service
	stopped, err := reg.StopService(ctx, "svc-worker")
	if err != nil {
		t.Fatalf("StopService failed: %v", err)
	}
	if stopped.Status != "stopped" {
		t.Errorf("expected status stopped, got %s", stopped.Status)
	}

	// 4. Delete service
	if err := reg.DeleteService(ctx, "svc-worker"); err != nil {
		t.Fatalf("DeleteService failed: %v", err)
	}
	_, ok = router.Match("worker.localhost")
	if ok {
		t.Errorf("expected route to be removed after delete")
	}
}

func TestRegistryAdoptDiscovered(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "prot.db")
	db, _ := storage.Open(dbPath)
	defer db.Close()

	router := proxy.NewRouter()
	reg := New(Config{DB: db, Router: router})

	// Seed discovered service
	disc := discovery.ProcessService{
		ID:          "disc-1",
		PID:         5555,
		Name:        "vite-dev",
		Port:        5173,
		Fingerprint: "abcdef1234567890",
		Cwd:         "/app",
	}
	reg.SetDiscovered([]discovery.ProcessService{disc})

	adopted, err := reg.AdoptDiscovered(context.Background(), "disc-1", "Vite App", "app", "npm run dev")
	if err != nil {
		t.Fatalf("AdoptDiscovered failed: %v", err)
	}
	if adopted.Alias != "app" || adopted.Port != 5173 {
		t.Errorf("unexpected adopted service: %+v", adopted)
	}

	// Verify route is live
	rt, ok := router.Match("app.localhost")
	if !ok || rt.TargetURL != "http://127.0.0.1:5173" {
		t.Errorf("router missing adopted route: %+v", rt)
	}
}
