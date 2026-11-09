package registry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/secrets"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

func str(s string) *string       { return &s }
func num(n uint32) *uint32       { return &n }
func flag(b bool) *bool          { return &b }
func args(a ...string) *[]string { return &a }

func tunnelPID(t *testing.T, env *tunnelEnv, id string) int {
	t.Helper()
	st, ok := env.mgr.GetStatus(id)
	if !ok {
		t.Fatalf("tunnel %s is gone", id)
	}
	return st.PID
}

func TestUpdateTunnelEditsAStoppedTunnelAndPersistsIt(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	view, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "db", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Config.ID

	got, err := env.reg.UpdateTunnel(ctx, "db", TunnelPatch{
		SSHHost: str("bastion.example.com"), SSHUser: str("deploy"), RemotePort: num(6543),
		IdentityFile: str("/keys/id"), ExtraArgs: args("-p", "2222"), AutoConnect: flag(true),
	})
	if err != nil {
		t.Fatalf("UpdateTunnel: %v", err)
	}
	c := got.Config
	if c.ID != id || c.SSHHost != "bastion.example.com" || c.SSHUser != "deploy" || c.RemotePort != 6543 ||
		c.IdentityFile != "/keys/id" || !reflect.DeepEqual(c.ExtraArgs, []string{"-p", "2222"}) || !c.AutoConnect {
		t.Errorf("config after edit: %+v", c)
	}
	if got.State != tunnels.StateDisconnected {
		t.Errorf("a stopped tunnel must stay stopped, is %s", got.State)
	}
	if c.RemoteHost != "127.0.0.1" || c.Alias != "db" {
		t.Errorf("untouched fields changed: %+v", c)
	}

	saved, err := env.db.ListTunnels(ctx)
	if err != nil || len(saved) != 1 || saved[0].SSHHost != "bastion.example.com" || saved[0].RemotePort != 6543 {
		t.Errorf("edit not persisted: %+v, %v", saved, err)
	}
}

