package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func userVersion(t *testing.T, db *DB) int {
	t.Helper()
	var v int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func tableExists(t *testing.T, db *DB, name string) bool {
	t.Helper()
	var n int
	err := db.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestFreshDatabaseIsFullyMigrated(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if got := userVersion(t, db); got != len(migrations) {
		t.Errorf("user_version = %d, want %d", got, len(migrations))
	}
	for _, table := range []string{"services", "tunnels", "event_logs"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %s missing", table)
		}
	}
	if tableExists(t, db, "workspaces") {
		t.Error("the unused workspaces table must not be created")
	}
}

// A database written by an early build (no user_version, with workspace tables and data) must keep its data.
func TestLegacyDatabaseIsUpgradedInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prot.db")
	raw := openRaw(t, path)
	for _, stmt := range []string{migrations[0], `
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, created_at TEXT NOT NULL
		);
		CREATE TABLE workspace_services (workspace_id TEXT NOT NULL, service_id TEXT NOT NULL);`,
		`INSERT INTO services VALUES ('svc-old','Old','managed','stopped','old','sleep 1','[]','','{}',0,0,0,'no',0,
			'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seeding legacy db: %v", err)
		}
	}
	_ = raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer db.Close()

	if got := userVersion(t, db); got != len(migrations) {
		t.Errorf("user_version = %d, want %d", got, len(migrations))
	}
	if tableExists(t, db, "workspaces") || tableExists(t, db, "workspace_services") {
		t.Error("workspace tables should have been dropped")
	}
	svc, err := db.GetService(context.Background(), "svc-old")
	if err != nil || svc.Name != "Old" {
		t.Errorf("existing data lost: %+v, %v", svc, err)
	}
}

func TestReopeningDoesNotReapplyMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prot.db")
	for i := 0; i < 3; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		_ = db.Close()
	}
}

func TestDatabaseFromNewerProtIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prot.db")
	raw := openRaw(t, path)
	if _, err := raw.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	_, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), "newer than this prot") {
		t.Errorf("expected a downgrade refusal, got %v", err)
	}
}

func TestResolveService(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, svc := range []Service{
		{ID: "svc-a", Name: "A", Alias: "web"},
		{ID: "web", Name: "B (id collides with A's alias)", Alias: ""},
		{ID: "svc-c", Name: "C", Alias: ""},
	} {
		if err := db.SaveService(ctx, svc); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := db.ResolveService(ctx, "svc-a"); err != nil || got.ID != "svc-a" {
		t.Errorf("by id: %+v, %v", got, err)
	}
	if got, err := db.ResolveService(ctx, "web"); err != nil || got.ID != "web" {
		t.Errorf("an exact id match must win over an alias match, got %+v, %v", got, err)
	}
	if got, err := db.ResolveService(ctx, "svc-a"); err != nil || got.Alias != "web" {
		t.Errorf("alias not loaded: %+v, %v", got, err)
	}
	// Services without an alias store "": looking up "" must not return an arbitrary one of them.
	if _, err := db.ResolveService(ctx, ""); err != ErrNotFound {
		t.Errorf("empty identifier must be ErrNotFound, got %v", err)
	}
	if _, err := db.ResolveService(ctx, "nope"); err != ErrNotFound {
		t.Errorf("unknown identifier must be ErrNotFound, got %v", err)
	}
}

func TestCorruptRowIsReportedNotSwallowed(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if err := db.SaveService(ctx, Service{ID: "svc-bad", Name: "Bad"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("UPDATE services SET env_json = '{not json' WHERE id = 'svc-bad'"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.GetService(ctx, "svc-bad"); err == nil || !strings.Contains(err.Error(), "decoding env") {
		t.Errorf("GetService should report corrupt env, got %v", err)
	}
	if _, err := db.ListServices(ctx); err == nil {
		t.Error("ListServices should report corrupt rows")
	}
}

func TestNilArgsAndEnvRoundTripAsEmpty(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if err := db.SaveService(ctx, Service{ID: "svc-nil", Name: "Nil"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetService(ctx, "svc-nil")
	if err != nil {
		t.Fatal(err)
	}
	if got.Args == nil || got.Env == nil {
		t.Errorf("Args and Env must be non-nil so they serialise as [] and {}: %+v", got)
	}
}

func TestEventLogIsTrimmed(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	total := maxEventLogRows + pruneEveryWrites*2
	for i := 0; i < total; i++ {
		if err := db.LogEvent(ctx, "test", "svc", "payload"); err != nil {
			t.Fatal(err)
		}
	}

	var n int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM event_logs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Trimming runs every pruneEveryWrites inserts, so the table may overshoot by up to that many rows.
	if n > maxEventLogRows+pruneEveryWrites || n < maxEventLogRows {
		t.Errorf("event_logs has %d rows after %d inserts, want about %d", n, total, maxEventLogRows)
	}
	recent, err := db.GetRecentEvents(ctx, 1)
	if err != nil || len(recent) != 1 {
		t.Fatalf("newest event missing after trimming: %v %v", recent, err)
	}
}

// A database from before tunnels could have a password: its tunnels must survive with the flag off.
func TestTunnelsGainTheHasPasswordFlagWithoutLosingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prot.db")
	raw := openRaw(t, path)
	for _, stmt := range []string{
		migrations[0], migrations[1], "PRAGMA user_version = 2",
		`INSERT INTO tunnels VALUES ('t-old','db','bastion','me','127.0.0.1',5432,15432,'','[]',1,'2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seeding v2 database: %v", err)
		}
	}
	_ = raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	got, err := db.ListTunnels(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("tunnels after upgrade: %+v, %v", got, err)
	}
	if got[0].Alias != "db" || got[0].RemotePort != 5432 || !got[0].AutoConnect || got[0].HasPassword {
		t.Errorf("upgraded row = %+v", got[0])
	}

	got[0].HasPassword = true
	if err := db.SaveTunnel(ctx, got[0]); err != nil {
		t.Fatal(err)
	}
	if again, _ := db.ListTunnels(ctx); !again[0].HasPassword {
		t.Error("HasPassword did not round-trip")
	}
}
