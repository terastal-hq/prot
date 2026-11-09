package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when an entity is not found.
var ErrNotFound = errors.New("not found")

// Service lifecycle states stored in Service.Status and reported by the supervisor.
const (
	StatusStopped  = "stopped"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusCrashed  = "crashed"
)

// Restart policies for Service.RestartPolicy.
const (
	RestartNever     = "no"
	RestartOnFailure = "on-failure"
	RestartAlways    = "always"
)

// Service represents a managed or saved service definition.
type Service struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Runtime       string            `json:"runtime"` // "managed", "discovered", "remote"
	Status        string            `json:"status"`  // one of the Status* constants
	Alias         string            `json:"alias"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Cwd           string            `json:"cwd"`
	Env           map[string]string `json:"env"`
	Port          uint32            `json:"port"`
	AutoPort      bool              `json:"auto_port"`
	AutoStart     bool              `json:"auto_start"`
	RestartPolicy string            `json:"restart_policy"` // one of the Restart* constants
	LANExposed    bool              `json:"lan_exposed"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// Tunnel represents a saved SSH tunnel definition.
type Tunnel struct {
	ID           string   `json:"id"`
	Alias        string   `json:"alias"`
	SSHHost      string   `json:"ssh_host"`
	SSHUser      string   `json:"ssh_user"`
	RemoteHost   string   `json:"remote_host"`
	RemotePort   uint32   `json:"remote_port"`
	LocalPort    uint32   `json:"local_port"`
	IdentityFile string   `json:"identity_file,omitempty"`
	ExtraArgs    []string `json:"extra_args,omitempty"`
	AutoConnect  bool     `json:"auto_connect"`
	// HasPassword records that a secret for this tunnel is kept in the OS keyring. The secret is never stored here.
	HasPassword bool      `json:"has_password"`
	CreatedAt   time.Time `json:"created_at"`
}

// EventLog represents an auditable local daemon event.
type EventLog struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	ServiceID string    `json:"service_id,omitempty"`
	Payload   string    `json:"payload,omitempty"`
}

// DB wraps SQLite operations. database/sql already serialises access (the pool holds a single connection), so
// methods need no extra locking.
type DB struct {
	db          *sql.DB
	eventWrites atomic.Int64
}

// Event log retention: the table is trimmed every pruneEveryWrites inserts to the newest maxEventLogRows rows.
const (
	maxEventLogRows  = 10000
	pruneEveryWrites = 200
)

