package tunnels

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
)

// State represents the lifecycle status of an SSH tunnel.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateFailed       State = "failed"
)

// TunnelConfig defines a remote SSH forwarded endpoint.
type TunnelConfig struct {
	ID           string   `json:"id"`
	Alias        string   `json:"alias"`
	SSHHost      string   `json:"ssh_host"`    // Remote SSH server or host alias in ~/.ssh/config
	SSHUser      string   `json:"ssh_user"`    // Optional user
	RemoteHost   string   `json:"remote_host"` // Address on the remote machine (e.g. 127.0.0.1)
	RemotePort   uint32   `json:"remote_port"` // Port on the remote machine (e.g. 3000)
	LocalPort    uint32   `json:"local_port"`  // Desired local port, 0 for automatic
	IdentityFile string   `json:"identity_file,omitempty"`
	ExtraArgs    []string `json:"extra_args,omitempty"`
	AutoConnect  bool     `json:"auto_connect"`
}

// TunnelStatus reports live runtime state for a supervised tunnel.
type TunnelStatus struct {
	Config            TunnelConfig `json:"config"`
	State             State        `json:"state"`
	AssignedLocalPort uint32       `json:"assigned_local_port"`
	PID               int          `json:"pid,omitempty"`
	LastError         string       `json:"last_error,omitempty"`
	ConnectedAt       *time.Time   `json:"connected_at,omitempty"`
	Reconnections     int          `json:"reconnections"`
}

type supervisedTunnel struct {
	cfg        TunnelConfig
	status     TunnelStatus
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	desiredRun bool
}

// Manager supervises and monitors OpenSSH forwarding tunnels.
type Manager struct {
	mu           sync.RWMutex
	tunnels      map[string]*supervisedTunnel
	sshPath      string
	readyTimeout time.Duration // how long to wait for the local forward to open
}

// Timing for tunnel supervision.
const (
	defaultReadyTimeout = 3 * time.Second
	initialBackoff      = 500 * time.Millisecond
	maxBackoff          = 15 * time.Second
	stableConnection    = 30 * time.Second // a connection this long resets the reconnect backoff
)

// NewManager constructs a new SSH tunnel supervisor.
func NewManager() (*Manager, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("ssh binary not found in PATH: %w", err)
	}
	return newManager(sshPath), nil
}

func newManager(sshPath string) *Manager {
	return &Manager{
		tunnels:      make(map[string]*supervisedTunnel),
		sshPath:      sshPath,
		readyTimeout: defaultReadyTimeout,
	}
}

// Register registers a tunnel configuration.
func (m *Manager) Register(cfg TunnelConfig) (*TunnelStatus, error) {
	if cfg.ID == "" {
		return nil, errors.New("tunnel ID is required")
	}
	if cfg.SSHHost == "" {
		return nil, errors.New("ssh_host is required")
	}
	// ssh would parse a leading "-" as an option (for example -oProxyCommand=...), not a destination.
	if strings.HasPrefix(cfg.SSHHost, "-") || strings.HasPrefix(cfg.SSHUser, "-") {
		return nil, errors.New("ssh_host and ssh_user must not start with '-'")
	}
	if cfg.RemoteHost == "" {
		cfg.RemoteHost = "127.0.0.1"
	}
	if cfg.RemotePort == 0 {
		return nil, errors.New("remote_port must be greater than 0")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tunnels[cfg.ID]; exists {
		return nil, fmt.Errorf("tunnel %q is already registered", cfg.ID)
	}

	st := &supervisedTunnel{
		cfg: cfg,
		status: TunnelStatus{
			Config: cfg,
			State:  StateDisconnected,
		},
	}
	m.tunnels[cfg.ID] = st
	statusCopy := st.status
	return &statusCopy, nil
}

// Start begins supervising an SSH tunnel connection.
func (m *Manager) Start(ctx context.Context, id string) (*TunnelStatus, error) {
	m.mu.Lock()
	st, exists := m.tunnels[id]
	if !exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("tunnel %q not found", id)
	}
	if st.status.State == StateConnected || st.status.State == StateConnecting {
		statusCopy := st.status
		m.mu.Unlock()
		return &statusCopy, nil
	}

	st.desiredRun = true
	tunnelCtx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	st.status.State = StateConnecting
	st.status.LastError = ""
	statusCopy := st.status
	m.mu.Unlock()

	go m.superviseLoop(tunnelCtx, st)

	return &statusCopy, nil
}

