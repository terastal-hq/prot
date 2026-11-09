package registry

import (
	"context"
	"errors"
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

	adopted, err := reg.AdoptDiscovered(context.Background(), AdoptOptions{
		DiscoveryID: "disc-1",
		Name:        "Vite App",
		Alias:       "app",
		Command:     "npm run dev",
	})
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

func TestRegistryRenameAlias(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	router := proxy.NewRouter()
	reg := New(Config{DB: db, Router: router, GatewayHost: "8080"})
	ctx := context.Background()

	for _, svc := range []storage.Service{
		{ID: "svc-a", Name: "A", Alias: "alpha", Command: "true", Port: 9201},
		{ID: "svc-b", Name: "B", Alias: "beta", Command: "true", Port: 9202},
	} {
		if _, err := reg.RegisterService(ctx, svc); err != nil {
			t.Fatalf("RegisterService: %v", err)
		}
	}

	view, err := reg.RenameAlias(ctx, "svc-a", " Gamma ")
	if err != nil {
		t.Fatalf("RenameAlias: %v", err)
	}
	if view.Alias != "gamma" || view.CanonicalURL != "http://gamma.localhost:8080" {
		t.Errorf("unexpected view after rename: %+v", view)
	}
	if _, ok := router.Match("alpha.localhost"); ok {
		t.Error("old route should be removed")
	}
	if rt, ok := router.Match("gamma.localhost"); !ok || rt.TargetURL != "http://127.0.0.1:9201" {
		t.Errorf("new route missing or wrong: %+v", rt)
	}

	if _, err := reg.RenameAlias(ctx, "svc-a", "beta"); !errors.Is(err, ErrAliasTaken) {
		t.Errorf("expected ErrAliasTaken, got %v", err)
	}
	for _, bad := range []string{"", "-x", "x-", "has space", "a.b", "UP_per"} {
		if _, err := reg.RenameAlias(ctx, "svc-a", bad); !errors.Is(err, ErrInvalidAlias) {
			t.Errorf("alias %q: expected ErrInvalidAlias, got %v", bad, err)
		}
	}
	if _, err := reg.RenameAlias(ctx, "nope", "delta"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRegistryUpdateService(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	router := proxy.NewRouter()
	reg := New(Config{DB: db, Router: router, GatewayHost: "8080"})
	ctx := context.Background()

	if _, err := reg.RegisterService(ctx, storage.Service{
		ID: "svc-u", Name: "U", Alias: "upd", Command: "true", Port: 9301, RestartPolicy: "on-failure",
	}); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}

	str := func(v string) *string { return &v }
	flag := func(v bool) *bool { return &v }
	port := uint32(9302)
	env := map[string]string{"A": "1"}

	view, err := reg.UpdateService(ctx, "svc-u", ServicePatch{
		Name: str(" Renamed "), Command: str("echo hi"), Cwd: str("/tmp"), Env: &env, Port: &port,
		AutoStart: flag(true), AutoPort: flag(true), LANExposed: flag(true), RestartPolicy: str("always"),
	})
	if err != nil {
		t.Fatalf("UpdateService: %v", err)
	}
	if view.Name != "Renamed" || view.Command != "echo hi" || view.Cwd != "/tmp" || view.Env["A"] != "1" ||
		view.Port != 9302 || !view.AutoStart || !view.AutoPort || !view.LANExposed || view.RestartPolicy != "always" {
		t.Errorf("fields not applied: %+v", view)
	}
	if rt, ok := router.Match("upd.localhost"); !ok || rt.TargetURL != "http://127.0.0.1:9302" || !rt.LANExposed {
		t.Errorf("route not synced for stopped service: %+v", rt)
	}

	stored, _ := db.GetService(ctx, "svc-u")
	if stored.Command != "echo hi" || !stored.AutoStart {
		t.Errorf("not persisted: %+v", stored)
	}

	bigPort := uint32(70000)
	bad := []ServicePatch{
		{Name: str("  ")}, {Command: str("")}, {RestartPolicy: str("sometimes")}, {Port: &bigPort},
		{Env: &map[string]string{"A=B": "x"}},
	}
	for i, p := range bad {
		if _, err := reg.UpdateService(ctx, "svc-u", p); !errors.Is(err, ErrInvalidField) {
			t.Errorf("case %d: expected ErrInvalidField, got %v", i, err)
		}
	}
	if after, _ := db.GetService(ctx, "svc-u"); after.Name != "Renamed" {
		t.Errorf("rejected patches must not change data: %+v", after)
	}
}