// Open initializes SQLite database at dbPath with WAL mode and schema.
func Open(dbPath string) (*DB, error) {
	if dbPath == "" {
		return nil, errors.New("empty dbPath")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	dsn := fmt.Sprintf(
		"%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
			"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		dbPath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1) // modernc sqlite performs best with serialized writer
	db.SetMaxIdleConns(1)

	s := &DB{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating database schema: %w", err)
	}

	return s, nil
}

// Close closes the database.
func (s *DB) Close() error {
	return s.db.Close()
}

// migrations are applied in order; the number applied is stored in PRAGMA user_version. Never edit an entry that
// has shipped: append a new one instead.
var migrations = []string{
	// 1: initial schema
	`
	CREATE TABLE IF NOT EXISTS services (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		runtime TEXT NOT NULL,
		status TEXT NOT NULL,
		alias TEXT NOT NULL,
		command TEXT NOT NULL,
		args_json TEXT NOT NULL,
		cwd TEXT NOT NULL,
		env_json TEXT NOT NULL,
		port INTEGER NOT NULL,
		auto_port INTEGER NOT NULL,
		auto_start INTEGER NOT NULL,
		restart_policy TEXT NOT NULL,
		lan_exposed INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_services_alias ON services(alias);

	CREATE TABLE IF NOT EXISTS tunnels (
		id TEXT PRIMARY KEY,
		alias TEXT NOT NULL,
		ssh_host TEXT NOT NULL,
		ssh_user TEXT NOT NULL,
		remote_host TEXT NOT NULL,
		remote_port INTEGER NOT NULL,
		local_port INTEGER NOT NULL,
		identity_file TEXT NOT NULL,
		extra_args_json TEXT NOT NULL,
		auto_connect INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS event_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		event_type TEXT NOT NULL,
		service_id TEXT NOT NULL,
		payload TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_event_logs_timestamp ON event_logs(timestamp);
	`,
	// 2: drop the never-used workspace tables created by early builds
	`
	DROP TABLE IF EXISTS workspace_services;
	DROP TABLE IF EXISTS workspaces;
	`,
	// 3: tunnels can have a password or key passphrase kept in the OS keyring (only a flag lives here)
	`ALTER TABLE tunnels ADD COLUMN has_password INTEGER NOT NULL DEFAULT 0;`,
}

func (s *DB) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this prot (supports %d); upgrade prot",
			version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", i+1, err)
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value, what string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing %s %q: %w", what, value, err)
	}
	return t, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

const serviceColumns = `id, name, runtime, status, alias, command, args_json, cwd, env_json,
	port, auto_port, auto_start, restart_policy, lan_exposed, created_at, updated_at`

func scanService(row rowScanner) (Service, error) {
	var svc Service
	var argsJSON, envJSON, createdAt, updatedAt string
	var autoPort, autoStart, lanExposed int

	err := row.Scan(
		&svc.ID, &svc.Name, &svc.Runtime, &svc.Status, &svc.Alias, &svc.Command,
		&argsJSON, &svc.Cwd, &envJSON, &svc.Port,
		&autoPort, &autoStart, &svc.RestartPolicy, &lanExposed,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return Service{}, err
	}

	svc.Args = []string{}
	if err := json.Unmarshal([]byte(argsJSON), &svc.Args); err != nil {
		return Service{}, fmt.Errorf("decoding args of service %q: %w", svc.ID, err)
	}
	if svc.Args == nil { // the stored value was JSON null
		svc.Args = []string{}
	}
	svc.Env = map[string]string{}
	if err := json.Unmarshal([]byte(envJSON), &svc.Env); err != nil {
		return Service{}, fmt.Errorf("decoding env of service %q: %w", svc.ID, err)
	}
	if svc.Env == nil {
		svc.Env = map[string]string{}
	}

	svc.AutoPort = autoPort == 1
	svc.AutoStart = autoStart == 1
	svc.LANExposed = lanExposed == 1
	if svc.CreatedAt, err = parseTime(createdAt, "created_at"); err != nil {
		return Service{}, err
	}
	if svc.UpdatedAt, err = parseTime(updatedAt, "updated_at"); err != nil {
		return Service{}, err
	}
	return svc, nil
}

// SaveService inserts or updates a Service.
func (s *DB) SaveService(ctx context.Context, svc Service) error {
	argsJSON, err := json.Marshal(svc.Args)
	if err != nil {
		return fmt.Errorf("encoding args: %w", err)
	}
	envJSON, err := json.Marshal(svc.Env)
	if err != nil {
		return fmt.Errorf("encoding env: %w", err)
	}

	now := time.Now().UTC()
	if svc.CreatedAt.IsZero() {
		svc.CreatedAt = now
	}
	svc.UpdatedAt = now

	_, err = s.db.ExecContext(ctx, `
	INSERT INTO services (`+serviceColumns+`)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name=excluded.name,
		runtime=excluded.runtime,
		status=excluded.status,
		alias=excluded.alias,
		command=excluded.command,
		args_json=excluded.args_json,
		cwd=excluded.cwd,
		env_json=excluded.env_json,
		port=excluded.port,
		auto_port=excluded.auto_port,
		auto_start=excluded.auto_start,
		restart_policy=excluded.restart_policy,
		lan_exposed=excluded.lan_exposed,
		updated_at=excluded.updated_at`,
		svc.ID, svc.Name, svc.Runtime, svc.Status, svc.Alias, svc.Command, string(argsJSON), svc.Cwd,
		string(envJSON), svc.Port, boolToInt(svc.AutoPort), boolToInt(svc.AutoStart), svc.RestartPolicy,
		boolToInt(svc.LANExposed), formatTime(svc.CreatedAt), formatTime(svc.UpdatedAt),
	)
	return err
}

// GetService fetches a service by ID.
func (s *DB) GetService(ctx context.Context, id string) (*Service, error) {
	return s.queryService(ctx, "WHERE id = ?", id)
}

// ResolveService fetches a service by ID or alias in one query. An exact ID match wins over an alias match.
func (s *DB) ResolveService(ctx context.Context, idOrAlias string) (*Service, error) {
	if idOrAlias == "" {
		return nil, ErrNotFound // services without an alias store "", which must never match
	}
	return s.queryService(ctx,
		"WHERE id = ?1 OR alias = ?1 ORDER BY (id = ?1) DESC LIMIT 1", idOrAlias)
}

func (s *DB) queryService(ctx context.Context, where string, args ...any) (*Service, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+serviceColumns+" FROM services "+where, args...)
	svc, err := scanService(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

// ListServices returns all registered services.
func (s *DB) ListServices(ctx context.Context) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+serviceColumns+" FROM services ORDER BY name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Service{}
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, svc)
	}
	return list, rows.Err()
}

// DeleteService removes a service by ID.
func (s *DB) DeleteService(ctx context.Context, id string) error {
	return s.deleteByID(ctx, "services", id)
}

