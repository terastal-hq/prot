package tunnels

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"syscall"
	"time"
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
	mu      sync.RWMutex
	tunnels map[string]*supervisedTunnel
	sshPath string
}

// NewManager constructs a new SSH tunnel supervisor.
func NewManager() (*Manager, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("ssh binary not found in PATH: %w", err)
	}
	return &Manager{
		tunnels: make(map[string]*supervisedTunnel),
		sshPath: sshPath,
	}, nil
}

// Register registers a tunnel configuration.
func (m *Manager) Register(cfg TunnelConfig) (*TunnelStatus, error) {
	if cfg.ID == "" {
		return nil, errors.New("tunnel ID is required")
	}
	if cfg.SSHHost == "" {
		return nil, errors.New("ssh_host is required")
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
	backoff := 500 * time.Millisecond
	maxBackoff := 15 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.mu.RLock()
		desired := st.desiredRun
		m.mu.RUnlock()
		if !desired {
			return
		}

		err := m.runOnce(ctx, st)
		if err == nil || ctx.Err() != nil {
			return
		}

		m.mu.Lock()
		st.status.LastError = err.Error()
		st.status.State = StateReconnecting
		st.status.Reconnections++
		m.mu.Unlock()

		select {
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) runOnce(ctx context.Context, st *supervisedTunnel) error {
	localPort := st.cfg.LocalPort
	if localPort == 0 {
		freePort, err := findFreePort()
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

	// Wait for local port readiness (up to 3 seconds)
	ready := waitForPort(ctx, localPort, 3*time.Second)
	if !ready {
		// Check if process exited early
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return fmt.Errorf("ssh exited prematurely with code %d", cmd.ProcessState.ExitCode())
		}
	}

	now := time.Now().UTC()
	m.mu.Lock()
	st.status.State = StateConnected
	st.status.ConnectedAt = &now
	m.mu.Unlock()

	// Wait for ssh process to terminate
	err := cmd.Wait()

	m.mu.Lock()
	st.status.PID = 0
	st.status.ConnectedAt = nil
	m.mu.Unlock()

	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("ssh session ended: %w", err)
	}
	return nil
}

func findFreePort() (uint32, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return uint32(ln.Addr().(*net.TCPAddr).Port), nil
}

func waitForPort(ctx context.Context, port uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// FindFreePort exported helper
func FindFreePort() (uint32, error) {
	return findFreePort()
}
