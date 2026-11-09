package registry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/secrets"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/supervisor"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// ServiceView combines persistent configuration with live runtime status.
type ServiceView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Runtime       string            `json:"runtime"` // "managed", "discovered", "remote"
	Status        string            `json:"status"`  // one of the storage.Status* constants
	Alias         string            `json:"alias"`
	CanonicalURL  string            `json:"canonical_url,omitempty"`
	DirectURL     string            `json:"direct_url,omitempty"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Port          uint32            `json:"port"`
	AutoPort      bool              `json:"auto_port"`
	AutoStart     bool              `json:"auto_start"`
	RestartPolicy string            `json:"restart_policy"`
	LANExposed    bool              `json:"lan_exposed"`
	PID           int               `json:"pid,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Framework     string            `json:"framework,omitempty"`
}

// Event represents a broadcast event for subscribers.
type Event struct {
	Type      string    `json:"type"`
	ServiceID string    `json:"service_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// Registry is the unified domain authority for services, routes, and tunnels.
type Registry struct {
	db         *storage.DB
	supervisor *supervisor.Supervisor
	router     *proxy.Router
	tunnels    *tunnels.Manager
	secrets    secrets.Store

	mu          sync.RWMutex
	discovered  map[string]discovery.ProcessService
	gatewayHost string

	tunnelRegMu sync.Mutex // serialises tunnel registration so two requests cannot claim one alias

	subMu       sync.RWMutex
	subscribers map[chan Event]struct{}
}

// Config configures the Registry.
type Config struct {
	DB          *storage.DB
	Router      *proxy.Router
	Tunnels     *tunnels.Manager
	Secrets     secrets.Store // keeps tunnel passwords; nil disables them
	GatewayHost string        // e.g. "localhost:8080"
}

// New constructs a Registry.
func New(cfg Config) *Registry {
	r := &Registry{
		db:          cfg.DB,
		router:      cfg.Router,
		tunnels:     cfg.Tunnels,
		secrets:     cfg.Secrets,
		gatewayHost: cfg.GatewayHost,
		discovered:  make(map[string]discovery.ProcessService),
		subscribers: make(map[chan Event]struct{}),
	}

	r.supervisor = supervisor.New(func(serviceID, status string, port uint32) {
		r.handleSupervisorStatus(serviceID, status, port)
	})
	if r.tunnels != nil {
		r.tunnels.SetObserver(r.handleTunnelState)
		r.tunnels.SetSecretProvider(r.tunnelSecret)
	}

	return r
}

func (r *Registry) handleSupervisorStatus(serviceID, status string, port uint32) {
	// Called from supervisor goroutines, so there is no request context to inherit.
	ctx := context.Background()
	if err := r.db.UpdateServiceStatus(ctx, serviceID, status); err != nil {
		log.Printf("registry: recording status %q for %q: %v", status, serviceID, err)
	}

	svc, err := r.db.GetService(ctx, serviceID)
	if err != nil {
		log.Printf("registry: loading %q after status change: %v", serviceID, err)
	}
	if err == nil && svc.Alias != "" && status == storage.StatusRunning {
		targetPort := port
		if targetPort == 0 {
			targetPort = svc.Port
		}
		if targetPort > 0 {
			if err := r.router.SetRoute(svc.Alias, netutil.LoopbackURL(targetPort), svc.LANExposed); err != nil {
				log.Printf("registry: routing %q to port %d: %v", svc.Alias, targetPort, err)
			}
		}
	}

	r.Broadcast(Event{
		Type:      "service.status",
		ServiceID: serviceID,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"status": status,
			"port":   port,
		},
	})
}

// SetDiscovered updates the in-memory cache of discovered processes.
func (r *Registry) SetDiscovered(services []discovery.ProcessService) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.discovered = make(map[string]discovery.ProcessService, len(services))
	for _, s := range services {
		r.discovered[s.ID] = s
	}

	r.Broadcast(Event{
		Type:      "discovery.updated",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"count": len(services),
		},
	})
}

// Broadcast sends an event to all subscribers.
func (r *Registry) Broadcast(ev Event) {
	r.subMu.RLock()
	defer r.subMu.RUnlock()

	for ch := range r.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe returns an event subscription channel and an unsubscribe func.
func (r *Registry) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 50)
	r.subMu.Lock()
	r.subscribers[ch] = struct{}{}
	r.subMu.Unlock()

	unsubscribe := func() {
		r.subMu.Lock()
		delete(r.subscribers, ch)
		close(ch)
		r.subMu.Unlock()
	}

	return ch, unsubscribe
}

// ListServices returns all managed services merged with live runtime status and discovered services.
func (r *Registry) ListServices(ctx context.Context) ([]ServiceView, error) {
	saved, err := r.db.ListServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing services from db: %w", err)
	}

	views := make([]ServiceView, 0, len(saved))
	for _, s := range saved {
		view := r.toServiceView(s)
		views = append(views, view)
	}

	return views, nil
}

// GetService fetches a service by ID or alias.
func (r *Registry) GetService(ctx context.Context, idOrAlias string) (*ServiceView, error) {
	svc, err := r.db.ResolveService(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}
	view := r.toServiceView(*svc)
	return &view, nil
}

func (r *Registry) toServiceView(s storage.Service) ServiceView {
	view := ServiceView{
		ID:            s.ID,
		Name:          s.Name,
		Runtime:       s.Runtime,
		Status:        s.Status,
		Alias:         s.Alias,
		Command:       s.Command,
		Args:          s.Args,
		Cwd:           s.Cwd,
		Env:           s.Env,
		Port:          s.Port,
		AutoPort:      s.AutoPort,
		AutoStart:     s.AutoStart,
		RestartPolicy: s.RestartPolicy,
		LANExposed:    s.LANExposed,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}

	// Enrich with live supervisor state if managed
	if ps, ok := r.supervisor.GetState(s.ID); ok {
		view.Status = ps.Status
		view.PID = ps.PID
		if ps.AssignedPort > 0 {
			view.Port = ps.AssignedPort
		}
	}

	if view.Port > 0 {
		view.DirectURL = netutil.LoopbackURL(view.Port)
	}

	if view.Alias != "" {
		view.CanonicalURL = r.canonicalURL(view.Alias)
	}

	return view
}

// RegisterService saves and initializes a new service.
func (r *Registry) RegisterService(ctx context.Context, svc storage.Service) (*ServiceView, error) {
	if svc.ID == "" {
		svc.ID = fmt.Sprintf("svc_%d", time.Now().UnixNano())
	}
	if svc.Runtime == "" {
		svc.Runtime = "managed"
	}
	if svc.Status == "" {
		svc.Status = storage.StatusStopped
	}

	if err := r.db.SaveService(ctx, svc); err != nil {
		return nil, fmt.Errorf("saving service: %w", err)
	}

	if svc.Alias != "" && svc.Port > 0 {
		targetURL := netutil.LoopbackURL(svc.Port)
		_ = r.router.SetRoute(svc.Alias, targetURL, svc.LANExposed)
	}

	_ = r.db.LogEvent(ctx, "service.registered", svc.ID, fmt.Sprintf(`{"name": %q, "alias": %q}`, svc.Name, svc.Alias))

	view := r.toServiceView(svc)
	r.Broadcast(Event{
		Type:      "service.registered",
		ServiceID: svc.ID,
		Timestamp: time.Now().UTC(),
		Data:      view,
	})

	return &view, nil
}

// Validation errors returned by UpdateService.
var (
	// ErrInvalidAlias is returned when an alias is not a valid DNS label.
	ErrInvalidAlias = errors.New("alias must be 1-63 characters: lowercase letters, digits and hyphens, " +
		"not starting or ending with a hyphen")
	// ErrAliasTaken is returned when another service or tunnel already uses the alias.
	ErrAliasTaken = errors.New("alias is already used by another service or tunnel")
	// ErrInvalidField is returned for any other rejected field value.
	ErrInvalidField = errors.New("invalid field")
)

var aliasPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ServicePatch holds optional changes to a service; nil fields are left untouched.
type ServicePatch struct {
	Name          *string            `json:"name"`
	Alias         *string            `json:"alias"`
	Command       *string            `json:"command"`
	Cwd           *string            `json:"cwd"`
	Env           *map[string]string `json:"env"`
	Port          *uint32            `json:"port"`
	AutoPort      *bool              `json:"auto_port"`
	AutoStart     *bool              `json:"auto_start"`
	RestartPolicy *string            `json:"restart_policy"`
	LANExposed    *bool              `json:"lan_exposed"`
}

func invalidField(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidField, fmt.Sprintf(format, args...))
}

// applyPatch validates patch and applies it to svc in place.
func applyPatch(svc *storage.Service, patch ServicePatch, others []storage.Service) error {
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return invalidField("name cannot be empty")
		}
		svc.Name = name
	}
	if patch.Alias != nil {
		alias := strings.ToLower(strings.TrimSpace(*patch.Alias))
		if !aliasPattern.MatchString(alias) {
			return ErrInvalidAlias
		}
		for _, o := range others {
			if o.ID != svc.ID && o.Alias == alias {
				return ErrAliasTaken
			}
		}
		svc.Alias = alias
	}
	if patch.Command != nil {
		cmd := strings.TrimSpace(*patch.Command)
		if cmd == "" && svc.Command != "" {
			return invalidField("command cannot be empty")
		}
		svc.Command = cmd
	}
	if patch.Cwd != nil {
		svc.Cwd = strings.TrimSpace(*patch.Cwd)
	}
	if patch.Env != nil {
		for k := range *patch.Env {
			if k == "" || strings.ContainsAny(k, "= \t\n") {
				return invalidField("invalid environment variable name %q", k)
			}
		}
		svc.Env = *patch.Env
	}
	if patch.Port != nil {
		if *patch.Port > 65535 {
			return invalidField("port must be between 0 and 65535")
		}
		svc.Port = *patch.Port
	}
	if patch.RestartPolicy != nil {
		switch *patch.RestartPolicy {
		case storage.RestartNever, storage.RestartOnFailure, storage.RestartAlways:
			svc.RestartPolicy = *patch.RestartPolicy
		default:
			return invalidField("restart_policy must be no, on-failure or always")
		}
	}
	if patch.AutoPort != nil {
		svc.AutoPort = *patch.AutoPort
	}
	if patch.AutoStart != nil {
		svc.AutoStart = *patch.AutoStart
	}
	if patch.LANExposed != nil {
		svc.LANExposed = *patch.LANExposed
	}
	return nil
}

// UpdateService validates and applies a partial update, keeps the live route in sync and broadcasts the result.
// Changes to the command, port, working directory, environment and restart policy take effect on the next start.
func (r *Registry) UpdateService(ctx context.Context, idOrAlias string, patch ServicePatch) (*ServiceView, error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}
	svc, err := r.db.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	all, err := r.db.ListServices(ctx)
	if err != nil {
		return nil, err
	}

	oldAlias := svc.Alias
	if err := applyPatch(svc, patch, all); err != nil {
		return nil, err
	}
	if patch.Alias != nil && r.aliasInUseByTunnel(svc.Alias, "") {
		return nil, ErrAliasTaken
	}
	if err := r.db.SaveService(ctx, *svc); err != nil {
		return nil, fmt.Errorf("saving service: %w", err)
	}

	r.syncRoute(oldAlias, svc)
	_ = r.db.LogEvent(ctx, "service.updated", id, "{}")

	view := r.toServiceView(*svc)
	r.Broadcast(Event{
		Type:      "service.updated",
		ServiceID: id,
		Timestamp: time.Now().UTC(),
		Data:      view,
	})
	return &view, nil
}

// syncRoute moves an existing route to the service's current alias and LAN setting. A stopped service
// also follows its configured port; a live one keeps its target until the next restart.
func (r *Registry) syncRoute(oldAlias string, svc *storage.Service) {
	rt, ok := r.router.GetRoute(oldAlias)
	if !ok {
		return
	}
	target := rt.TargetURL
	if _, running := r.supervisor.GetState(svc.ID); !running && svc.Port > 0 {
		target = netutil.LoopbackURL(svc.Port)
	}
	if oldAlias != svc.Alias {
		r.router.RemoveRoute(oldAlias)
	}
	_ = r.router.SetRoute(svc.Alias, target, svc.LANExposed)
}

// RenameAlias changes only the .localhost alias of a service.
func (r *Registry) RenameAlias(ctx context.Context, idOrAlias, newAlias string) (*ServiceView, error) {
	return r.UpdateService(ctx, idOrAlias, ServicePatch{Alias: &newAlias})
}

func (r *Registry) resolveServiceID(ctx context.Context, idOrAlias string) (string, error) {
	svc, err := r.db.ResolveService(ctx, idOrAlias)
	if err != nil {
		return "", err
	}
	return svc.ID, nil
}

// DeleteService stops and deletes a service.
func (r *Registry) DeleteService(ctx context.Context, idOrAlias string) error {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return err
	}

	svc, err := r.db.GetService(ctx, id)
	if err != nil {
		return err
	}

	// Stop running process if any
	_ = r.supervisor.Stop(id, 2*time.Second)

	if svc.Alias != "" {
		r.router.RemoveRoute(svc.Alias)
	}

	if err := r.db.DeleteService(ctx, id); err != nil {
		return err
	}

	_ = r.db.LogEvent(ctx, "service.deleted", id, "")

	r.Broadcast(Event{
		Type:      "service.deleted",
		ServiceID: id,
		Timestamp: time.Now().UTC(),
	})

	return nil
}

// StartService starts a registered managed service.
func (r *Registry) StartService(ctx context.Context, idOrAlias string) (*ServiceView, error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}

	svc, err := r.db.GetService(ctx, id)
	if err != nil {
		return nil, err
	}

	ps, err := r.supervisor.Start(ctx, *svc)
	if err != nil {
		return nil, fmt.Errorf("starting process: %w", err)
	}

	svc.Status = ps.Status
	_ = r.db.UpdateServiceStatus(ctx, id, ps.Status)

	_ = r.db.LogEvent(ctx, "service.started", id, fmt.Sprintf(`{"pid": %d}`, ps.PID))

	view := r.toServiceView(*svc)
	return &view, nil
}

// StopService gracefully stops a running service.
func (r *Registry) StopService(ctx context.Context, idOrAlias string) (*ServiceView, error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}

	svc, err := r.db.GetService(ctx, id)
	if err != nil {
		return nil, err
	}

	// Stopping is idempotent: a service the supervisor has no process for (never started since the daemon
	// booted, or already cleaned up) is simply already stopped.
	if err := r.supervisor.Stop(id, 3*time.Second); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("stopping process: %w", err)
	}

	_ = r.db.UpdateServiceStatus(ctx, id, storage.StatusStopped)
	_ = r.db.LogEvent(ctx, "service.stopped", id, "")

	svc.Status = storage.StatusStopped
	view := r.toServiceView(*svc)
	return &view, nil
}

// RestartService restarts a managed service.
func (r *Registry) RestartService(ctx context.Context, idOrAlias string) (*ServiceView, error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}

	svc, err := r.db.GetService(ctx, id)
	if err != nil {
		return nil, err
	}

	ps, err := r.supervisor.Restart(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("restarting process: %w", err)
	}

	svc.Status = ps.Status
	_ = r.db.UpdateServiceStatus(ctx, id, ps.Status)
	_ = r.db.LogEvent(ctx, "service.restarted", id, fmt.Sprintf(`{"pid": %d}`, ps.PID))

	view := r.toServiceView(*svc)
	return &view, nil
}

// AdoptOptions specifies parameters for adopting a discovered service.
type AdoptOptions struct {
	DiscoveryID string
	Name        string
	Alias       string
	Command     string
}

// AdoptDiscovered converts an observed discovery item into a managed service.
func (r *Registry) AdoptDiscovered(ctx context.Context, opts AdoptOptions) (*ServiceView, error) {
	r.mu.RLock()
	disc, ok := r.discovered[opts.DiscoveryID]
	r.mu.RUnlock()
	if !ok {
		return nil, errors.New("discovered service not found or expired")
	}

	name := opts.Name
	if name == "" {
		name = disc.Name
		if name == "" {
			name = fmt.Sprintf("Service on :%d", disc.Port)
		}
	}

	alias := opts.Alias
	if alias == "" {
		alias = disc.Name
	}

	svc := storage.Service{
		ID:            fmt.Sprintf("svc_%s_%d", disc.Fingerprint[:8], disc.Port),
		Name:          name,
		Runtime:       "managed",
		Status:        storage.StatusRunning,
		Alias:         alias,
		Command:       opts.Command,
		Cwd:           disc.Cwd,
		Port:          disc.Port,
		AutoPort:      false,
		RestartPolicy: storage.RestartNever,
	}

	return r.RegisterService(ctx, svc)
}

// GetLogs returns captured logs for a service.
func (r *Registry) GetLogs(ctx context.Context, idOrAlias string, limit int) ([]supervisor.LogEntry, error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}
	return r.supervisor.GetLogs(id, limit)
}

// FollowLogs returns the last limit log lines and a live channel for the rest. Each line is delivered exactly
// once, either in the history or on the channel.
func (r *Registry) FollowLogs(
	ctx context.Context,
	idOrAlias string,
	limit int,
) ([]supervisor.LogEntry, <-chan supervisor.LogEntry, func(), error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, nil, nil, err
	}
	return r.supervisor.FollowLogsWithHistory(id, limit)
}

// StopAll terminates all running managed processes.
func (r *Registry) StopAll(timeout time.Duration) {
	r.supervisor.StopAll(timeout)
}

// ListDiscovered returns all currently observed local TCP processes.
func (r *Registry) ListDiscovered() []discovery.ProcessService {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := make([]discovery.ProcessService, 0, len(r.discovered))
	for _, s := range r.discovered {
		res = append(res, s)
	}
	return res
}

// Supervisor exposes the process supervisor for inspection in tests and diagnostics.
func (r *Registry) Supervisor() *supervisor.Supervisor { return r.supervisor }
