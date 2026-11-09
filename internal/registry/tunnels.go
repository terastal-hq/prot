package registry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/secrets"
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

// ErrPasswordsUnavailable is returned when a password is given but there is nowhere safe to keep it.
var ErrPasswordsUnavailable = errors.New("cannot store a password: no OS keyring (Secret Service) is available")

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
		HasPassword:  cfg.HasPassword,
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
		HasPassword:  t.HasPassword,
	}
}

// tunnelSecret is the tunnel manager's lookup for a stored password, called on every connection attempt.
func (r *Registry) tunnelSecret(tunnelID string) (string, error) {
	if r.secrets == nil {
		return "", ErrPasswordsUnavailable
	}
	secret, err := r.secrets.Get(secrets.TunnelKey(tunnelID))
	if errors.Is(err, secrets.ErrNotFound) {
		return "", errors.New("the stored password is missing from the keyring; set it again with 'prot tunnel password'")
	}
	return secret, err
}

// checkPassword validates a password before it is stored and confirms there is somewhere to store it.
func (r *Registry) checkPassword(password string) error {
	if r.secrets == nil {
		return ErrPasswordsUnavailable
	}
	if password == "" {
		return invalidField("password cannot be empty")
	}
	if strings.ContainsAny(password, "\r\n") {
		return invalidField("password cannot contain a line break")
	}
	return nil
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

// RegisterTunnel registers a tunnel that authenticates with keys or ssh-agent.
func (r *Registry) RegisterTunnel(ctx context.Context, cfg tunnels.TunnelConfig) (*TunnelView, error) {
	return r.RegisterTunnelWithPassword(ctx, cfg, "")
}

// RegisterTunnelWithPassword validates, saves and registers a tunnel, gives it a .localhost route and, when
// auto_connect is set, connects it. A non-empty password (or key passphrase) goes into the OS keyring; nothing
// is saved if it cannot be stored.
func (r *Registry) RegisterTunnelWithPassword(
	ctx context.Context,
	cfg tunnels.TunnelConfig,
	password string,
) (*TunnelView, error) {
	if r.tunnels == nil {
		return nil, ErrTunnelsUnavailable
	}
	cfg.HasPassword = false // set below, from whether a password was really stored

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

	if password != "" {
		if err := r.checkPassword(password); err != nil {
			return nil, err
		}
		if err := r.secrets.Set(secrets.TunnelKey(cfg.ID), password); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPasswordsUnavailable, err)
		}
		cfg.HasPassword = true
	}
	discardSecret := func() {
		if cfg.HasPassword {
			_ = r.secrets.Delete(secrets.TunnelKey(cfg.ID))
		}
	}

	st, err := r.tunnels.Register(cfg)
	if err != nil {
		discardSecret()
		return nil, invalidField("%s", err)
	}
	if err := r.db.SaveTunnel(ctx, tunnelToStorage(st.Config)); err != nil {
		_ = r.tunnels.Remove(cfg.ID)
		discardSecret()
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

// WaitForTunnel waits until a tunnel is connected, or has failed an attempt (so the reason is known), or the
// timeout passes, and returns the last status seen.
func (r *Registry) WaitForTunnel(ctx context.Context, idOrAlias string, timeout time.Duration) (*TunnelView, error) {
	id, err := r.resolveTunnelID(idOrAlias)
	if err != nil {
		return nil, err
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		st, ok := r.tunnels.GetStatus(id)
		if !ok {
			return nil, fmt.Errorf("%w: %q", tunnels.ErrNotFound, id)
		}
		view := r.tunnelView(*st)
		if st.State == tunnels.StateConnected || (st.State == tunnels.StateReconnecting && st.LastError != "") {
			return &view, nil
		}

		select {
		case <-tick.C:
		case <-deadline.C:
			return &view, nil
		case <-ctx.Done():
			return &view, ctx.Err()
		}
	}
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
	if st != nil && st.Config.HasPassword && r.secrets != nil {
		if err := r.secrets.Delete(secrets.TunnelKey(id)); err != nil {
			log.Printf("registry: removing the stored password of tunnel %q from the keyring: %v", id, err)
		}
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

// SetTunnelPassword stores or replaces a tunnel's password (or key passphrase) in the OS keyring. It takes effect
// on the next connection attempt: reconnect the tunnel, or let a retrying one pick it up.
func (r *Registry) SetTunnelPassword(ctx context.Context, idOrAlias, password string) (*TunnelView, error) {
	id, err := r.resolveTunnelID(idOrAlias)
	if err != nil {
		return nil, err
	}
	if err := r.checkPassword(password); err != nil {
		return nil, err
	}
	if err := r.secrets.Set(secrets.TunnelKey(id), password); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPasswordsUnavailable, err)
	}
	return r.setHasPassword(ctx, id, true)
}

// ClearTunnelPassword removes a tunnel's stored password; it goes back to keys and ssh-agent only.
func (r *Registry) ClearTunnelPassword(ctx context.Context, idOrAlias string) (*TunnelView, error) {
	id, err := r.resolveTunnelID(idOrAlias)
	if err != nil {
		return nil, err
	}
	if r.secrets != nil {
		if err := r.secrets.Delete(secrets.TunnelKey(id)); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPasswordsUnavailable, err)
		}
	}
	return r.setHasPassword(ctx, id, false)
}

func (r *Registry) setHasPassword(ctx context.Context, id string, has bool) (*TunnelView, error) {
	if err := r.tunnels.SetHasPassword(id, has); err != nil {
		return nil, err
	}
	st, ok := r.tunnels.GetStatus(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q", tunnels.ErrNotFound, id)
	}
	if err := r.db.SaveTunnel(ctx, tunnelToStorage(st.Config)); err != nil {
		return nil, fmt.Errorf("saving tunnel: %w", err)
	}
	view := r.tunnelView(*st)
	return &view, nil
}

// StopAllTunnels disconnects every tunnel and waits for the ssh processes to exit.
func (r *Registry) StopAllTunnels() {
	if r.tunnels != nil {
		r.tunnels.StopAll()
	}
}
