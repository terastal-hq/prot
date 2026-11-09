package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/proxy"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/supervisor"
	"github.com/terastal-hq/prot/internal/tunnels"
)

// ServiceView combines persistent configuration with live runtime status.
type ServiceView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Runtime       string            `json:"runtime"` // "managed", "discovered", "remote"
	Status        string            `json:"status"`  // "stopped", "starting", "running", "unhealthy", "crashed"
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
	Type      string      `json:"type"`
	ServiceID string      `json:"service_id,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// Registry is the unified domain authority for services, routes, and tunnels.
type Registry struct {
	db         *storage.DB
	supervisor *supervisor.Supervisor
	router     *proxy.Router
	tunnels    *tunnels.Manager

	mu          sync.RWMutex
	discovered  map[string]discovery.ProcessService
	gatewayHost string

	subMu       sync.RWMutex
	subscribers map[chan Event]struct{}
}

// Config configures the Registry.
type Config struct {
	DB          *storage.DB
	Router      *proxy.Router
	Tunnels     *tunnels.Manager
	GatewayHost string // e.g. "localhost:8080"
}

// New constructs a Registry.
func New(cfg Config) *Registry {
	r := &Registry{
		db:          cfg.DB,
		router:      cfg.Router,
		tunnels:     cfg.Tunnels,
		gatewayHost: cfg.GatewayHost,
		discovered:  make(map[string]discovery.ProcessService),
		subscribers: make(map[chan Event]struct{}),
	}

	r.supervisor = supervisor.New(func(serviceID, status string, port uint32) {
		r.handleSupervisorStatus(serviceID, status, port)
	})

	return r
}

func (r *Registry) handleSupervisorStatus(serviceID, status string, port uint32) {
	ctx := context.Background()
	_ = r.db.UpdateServiceStatus(ctx, serviceID, status)

	svc, err := r.db.GetService(ctx, serviceID)
	if err == nil && svc.Alias != "" {
		if status == "running" {
			targetPort := port
			if targetPort == 0 {
				targetPort = svc.Port
			}
			if targetPort > 0 {
				targetURL := fmt.Sprintf("http://127.0.0.1:%d", targetPort)
				_ = r.router.SetRoute(svc.Alias, targetURL, svc.LANExposed)
			}
		}
	}

	r.Broadcast(Event{
		Type:      "service.status",
		ServiceID: serviceID,
		Timestamp: time.Now().UTC(),
		Data: map[string]interface{}{
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
		Data: map[string]interface{}{
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
	// Try by ID first
	svc, err := r.db.GetService(ctx, idOrAlias)
	if err == nil {
		v := r.toServiceView(*svc)
		return &v, nil
	}

	// Try by Alias
	all, err := r.db.ListServices(ctx)
	if err == nil {
		for _, s := range all {
			if s.Alias == idOrAlias {
				v := r.toServiceView(s)
				return &v, nil
			}
		}
	}

	return nil, storage.ErrNotFound
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
		view.DirectURL = fmt.Sprintf("http://127.0.0.1:%d", view.Port)
	}

	if view.Alias != "" {
		if r.gatewayHost != "" {
			view.CanonicalURL = fmt.Sprintf("http://%s.localhost:%s", view.Alias, r.gatewayHost)
		} else {
			view.CanonicalURL = fmt.Sprintf("http://%s.localhost", view.Alias)
		}
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
		svc.Status = "stopped"
	}

	if err := r.db.SaveService(ctx, svc); err != nil {
		return nil, fmt.Errorf("saving service: %w", err)
	}

	if svc.Alias != "" && svc.Port > 0 {
		targetURL := fmt.Sprintf("http://127.0.0.1:%d", svc.Port)
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

// ErrInvalidAlias is returned when an alias is not a valid DNS label.
var ErrInvalidAlias = errors.New("alias must be 1-63 characters: lowercase letters, digits and hyphens, " +
	"not starting or ending with a hyphen")

// ErrAliasTaken is returned when another service already uses the alias.
var ErrAliasTaken = errors.New("alias is already used by another service")

var aliasPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// RenameAlias changes the .localhost alias of a service and moves its live route.
func (r *Registry) RenameAlias(ctx context.Context, idOrAlias, newAlias string) (*ServiceView, error) {
	newAlias = strings.ToLower(strings.TrimSpace(newAlias))
	if !aliasPattern.MatchString(newAlias) {
		return nil, ErrInvalidAlias
	}

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
	for _, other := range all {
		if other.ID != id && other.Alias == newAlias {
			return nil, ErrAliasTaken
		}
	}

	oldAlias := svc.Alias
	if oldAlias != newAlias {
		svc.Alias = newAlias
		if err := r.db.SaveService(ctx, *svc); err != nil {
			return nil, fmt.Errorf("saving service: %w", err)
		}
		if rt, ok := r.router.GetRoute(oldAlias); ok {
			r.router.RemoveRoute(oldAlias)
			_ = r.router.SetRoute(newAlias, rt.TargetURL, rt.LANExposed)
		}
		_ = r.db.LogEvent(ctx, "service.renamed", id, fmt.Sprintf(`{"from": %q, "to": %q}`, oldAlias, newAlias))
	}

	view := r.toServiceView(*svc)
	r.Broadcast(Event{
		Type:      "service.updated",
		ServiceID: id,
		Timestamp: time.Now().UTC(),
		Data:      view,
	})
	return &view, nil
}

func (r *Registry) resolveServiceID(ctx context.Context, idOrAlias string) (string, error) {
	if svc, err := r.db.GetService(ctx, idOrAlias); err == nil {
		return svc.ID, nil
	}
	all, err := r.db.ListServices(ctx)
	if err == nil {
		for _, s := range all {
			if s.Alias == idOrAlias {
				return s.ID, nil
			}
		}
	}
	return "", storage.ErrNotFound
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

	if err := r.supervisor.Stop(id, 3*time.Second); err != nil {
		return nil, fmt.Errorf("stopping process: %w", err)
	}

	_ = r.db.UpdateServiceStatus(ctx, id, "stopped")
	_ = r.db.LogEvent(ctx, "service.stopped", id, "")

	svc.Status = "stopped"
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
		Status:        "running",
		Alias:         alias,
		Command:       opts.Command,
		Cwd:           disc.Cwd,
		Port:          disc.Port,
		AutoPort:      false,
		RestartPolicy: "no",
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

// FollowLogs returns a subscription channel for streaming service logs.
func (r *Registry) FollowLogs(ctx context.Context, idOrAlias string) (<-chan supervisor.LogEntry, func(), error) {
	id, err := r.resolveServiceID(ctx, idOrAlias)
	if err != nil {
		return nil, nil, err
	}
	return r.supervisor.FollowLogs(id)
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