// Stop gracefully shuts down a supervised tunnel.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	st, exists := m.tunnels[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("tunnel %q not found", id)
	}

	st.desiredRun = false
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}

	if st.cmd != nil && st.cmd.Process != nil {
		_ = st.cmd.Process.Signal(syscall.SIGTERM)
	}

	st.status.State = StateDisconnected
	st.status.PID = 0
	st.status.ConnectedAt = nil
	m.mu.Unlock()

	return nil
}

// GetStatus returns the current status for a tunnel.
func (m *Manager) GetStatus(id string) (*TunnelStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, exists := m.tunnels[id]
	if !exists {
		return nil, false
	}
	copy := st.status
	return &copy, true
}

// List returns statuses for all registered tunnels.
func (m *Manager) List() []TunnelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]TunnelStatus, 0, len(m.tunnels))
	for _, st := range m.tunnels {
		res = append(res, st.status)
	}
	return res
}

func (m *Manager) superviseLoop(ctx context.Context, st *supervisedTunnel) {
	backoff := initialBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		m.mu.RLock()
		desired := st.desiredRun
		m.mu.RUnlock()
		if !desired {
			return
		}

		began := time.Now()
		err := m.runOnce(ctx, st)
		if err == nil || ctx.Err() != nil {
			return
		}
		if time.Since(began) >= stableConnection {
			backoff = initialBackoff
		}

		m.mu.Lock()
		st.status.LastError = err.Error()
		st.status.State = StateReconnecting
		st.status.Reconnections++
		m.mu.Unlock()

		select {
		case <-time.After(backoff):
			backoff = min(backoff*2, maxBackoff)
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) runOnce(ctx context.Context, st *supervisedTunnel) error {
	localPort := st.cfg.LocalPort
	if localPort == 0 {
		freePort, err := netutil.FreePort()
		if err != nil {
			return fmt.Errorf("allocate local port: %w", err)
		}
		localPort = freePort
	}

	// Format: -N -L [127.0.0.1:]<localPort>:<remoteHost>:<remotePort>
	forwardSpec := fmt.Sprintf("127.0.0.1:%d:%s:%d", localPort, st.cfg.RemoteHost, st.cfg.RemotePort)
	target := st.cfg.SSHHost
	if st.cfg.SSHUser != "" {
		target = fmt.Sprintf("%s@%s", st.cfg.SSHUser, st.cfg.SSHHost)
	}

	args := []string{
		"-N", // Do not execute remote command
		"-L", forwardSpec,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}

	if st.cfg.IdentityFile != "" {
		args = append(args, "-i", st.cfg.IdentityFile)
	}
	args = append(args, st.cfg.ExtraArgs...)
	args = append(args, target)

	cmd := exec.CommandContext(ctx, m.sshPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ssh process: %w", err)
	}

	m.mu.Lock()
	st.cmd = cmd
	st.status.AssignedLocalPort = localPort
	st.status.PID = cmd.Process.Pid
	m.mu.Unlock()
	defer m.clearProcess(st)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Only report "connected" once the local forward really accepts connections. ssh exiting first (bad host,
	// refused auth, port already taken) or the port never opening is a failed attempt to retry.
	readyCtx, cancelReady := context.WithTimeout(ctx, m.readyTimeout)
	defer cancelReady()
	ready := make(chan bool, 1)
	go func() { ready <- netutil.WaitForPort(readyCtx, localPort, m.readyTimeout) }()

	select {
	case err := <-exited:
		return fmt.Errorf("ssh exited before the tunnel was ready: %w", exitReason(err))
	case ok := <-ready:
		if !ok {
			_ = cmd.Process.Kill()
			<-exited
			if ctx.Err() != nil {
				return nil // stopped while connecting
			}
			return fmt.Errorf("local port %d did not open within %s", localPort, m.readyTimeout)
		}
	}

	now := time.Now().UTC()
	m.mu.Lock()
	if ctx.Err() == nil { // Stop may have run while we waited; do not resurrect a stopped tunnel
		st.status.State = StateConnected
		st.status.ConnectedAt = &now
	}
	m.mu.Unlock()

	err := <-exited
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("ssh session ended: %w", err)
	}
	return nil
}

// clearProcess forgets the finished ssh process.
func (m *Manager) clearProcess(st *supervisedTunnel) {
	m.mu.Lock()
	st.status.PID = 0
	st.status.ConnectedAt = nil
	st.cmd = nil
	m.mu.Unlock()
}

// exitReason describes an ssh exit that returned no Go error (a clean exit 0) as such.
func exitReason(err error) error {
	if err == nil {
		return errors.New("exit status 0")
	}
	return err
}