func TestUpdateTunnelCanClearOptionalFields(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	cfg := tunnelCfg(t, "db", false)
	cfg.SSHUser, cfg.IdentityFile, cfg.ExtraArgs = "ops", "/keys/id", []string{"-p", "22"}
	if _, err := env.reg.RegisterTunnel(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	got, err := env.reg.UpdateTunnel(ctx, "db", TunnelPatch{SSHUser: str(""), IdentityFile: str(""), ExtraArgs: args()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.SSHUser != "" || got.Config.IdentityFile != "" || len(got.Config.ExtraArgs) != 0 {
		t.Errorf("fields were not cleared: %+v", got.Config)
	}
}

func TestUpdateTunnelRenameMovesTheRoute(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	cfg := tunnelCfg(t, "old", false)
	if _, err := env.reg.RegisterTunnel(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	got, err := env.reg.UpdateTunnel(ctx, "old", TunnelPatch{Alias: str("  New-Name ")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Alias != "new-name" || got.CanonicalURL != "http://new-name.localhost:8080" {
		t.Errorf("rename: %+v %q", got.Config, got.CanonicalURL)
	}
	if _, ok := env.router.GetRoute("old"); ok {
		t.Error("the old alias still routes")
	}
	rt, ok := env.router.GetRoute("new-name")
	if !ok || rt.TargetURL != netutil.LoopbackURL(cfg.LocalPort) {
		t.Errorf("new route = %+v (ok=%v)", rt, ok)
	}
	if _, err := env.reg.StartTunnel(ctx, "old"); err == nil {
		t.Error("the old alias must no longer resolve to the tunnel")
	}
}

func TestUpdateTunnelRefusesBadEditsAndChangesNothing(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "db", false)); err != nil {
		t.Fatal(err)
	}
	other, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "other", false))
	if err != nil {
		t.Fatal(err)
	}
	web := storage.Service{ID: "svc-web", Name: "Web", Alias: "web", Port: 3000}
	if _, err := env.reg.RegisterService(ctx, web); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		patch TunnelPatch
		want  error
	}{
		{"alias of another tunnel", TunnelPatch{Alias: str("other")}, ErrAliasTaken},
		{"alias of a service", TunnelPatch{Alias: str("web")}, ErrAliasTaken},
		{"invalid alias", TunnelPatch{Alias: str("Not Valid!")}, ErrInvalidAlias},
		{"option-like host", TunnelPatch{SSHHost: str("-oProxyCommand=evil")}, ErrInvalidField},
		{"empty host", TunnelPatch{SSHHost: str("")}, ErrInvalidField},
		{"no remote port", TunnelPatch{RemotePort: num(0)}, ErrInvalidField},
	}
	for _, c := range cases {
		if _, err := env.reg.UpdateTunnel(ctx, "db", c.patch); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}

	for _, v := range env.reg.ListTunnels() {
		if v.Config.ID == other.Config.ID {
			continue
		}
		if v.Config.Alias != "db" || v.Config.SSHHost != "fake.invalid" || v.Config.RemotePort != 5432 {
			t.Errorf("a refused edit changed the tunnel: %+v", v.Config)
		}
	}
	if _, ok := env.router.GetRoute("db"); !ok {
		t.Error("a refused edit removed the route")
	}
	if _, err := env.reg.UpdateTunnel(ctx, "nope", TunnelPatch{}); !errors.Is(err, tunnels.ErrNotFound) {
		t.Errorf("unknown tunnel: %v", err)
	}
}

// A live tunnel is reconnected so the new settings take effect; one that is edited without change is left alone.
func TestUpdateTunnelReconnectsALiveTunnel(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	cfg := tunnelCfg(t, "live", true)
	view, err := env.reg.RegisterTunnel(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Config.ID
	waitTunnel(t, env, id, tunnels.StateConnected)
	before := tunnelPID(t, env, id)

	if _, err := env.reg.UpdateTunnel(ctx, "live", TunnelPatch{RemotePort: num(5432)}); err != nil {
		t.Fatal(err)
	}
	if after := tunnelPID(t, env, id); after != before {
		t.Errorf("an edit that changes nothing restarted ssh (%d -> %d)", before, after)
	}

	newPort, err := netutil.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	edit := TunnelPatch{LocalPort: num(uint32(newPort)), RemotePort: num(9999)}
	if _, err := env.reg.UpdateTunnel(ctx, "live", edit); err != nil {
		t.Fatal(err)
	}
	connected := waitTunnel(t, env, id, tunnels.StateConnected)
	if connected.PID == before || connected.PID == 0 {
		t.Errorf("tunnel was not restarted: pid %d -> %d", before, connected.PID)
	}
	if connected.Config.RemotePort != 9999 || connected.AssignedLocalPort != uint32(newPort) {
		t.Errorf("new settings not in effect: %+v", connected.TunnelStatus)
	}
	rt, ok := env.router.GetRoute("live")
	if !ok || rt.TargetURL != netutil.LoopbackURL(newPort) {
		t.Errorf("route = %+v (ok=%v), want port %d", rt, ok, newPort)
	}
}

// A refused edit must not drop a working connection.
func TestUpdateTunnelRefusedEditKeepsTheConnection(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	view, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "live", true))
	if err != nil {
		t.Fatal(err)
	}
	waitTunnel(t, env, view.Config.ID, tunnels.StateConnected)
	before := tunnelPID(t, env, view.Config.ID)

	if _, err := env.reg.UpdateTunnel(ctx, "live", TunnelPatch{SSHHost: str("-oProxyCommand=evil")}); err == nil {
		t.Fatal("expected the edit to be refused")
	}
	st, _ := env.mgr.GetStatus(view.Config.ID)
	if st.State != tunnels.StateConnected || st.PID != before {
		t.Errorf("connection was disturbed: %s pid %d (was %d)", st.State, st.PID, before)
	}
}

func TestUpdateTunnelKeepsTheStoredPasswordAndSurvivesRestart(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	first := newTunnelEnv(t, db)
	view, err := first.reg.RegisterTunnelWithPassword(ctx, tunnelCfg(t, "vault", false), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	id := view.Config.ID
	rename := TunnelPatch{Alias: str("renamed"), RemotePort: num(8200)}
	if _, err := first.reg.UpdateTunnel(ctx, "vault", rename); err != nil {
		t.Fatal(err)
	}
	got, err := first.secrets.Get(secrets.TunnelKey(id))
	if err != nil || got != "s3cret" {
		t.Errorf("the password must follow the tunnel's id through a rename: %q, %v", got, err)
	}
	first.reg.StopAllTunnels()

	second := newTunnelEnv(t, db)
	second.reg.RestoreTunnels(ctx)
	list := second.reg.ListTunnels()
	if len(list) != 1 {
		t.Fatalf("after restart: %+v", list)
	}
	if c := list[0].Config; c.Alias != "renamed" || c.RemotePort != 8200 || !c.HasPassword {
		t.Errorf("after restart: %+v", list)
	}
}

func TestUpdateTunnelBroadcastsAnEvent(t *testing.T) {
	env := newTunnelEnv(t, nil)
	ctx := context.Background()
	if _, err := env.reg.RegisterTunnel(ctx, tunnelCfg(t, "db", false)); err != nil {
		t.Fatal(err)
	}
	events, cancel := env.reg.Subscribe()
	defer cancel()

	if _, err := env.reg.UpdateTunnel(ctx, "db", TunnelPatch{RemotePort: num(1234)}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev := <-events:
			if ev.Type == "tunnel.updated" {
				return
			}
		default:
			t.Fatal("no tunnel.updated event was broadcast")
		}
	}
}
