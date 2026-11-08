package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when an entity is not found.
var ErrNotFound = errors.New("not found")

// Service represents a managed or saved service definition.
type Service struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Runtime       string            `json:"runtime"` // "managed", "discovered", "remote"
	Status        string            `json:"status"`  // "stopped", "starting", "running", "unhealthy", "unknown"
	Alias         string            `json:"alias"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Cwd           string            `json:"cwd"`
	Env           map[string]string `json:"env"`
	Port          uint32            `json:"port"`
	AutoPort      bool              `json:"auto_port"`
	AutoStart     bool              `json:"auto_start"`
	RestartPolicy string            `json:"restart_policy"` // "no", "always", "on-failure"
	LANExposed    bool              `json:"lan_exposed"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// Tunnel represents a saved SSH tunnel definition.
type Tunnel struct {
	ID           string    `json:"id"`
	Alias        string    `json:"alias"`
	SSHHost      string    `json:"ssh_host"`
	SSHUser      string    `json:"ssh_user"`
	RemoteHost   string    `json:"remote_host"`
	RemotePort   uint32    `json:"remote_port"`
	LocalPort    uint32    `json:"local_port"`
	IdentityFile string    `json:"identity_file,omitempty"`
	ExtraArgs    []string  `json:"extra_args,omitempty"`
	AutoConnect  bool      `json:"auto_connect"`
	CreatedAt    time.Time `json:"created_at"`
}

// Workspace represents a project workspace grouping services.
type Workspace struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ServiceIDs  []string  `json:"service_ids"`
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

// DB wraps SQLite operations.
type DB struct {
	db *sql.DB
	mu sync.RWMutex
}

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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

func (s *DB) migrate() error {
	schema := `
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

	CREATE TABLE IF NOT EXISTS workspaces (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS workspace_services (
		workspace_id TEXT NOT NULL,
		service_id TEXT NOT NULL,
		PRIMARY KEY (workspace_id, service_id),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (service_id) REFERENCES services(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS event_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		event_type TEXT NOT NULL,
		service_id TEXT NOT NULL,
		payload TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_event_logs_timestamp ON event_logs(timestamp);
	`
	_, err := s.db.Exec(schema)
	return err
}