func (s *DB) deleteByID(ctx context.Context, table, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateServiceStatus updates only the status of a service.
func (s *DB) UpdateServiceStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE services SET status = ?, updated_at = ? WHERE id = ?", status, formatTime(time.Now()), id)
	return err
}

// SaveTunnel saves or updates an SSH tunnel definition.
func (s *DB) SaveTunnel(ctx context.Context, tun Tunnel) error {
	extraArgsJSON, err := json.Marshal(tun.ExtraArgs)
	if err != nil {
		return fmt.Errorf("encoding extra args: %w", err)
	}
	if tun.CreatedAt.IsZero() {
		tun.CreatedAt = time.Now().UTC()
	}

	_, err = s.db.ExecContext(ctx, `
	INSERT INTO tunnels (
		id, alias, ssh_host, ssh_user, remote_host, remote_port, local_port,
		identity_file, extra_args_json, auto_connect, has_password, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		alias=excluded.alias,
		ssh_host=excluded.ssh_host,
		ssh_user=excluded.ssh_user,
		remote_host=excluded.remote_host,
		remote_port=excluded.remote_port,
		local_port=excluded.local_port,
		identity_file=excluded.identity_file,
		extra_args_json=excluded.extra_args_json,
		auto_connect=excluded.auto_connect,
		has_password=excluded.has_password`,
		tun.ID, tun.Alias, tun.SSHHost, tun.SSHUser, tun.RemoteHost, tun.RemotePort, tun.LocalPort,
		tun.IdentityFile, string(extraArgsJSON), boolToInt(tun.AutoConnect), boolToInt(tun.HasPassword),
		formatTime(tun.CreatedAt),
	)
	return err
}

// ListTunnels returns all saved tunnels.
func (s *DB) ListTunnels(ctx context.Context) ([]Tunnel, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, alias, ssh_host, ssh_user, remote_host, remote_port, local_port,
	       identity_file, extra_args_json, auto_connect, has_password, created_at
	FROM tunnels ORDER BY alias ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Tunnel{}
	for rows.Next() {
		var tun Tunnel
		var extraArgsJSON, createdAt string
		var autoConnect, hasPassword int

		err := rows.Scan(
			&tun.ID, &tun.Alias, &tun.SSHHost, &tun.SSHUser, &tun.RemoteHost, &tun.RemotePort,
			&tun.LocalPort, &tun.IdentityFile, &extraArgsJSON, &autoConnect, &hasPassword, &createdAt,
		)
		if err != nil {
			return nil, err
		}

		tun.ExtraArgs = []string{}
		if err := json.Unmarshal([]byte(extraArgsJSON), &tun.ExtraArgs); err != nil {
			return nil, fmt.Errorf("decoding extra args of tunnel %q: %w", tun.ID, err)
		}
		if tun.ExtraArgs == nil {
			tun.ExtraArgs = []string{}
		}
		tun.AutoConnect = autoConnect == 1
		tun.HasPassword = hasPassword == 1
		if tun.CreatedAt, err = parseTime(createdAt, "created_at"); err != nil {
			return nil, err
		}
		list = append(list, tun)
	}
	return list, rows.Err()
}

// DeleteTunnel removes a saved tunnel by ID.
func (s *DB) DeleteTunnel(ctx context.Context, id string) error {
	return s.deleteByID(ctx, "tunnels", id)
}

// LogEvent records a structured audit event and periodically trims old ones.
func (s *DB) LogEvent(ctx context.Context, eventType, serviceID, payload string) error {
	_, err := s.db.ExecContext(
		ctx,
		"INSERT INTO event_logs (timestamp, event_type, service_id, payload) VALUES (?, ?, ?, ?)",
		formatTime(time.Now()), eventType, serviceID, payload,
	)
	if err != nil {
		return err
	}

	if s.eventWrites.Add(1)%pruneEveryWrites == 0 {
		return s.pruneEvents(ctx)
	}
	return nil
}

func (s *DB) pruneEvents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM event_logs WHERE id <= (SELECT MAX(id) FROM event_logs) - ?", maxEventLogRows)
	return err
}

// GetRecentEvents retrieves the most recent events bounded by limit.
func (s *DB) GetRecentEvents(ctx context.Context, limit int) ([]EventLog, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
	SELECT id, timestamp, event_type, service_id, payload
	FROM event_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []EventLog{}
	for rows.Next() {
		var ev EventLog
		var timestamp string
		if err := rows.Scan(&ev.ID, &timestamp, &ev.Type, &ev.ServiceID, &ev.Payload); err != nil {
			return nil, err
		}
		if ev.Timestamp, err = parseTime(timestamp, "event timestamp"); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}
