package registry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// ErrTunnelsUnavailable is returned when SSH tunnels cannot run on this machine (no ssh binary).
var ErrTunnelsUnavailable = errors.New("ssh tunnels are unavailable: no ssh binary was found in PATH")

// TunnelView is a tunnel's live status plus the address it is reachable at.
type TunnelView struct {
	tunnels.TunnelStatus
	CanonicalURL string `json:"canonical_url,omitempty"`
}

func (r *Registry) tunnelView(st tunnels.TunnelStatus) TunnelView {
	view := TunnelView{TunnelStatus: st}
	if st.Config.Alias != "" {
		view.CanonicalURL = r.canonicalURL(st.Config.Alias)
	}
	return view
}

func (r *Registry) canonicalURL(alias string) string {
	if r.gatewayHost != "" {
		return fmt.Sprintf("http://%s.localhost:%s", alias, r.gatewayHost)
	}
	return fmt.Sprintf("http://%s.localhost", alias)
}

func tunnelToStorage(cfg tunnels.TunnelConfig) storage.Tunnel {
	return storage.Tunnel{
		ID:           cfg.ID,
		Alias:        cfg.Alias,
		SSHHost:      cfg.SSHHost,
		SSHUser:      cfg.SSHUser,
		RemoteHost:   cfg.RemoteHost,
		RemotePort:   cfg.RemotePort,
		LocalPort:    cfg.LocalPort,
		IdentityFile: cfg.IdentityFile,
		ExtraArgs:    cfg.ExtraArgs,
		AutoConnect:  cfg.AutoConnect,
	}
}

func tunnelFromStorage(t storage.Tunnel) tunnels.TunnelConfig {
	return tunnels.TunnelConfig{
		ID:           t.ID,
		Alias:        t.Alias,
		SSHHost:      t.SSHHost,
		SSHUser:      t.SSHUser,
		RemoteHost:   t.RemoteHost,
		RemotePort:   t.RemotePort,
		LocalPort:    t.LocalPort,
		IdentityFile: t.IdentityFile,
		ExtraArgs:    t.ExtraArgs,
		AutoConnect:  t.AutoConnect,
	}
}

// handleTunnelState keeps the .localhost route pointed at the tunnel's local end and tells subscribers.
func (r *Registry) handleTunnelState(st tunnels.TunnelStatus) {
	if st.State == tunnels.StateConnected && st.Config.Alias != "" && st.AssignedLocalPort > 0 {
		// Each connection may get a fresh local port (when none was configured), so follow it. When the tunnel
		// drops the route stays: the gateway then shows its "upstream not responding" page instead of a 404.
		if err := r.router.SetRoute(st.Config.Alias, netutil.LoopbackURL(st.AssignedLocalPort), false); err != nil {
			log.Printf("registry: routing %q to tunnel port %d: %v", st.Config.Alias, st.AssignedLocalPort, err)
		}
	}
	r.Broadcast(Event{Type: "tunnel.status", Timestamp: time.Now().UTC(), Data: r.tunnelView(st)})
}

// aliasInUseByTunnel reports whether a tunnel already owns the alias.
func (r *Registry) aliasInUseByTunnel(alias, exceptTunnelID string) bool {
	if r.tunnels == nil {
		return false
	}
	for _, st := range r.tunnels.List() {
		if st.Config.ID != exceptTunnelID && st.Config.Alias == alias {
			return true
		}
	}
	return false
}

// TunnelsAvailable reports whether SSH tunnels can run here.
func (r *Registry) TunnelsAvailable() bool { return r.tunnels != nil }

// RegisterTunnel validates, saves and registers a tunnel, gives it a .localhost route and, when auto_connect is
// set, connects it.
func (r *Registry) RegisterTunnel(ctx context.Context, cfg tunnels.TunnelConfig) (*TunnelView, error) {
	if r.tunnels == nil {
		return nil, ErrTunnelsUnavailable
	}

	cfg.Alias = strings.ToLower(strings.TrimSpace(cfg.Alias))
	if !aliasPattern.MatchString(cfg.Alias) {
		return nil, ErrInvalidAlias
	}
	if cfg.ID == "" {
		cfg.ID = fmt.Sprintf("tun_%d", time.Now().UnixNano())
	}

	r.tunnelRegMu.Lock()
	defer r.tunnelRegMu.Unlock()

	if r.aliasInUseByTunnel(cfg.Alias, "") {
		return nil, ErrAliasTaken
	}
	services, err := r.db.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	for _, svc := range services {
		if svc.Alias == cfg.Alias {
			return nil, ErrAliasTaken
		}
	}

	st, err := r.tunnels.Register(cfg)
	if err != nil {
		return nil, invalidField("%s", err)
	}
	if err := r.db.SaveTunnel(ctx, tunnelToStorage(st.Config)); err != nil {
		_ = r.tunnels.Remove(cfg.ID)
		return nil, fmt.Errorf("saving tunnel: %w", err)
	}

	if st.Config.LocalPort > 0 {
		if err := r.router.SetRoute(st.Config.Alias, netutil.LoopbackURL(st.Config.LocalPort), false); err != nil {
			log.Printf("registry: routing %q: %v", st.Config.Alias, err)
		}
	}
	_ = r.db.LogEvent(ctx, "tunnel.registered", cfg.ID, fmt.Sprintf(`{"alias": %q}`, cfg.Alias))

	if st.Config.AutoConnect {
		if started, err := r.tunnels.Start(ctx, cfg.ID); err == nil {
			st = started
		}
	}
	view := r.tunnelView(*st)
	r.Broadcast(Event{Type: "tunnel.registered", Timestamp: time.Now().UTC(), Data: view})
	return &view, nil
}