// SaveService inserts or updates a Service.
func (s *DB) SaveService(ctx context.Context, svc Service) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	argsBytes, _ := json.Marshal(svc.Args)
	envBytes, _ := json.Marshal(svc.Env)
	if svc.CreatedAt.IsZero() {
		svc.CreatedAt = time.Now().UTC()
	}
	svc.UpdatedAt = time.Now().UTC()

	query := `
	INSERT INTO services (
		id, name, runtime, status, alias, command, args_json, cwd, env_json,
		port, auto_port, auto_start, restart_policy, lan_exposed, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		updated_at=excluded.updated_at;
	`

	autoPortInt := 0
	if svc.AutoPort {
		autoPortInt = 1
	}
	autoStartInt := 0
	if svc.AutoStart {
		autoStartInt = 1
	}
	lanExposedInt := 0
	if svc.LANExposed {
		lanExposedInt = 1
	}

	_, err := s.db.ExecContext(
		ctx,
		query,
		svc.ID,
		svc.Name,
		svc.Runtime,
		svc.Status,
		svc.Alias,
		svc.Command,
		string(argsBytes),
		svc.Cwd,
		string(envBytes),
		svc.Port,
		autoPortInt,
		autoStartInt,
		svc.RestartPolicy,
		lanExposedInt,
		svc.CreatedAt.Format(time.RFC3339Nano),
		svc.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetService fetches a service by ID.
func (s *DB) GetService(ctx context.Context, id string) (*Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, name, runtime, status, alias, command, args_json, cwd, env_json,
	       port, auto_port, auto_start, restart_policy, lan_exposed, created_at, updated_at
	FROM services WHERE id = ?
	`
	row := s.db.QueryRowContext(ctx, query, id)

	var svc Service
	var argsJSON, envJSON, createdAtStr, updatedAtStr string
	var autoPortInt, autoStartInt, lanExposedInt int

	err := row.Scan(
		&svc.ID, &svc.Name, &svc.Runtime, &svc.Status, &svc.Alias, &svc.Command,
		&argsJSON, &svc.Cwd, &envJSON, &svc.Port,
		&autoPortInt, &autoStartInt, &svc.RestartPolicy, &lanExposedInt,
		&createdAtStr, &updatedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	_ = json.Unmarshal([]byte(argsJSON), &svc.Args)
	if svc.Args == nil {
		svc.Args = []string{}
	}
	_ = json.Unmarshal([]byte(envJSON), &svc.Env)
	if svc.Env == nil {
		svc.Env = map[string]string{}
	}
	svc.AutoPort = autoPortInt == 1
	svc.AutoStart = autoStartInt == 1
	svc.LANExposed = lanExposedInt == 1
	svc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
	svc.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr)

	return &svc, nil
}

// ListServices returns all registered services.
func (s *DB) ListServices(ctx context.Context) ([]Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, name, runtime, status, alias, command, args_json, cwd, env_json,
	       port, auto_port, auto_start, restart_policy, lan_exposed, created_at, updated_at
	FROM services ORDER BY name ASC
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Service{}
	for rows.Next() {
		var svc Service
		var argsJSON, envJSON, createdAtStr, updatedAtStr string
		var autoPortInt, autoStartInt, lanExposedInt int

		err := rows.Scan(
			&svc.ID, &svc.Name, &svc.Runtime, &svc.Status, &svc.Alias, &svc.Command,
			&argsJSON, &svc.Cwd, &envJSON, &svc.Port,
			&autoPortInt, &autoStartInt, &svc.RestartPolicy, &lanExposedInt,
			&createdAtStr, &updatedAtStr,
		)
		if err != nil {
			return nil, err
		}

		_ = json.Unmarshal([]byte(argsJSON), &svc.Args)
		if svc.Args == nil {
			svc.Args = []string{}
		}
		_ = json.Unmarshal([]byte(envJSON), &svc.Env)
		if svc.Env == nil {
			svc.Env = map[string]string{}
		}
		svc.AutoPort = autoPortInt == 1
		svc.AutoStart = autoStartInt == 1
		svc.LANExposed = lanExposedInt == 1
		svc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		svc.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtStr)

		list = append(list, svc)
	}

	return list, rows.Err()
}

// DeleteService removes a service by ID.
func (s *DB) DeleteService(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, "DELETE FROM services WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateServiceStatus updates only the status of a service.
func (s *DB) UpdateServiceStatus(ctx context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, "UPDATE services SET status = ?, updated_at = ? WHERE id = ?", status, now, id)
	return err
}

// SaveTunnel saves or updates an SSH tunnel definition.
func (s *DB) SaveTunnel(ctx context.Context, tun Tunnel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	extraArgsBytes, _ := json.Marshal(tun.ExtraArgs)
	if tun.CreatedAt.IsZero() {
		tun.CreatedAt = time.Now().UTC()
	}

	autoConnectInt := 0
	if tun.AutoConnect {
		autoConnectInt = 1
	}

	query := `
	INSERT INTO tunnels (
		id, alias, ssh_host, ssh_user, remote_host, remote_port, local_port,
		identity_file, extra_args_json, auto_connect, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		alias=excluded.alias,
		ssh_host=excluded.ssh_host,
		ssh_user=excluded.ssh_user,
		remote_host=excluded.remote_host,
		remote_port=excluded.remote_port,
		local_port=excluded.local_port,
		identity_file=excluded.identity_file,
		extra_args_json=excluded.extra_args_json,
		auto_connect=excluded.auto_connect;
	`

	_, err := s.db.ExecContext(
		ctx,
		query,
		tun.ID,
		tun.Alias,
		tun.SSHHost,
		tun.SSHUser,
		tun.RemoteHost,
		tun.RemotePort,
		tun.LocalPort,
		tun.IdentityFile,
		string(extraArgsBytes),
		autoConnectInt,
		tun.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// ListTunnels returns all saved tunnels.
func (s *DB) ListTunnels(ctx context.Context) ([]Tunnel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
	SELECT id, alias, ssh_host, ssh_user, remote_host, remote_port, local_port,
	       identity_file, extra_args_json, auto_connect, created_at
	FROM tunnels ORDER BY alias ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Tunnel{}
	for rows.Next() {
		var tun Tunnel
		var extraArgsJSON, createdAtStr string
		var autoConnectInt int

		err := rows.Scan(
			&tun.ID, &tun.Alias, &tun.SSHHost, &tun.SSHUser, &tun.RemoteHost, &tun.RemotePort,
			&tun.LocalPort, &tun.IdentityFile, &extraArgsJSON, &autoConnectInt, &createdAtStr,
		)
		if err != nil {
			return nil, err
		}

		_ = json.Unmarshal([]byte(extraArgsJSON), &tun.ExtraArgs)
		if tun.ExtraArgs == nil {
			tun.ExtraArgs = []string{}
		}
		tun.AutoConnect = autoConnectInt == 1
		tun.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		list = append(list, tun)
	}

	return list, rows.Err()
}

// DeleteTunnel removes a saved tunnel by ID.
func (s *DB) DeleteTunnel(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, "DELETE FROM tunnels WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LogEvent records a structured audit event.
func (s *DB) LogEvent(ctx context.Context, eventType, serviceID, payload string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(
		ctx,
		"INSERT INTO event_logs (timestamp, event_type, service_id, payload) VALUES (?, ?, ?, ?)",
		now,
		eventType,
		serviceID,
		payload,
	)
	return err
}

// GetRecentEvents retrieves the most recent events bounded by limit.
func (s *DB) GetRecentEvents(ctx context.Context, limit int) ([]EventLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
	SELECT id, timestamp, event_type, service_id, payload
	FROM event_logs ORDER BY id DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []EventLog{}
	for rows.Next() {
		var ev EventLog
		var tsStr string
		if err := rows.Scan(&ev.ID, &tsStr, &ev.Type, &ev.ServiceID, &ev.Payload); err != nil {
			return nil, err
		}
		ev.Timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
		events = append(events, ev)
	}

	return events, rows.Err()
}
