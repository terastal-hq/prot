package registry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/secrets"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

const testPassword = "hunter2-do-not-leak"

func TestPasswordGoesToTheKeyringAndNowhereElse(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()

	view, err := env.reg.RegisterTunnelWithPassword(ctx, tunnelCfg(t, "pw", false), testPassword)
	if err != nil {
		t.Fatalf("RegisterTunnelWithPassword: %v", err)
	}
	if !view.Config.HasPassword {
		t.Error("view must say a password is stored")
	}

	got, err := env.secrets.Get(secrets.TunnelKey(view.Config.ID))
	if err != nil || got != testPassword {
		t.Errorf("keyring holds %q, %v", got, err)
	}

	// The secret must not appear in anything prot returns or writes.
	for name, v := range map[string]any{"view": view, "list": env.reg.ListTunnels()} {
		data, _ := json.Marshal(v)
		if strings.Contains(string(data), testPassword) {
			t.Errorf("%s leaks the password: %s", name, data)
		}
	}
	saved, _ := env.db.ListTunnels(ctx)
	data, _ := json.Marshal(saved)
	if len(saved) != 1 || !saved[0].HasPassword || strings.Contains(string(data), testPassword) {
		t.Errorf("database row: %s", data)
	}
}

func TestPasswordRefusedWhenThereIsNoSafePlace(t *testing.T) {
	ctx := context.Background()

	// no store at all
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("PROT_FAKE_SSH", "1")
	mgr := tunnels.NewManagerWithSSH("/bin/true")
	bare := New(Config{DB: db, Router: proxy.NewRouter(), Tunnels: mgr, GatewayHost: "8080"})
	_, err = bare.RegisterTunnelWithPassword(ctx, tunnelCfg(t, "a", false), testPassword)
	if !errors.Is(err, ErrPasswordsUnavailable) {
		t.Errorf("no store: %v", err)
	}
	if len(bare.ListTunnels()) != 0 {
		t.Error("a refused registration must not leave a tunnel behind")
	}

	// a store that fails (no Secret Service running)
	env := newTunnelEnv(t, nil)
	env.secrets.Err = secrets.ErrUnavailable
	_, err = env.reg.RegisterTunnelWithPassword(ctx, tunnelCfg(t, "b", false), testPassword)
	if !errors.Is(err, ErrPasswordsUnavailable) {
		t.Errorf("failing store: %v", err)
	}
	if len(env.reg.ListTunnels()) != 0 {
		t.Error("tunnel registered although its password could not be stored")
	}
	if saved, _ := env.db.ListTunnels(ctx); len(saved) != 0 {
		t.Errorf("tunnel saved although its password could not be stored: %+v", saved)
	}
}

func TestInvalidPasswordsAreRejected(t *testing.T) {
	env := newTunnelEnv(t, nil)
	for _, pw := range []string{"two\nlines", "carriage\rreturn"} {
		_, err := env.reg.RegisterTunnelWithPassword(context.Background(), tunnelCfg(t, "bad", false), pw)
		if !errors.Is(err, ErrInvalidField) {
			t.Errorf("password %q: %v", pw, err)
		}
	}
	if env.secrets.Len() != 0 {
		t.Error("nothing must be stored for a rejected password")
	}
}

// A failed registration must not leave a secret behind in the keyring.
func TestFailedRegistrationRemovesTheStoredPassword(t *testing.T) {
	env := newTunnelEnv(t, nil)
	cfg := tunnelCfg(t, "cleanup", false)
	cfg.SSHHost = "-oProxyCommand=evil" // rejected by the tunnel manager, after the password was stored

	if _, err := env.reg.RegisterTunnelWithPassword(context.Background(), cfg, testPassword); err == nil {
		t.Fatal("expected a validation error")
	}
	if env.secrets.Len() != 0 {
		t.Error("the keyring still holds the password of a tunnel that was never created")
	}
}