// ListTunnels returns every tunnel with its live status.
func (r *Registry) ListTunnels() []TunnelView {
	views := []TunnelView{}
	if r.tunnels == nil {
		return views
	}
	for _, st := range r.tunnels.List() {
		views = append(views, r.tunnelView(st))
	}
	return views
}

// resolveTunnelID finds a tunnel by id or alias.
func (r *Registry) resolveTunnelID(idOrAlias string) (string, error) {
	if r.tunnels == nil {
		return "", ErrTunnelsUnavailable
	}
	all := r.tunnels.List()
	for _, st := range all {
		if st.Config.ID == idOrAlias {
			return st.Config.ID, nil
		}
	}
	for _, st := range all {
		if idOrAlias != "" && st.Config.Alias == idOrAlias {
			return st.Config.ID, nil
		}
	}
	return "", fmt.Errorf("%w: %q", tunnels.ErrNotFound, idOrAlias)
}

// StartTunnel connects a tunnel.
func (r *Registry) StartTunnel(ctx context.Context, idOrAlias string) (*TunnelView, error) {
	id, err := r.resolveTunnelID(idOrAlias)
	if err != nil {
		return nil, err
	}
	st, err := r.tunnels.Start(ctx, id)
	if err != nil {
		return nil, err
	}
	view := r.tunnelView(*st)
	return &view, nil
}

// StopTunnel disconnects a tunnel and waits for its ssh process to exit.
func (r *Registry) StopTunnel(idOrAlias string) (*TunnelView, error) {
	id, err := r.resolveTunnelID(idOrAlias)
	if err != nil {
		return nil, err
	}
	if err := r.tunnels.Stop(id); err != nil {
		return nil, err
	}
	st, ok := r.tunnels.GetStatus(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q", tunnels.ErrNotFound, id)
	}
	view := r.tunnelView(*st)
	return &view, nil
}

// DeleteTunnel disconnects a tunnel, removes its route and forgets it for good.
func (r *Registry) DeleteTunnel(ctx context.Context, idOrAlias string) error {
	id, err := r.resolveTunnelID(idOrAlias)
	if err != nil {
		return err
	}
	st, _ := r.tunnels.GetStatus(id)

	if err := r.tunnels.Remove(id); err != nil {
		return err
	}
	if st != nil && st.Config.Alias != "" {
		r.router.RemoveRoute(st.Config.Alias)
	}
	if err := r.db.DeleteTunnel(ctx, id); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("deleting tunnel: %w", err)
	}
	_ = r.db.LogEvent(ctx, "tunnel.deleted", id, "")
	r.Broadcast(Event{Type: "tunnel.deleted", Timestamp: time.Now().UTC(), Data: map[string]string{"id": id}})
	return nil
}

// RestoreTunnels loads saved tunnels at startup, restores their routes and reconnects the auto_connect ones. A
// tunnel that fails to restore is logged and skipped so the others still come up.
func (r *Registry) RestoreTunnels(ctx context.Context) {
	saved, err := r.db.ListTunnels(ctx)
	if err != nil {
		log.Printf("registry: loading saved tunnels: %v", err)
		return
	}
	if len(saved) == 0 {
		return
	}
	if r.tunnels == nil {
		log.Printf("registry: %d saved tunnel(s) not restored: %v", len(saved), ErrTunnelsUnavailable)
		return
	}

	for _, t := range saved {
		cfg := tunnelFromStorage(t)
		if _, err := r.tunnels.Register(cfg); err != nil {
			log.Printf("registry: restoring tunnel %q: %v", t.Alias, err)
			continue
		}
		if cfg.LocalPort > 0 && cfg.Alias != "" {
			if err := r.router.SetRoute(cfg.Alias, netutil.LoopbackURL(cfg.LocalPort), false); err != nil {
				log.Printf("registry: routing %q: %v", cfg.Alias, err)
			}
		}
		if cfg.AutoConnect {
			if _, err := r.tunnels.Start(ctx, cfg.ID); err != nil {
				log.Printf("registry: reconnecting tunnel %q: %v", cfg.Alias, err)
			}
		}
	}
}

// StopAllTunnels disconnects every tunnel and waits for the ssh processes to exit.
func (r *Registry) StopAllTunnels() {
	if r.tunnels != nil {
		r.tunnels.StopAll()
	}
}