func TestSetAndClearPassword(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	view, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "later", false))
	if err != nil {
		t.Fatal(err)
	}
	if view.Config.HasPassword {
		t.Fatal("no password was given")
	}
	key := secrets.TunnelKey(view.Config.ID)

	updated, err := env.reg.SetTunnelPassword(ctx, "later", testPassword)
	if err != nil || !updated.Config.HasPassword {
		t.Fatalf("SetTunnelPassword: %+v, %v", updated, err)
	}
	if got, _ := env.secrets.Get(key); got != testPassword {
		t.Errorf("keyring holds %q", got)
	}
	if saved, _ := env.db.ListTunnels(ctx); !saved[0].HasPassword {
		t.Error("the flag was not persisted")
	}

	if _, err := env.reg.SetTunnelPassword(ctx, "later", "replaced"); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.secrets.Get(key); got != "replaced" {
		t.Errorf("replace failed: %q", got)
	}

	cleared, err := env.reg.ClearTunnelPassword(ctx, view.Config.ID)
	if err != nil || cleared.Config.HasPassword {
		t.Fatalf("ClearTunnelPassword: %+v, %v", cleared, err)
	}
	if _, err := env.secrets.Get(key); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("secret still in the keyring: %v", err)
	}
	if saved, _ := env.db.ListTunnels(ctx); saved[0].HasPassword {
		t.Error("the cleared flag was not persisted")
	}

	if _, err := env.reg.SetTunnelPassword(ctx, "ghost", "x"); !errors.Is(err, tunnels.ErrNotFound) {
		t.Errorf("unknown tunnel: %v", err)
	}
	if _, err := env.reg.SetTunnelPassword(ctx, "later", ""); !errors.Is(err, ErrInvalidField) {
		t.Errorf("empty password: %v", err)
	}
}

func TestDeletingATunnelRemovesItsPassword(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	if _, err := env.reg.RegisterTunnelWithPassword(ctx, tunnelCfg(t, "gone", false), testPassword); err != nil {
		t.Fatal(err)
	}
	if env.secrets.Len() != 1 {
		t.Fatal("password not stored")
	}
	if err := env.reg.DeleteTunnel(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if env.secrets.Len() != 0 {
		t.Error("the keyring still holds the password of a deleted tunnel")
	}
}

// After a daemon restart the flag comes back from the database and the secret from the keyring.
func TestRestoredTunnelStillHasItsPassword(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	first := newTunnelEnv(t, db)
	view, err := first.reg.RegisterTunnelWithPassword(ctx, tunnelCfg(t, "persist", false), testPassword)
	if err != nil {
		t.Fatal(err)
	}

	second := newTunnelEnv(t, db)
	second.secrets = first.secrets // the OS keyring outlives the daemon
	second.reg = New(Config{
		DB: db, Router: second.router, Tunnels: second.mgr, Secrets: first.secrets, GatewayHost: "8080",
	})
	second.reg.RestoreTunnels(ctx)

	restored := second.reg.ListTunnels()
	if len(restored) != 1 || !restored[0].Config.HasPassword {
		t.Fatalf("restored tunnels: %+v", restored)
	}
	got, err := second.reg.tunnelSecret(view.Config.ID)
	if err != nil || got != testPassword {
		t.Errorf("secret lookup after restart: %q, %v", got, err)
	}
}

func TestTunnelSecretLookupExplainsAMissingSecret(t *testing.T) {
	env := newTunnelEnv(t, nil)
	_, err := env.reg.tunnelSecret("never-stored")
	if err == nil || !strings.Contains(err.Error(), "prot tunnel password") {
		t.Errorf("a missing secret should say how to fix it, got %v", err)
	}

	env.secrets.Err = secrets.ErrUnavailable
	if _, err := env.reg.tunnelSecret("x"); !errors.Is(err, secrets.ErrUnavailable) {
		t.Errorf("a failing keyring should surface as such, got %v", err)
	}
}
