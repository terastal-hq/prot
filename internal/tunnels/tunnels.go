package tunnels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/terastal-hq/prot/internal/askpass"
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
	// HasPassword says a password (or key passphrase) for this tunnel is kept in the OS keyring. The secret itself
	// is never part of the configuration or any API response.
	HasPassword bool `json:"has_password"`
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
	ctl        sync.Mutex // serialises Start, Stop and Remove for this tunnel
	cfg        TunnelConfig
	status     TunnelStatus
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	desiredRun bool
	done       chan struct{} // closed when the supervise loop has fully exited
}

// ErrNotFound is returned for an unknown tunnel id.
var ErrNotFound = errors.New("tunnel not found")

// Manager supervises and monitors OpenSSH forwarding tunnels.
type Manager struct {
	mu           sync.RWMutex
	tunnels      map[string]*supervisedTunnel
	sshPath      string
	readyTimeout time.Duration // how long to wait for the local forward to open
	observer     func(TunnelStatus)
	secret       func(tunnelID string) (string, error) // looks up a tunnel's stored password
	askpassPath  string                                // program ssh runs to ask for it; defaults to this binary
	configFile   string                                // ssh client configuration (-F); empty means ssh's default
}

// Timing for tunnel supervision.
const (
	defaultReadyTimeout = 15 * time.Second // covers DNS, the TCP connect (ConnectTimeout) and key exchange
	stopWait            = 5 * time.Second  // how long Stop waits for the supervise loop and ssh to exit
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

// NewManagerWithSSH is like NewManager but uses the given ssh binary instead of searching PATH.
func NewManagerWithSSH(sshPath string) *Manager {
	return newManager(sshPath)
}

// SetObserver registers a callback invoked (without any manager lock held) whenever a tunnel changes state. It
// must be set before tunnels are started.
func (m *Manager) SetObserver(fn func(TunnelStatus)) {
	m.mu.Lock()
	m.observer = fn
	m.mu.Unlock()
}

// SetConfigFile makes every ssh run read its client configuration from path instead of ~/.ssh/config, so tunnels
// resolve machine names from the same file the daemon lists them from.
func (m *Manager) SetConfigFile(path string) {
	m.mu.Lock()
	m.configFile = path
	m.mu.Unlock()
}

// SetSecretProvider registers how a tunnel's stored password is looked up. It is called on every connection
// attempt, so a keyring that was locked at boot is picked up as soon as it opens.
func (m *Manager) SetSecretProvider(fn func(tunnelID string) (string, error)) {
	m.mu.Lock()
	m.secret = fn
	m.mu.Unlock()
}

// SetAskpassProgram sets the program ssh runs to ask for a password. The default is the running binary, which
// acts as the helper when PROT_ASKPASS=1 (see package askpass).
func (m *Manager) SetAskpassProgram(path string) {
	m.mu.Lock()
	m.askpassPath = path
	m.mu.Unlock()
}

// emit reports the tunnel's current status to the observer.
func (m *Manager) emit(st *supervisedTunnel) {
	m.mu.RLock()
	fn := m.observer
	status := st.status
	m.mu.RUnlock()
	if fn != nil {
		fn(status)
	}
}

// SSHPath returns the ssh binary this manager runs.
func (m *Manager) SSHPath() string { return m.sshPath }

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

func (m *Manager) lookup(id string) (*supervisedTunnel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.tunnels[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return st, nil
}

// Start begins supervising an SSH tunnel connection. Starting a tunnel that is already connecting or connected
// returns its current status.
func (m *Manager) Start(_ context.Context, id string) (*TunnelStatus, error) {
	st, err := m.lookup(id)
	if err != nil {
		return nil, err
	}
	st.ctl.Lock()
	defer st.ctl.Unlock()

	m.mu.Lock()
	if st.desiredRun {
		statusCopy := st.status
		m.mu.Unlock()
		return &statusCopy, nil
	}

	st.desiredRun = true
	tunnelCtx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	done := make(chan struct{})
	st.done = done
	st.status.State = StateConnecting
	st.status.LastError = ""
	statusCopy := st.status
	m.mu.Unlock()

	go func() {
		defer close(done)
		m.superviseLoop(tunnelCtx, st)
	}()

	m.emit(st)
	return &statusCopy, nil
}

// Stop shuts a tunnel down and waits for its ssh process to exit. Stopping a stopped tunnel is a no-op.
func (m *Manager) Stop(id string) error {
	st, err := m.lookup(id)
	if err != nil {
		return err
	}
	m.stop(st)
	return nil
}

func (m *Manager) stop(st *supervisedTunnel) {
	st.ctl.Lock()
	defer st.ctl.Unlock()

	m.mu.Lock()
	st.desiredRun = false
	if st.cancel != nil {
		st.cancel() // exec.CommandContext kills the ssh process
		st.cancel = nil
	}
	done := st.done
	st.done = nil
	m.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-time.After(stopWait):
		}
	}

	m.mu.Lock()
	st.status.State = StateDisconnected
	st.status.PID = 0
	st.status.ConnectedAt = nil
	m.mu.Unlock()
	m.emit(st)
}

// SetHasPassword records whether the tunnel has a stored password. It applies from the next connection attempt,
// so a tunnel that is retrying picks a new password up by itself.
func (m *Manager) SetHasPassword(id string, has bool) error {
	st, err := m.lookup(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	st.cfg.HasPassword = has
	st.status.Config.HasPassword = has
	m.mu.Unlock()
	m.emit(st)
	return nil
}

// StopAll stops every tunnel in parallel and waits for them. The daemon calls it on shutdown so no ssh process
// outlives it.
func (m *Manager) StopAll() {
	m.mu.RLock()
	all := make([]*supervisedTunnel, 0, len(m.tunnels))
	for _, st := range m.tunnels {
		all = append(all, st)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, st := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.stop(st)
		}()
	}
	wg.Wait()
}

// Remove stops a tunnel and forgets it.
func (m *Manager) Remove(id string) error {
	st, err := m.lookup(id)
	if err != nil {
		return err
	}
	m.stop(st)

	m.mu.Lock()
	delete(m.tunnels, id)
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

// List returns statuses for all registered tunnels, sorted by alias (then id) so the order is stable between
// calls: clients poll this and would otherwise see the tunnels reshuffle every time.
func (m *Manager) List() []TunnelStatus {
	m.mu.RLock()
	res := make([]TunnelStatus, 0, len(m.tunnels))
	for _, st := range m.tunnels {
		res = append(res, st.status)
	}
	m.mu.RUnlock()

	sort.Slice(res, func(i, j int) bool {
		a, b := res[i].Config, res[j].Config
		if a.Alias != b.Alias {
			return a.Alias < b.Alias
		}
		return a.ID < b.ID
	})
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
		m.emit(st)

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

	env, err := m.sshEnv(st.cfg)
	if err != nil {
		return err
	}
	args := sshArgs(st.cfg, localPort)
	m.mu.RLock()
	configFile := m.configFile
	m.mu.RUnlock()
	if configFile != "" {
		args = append([]string{"-F", configFile}, args...)
	}

	cmd := exec.CommandContext(ctx, m.sshPath, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := &tailBuffer{}
	cmd.Stderr = stderr // ssh says why it failed here; keep the tail for the status instead of discarding it

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
		return withOutput(fmt.Errorf("ssh exited before the tunnel was ready: %w", exitReason(err)), stderr)
	case ok := <-ready:
		if !ok {
			_ = cmd.Process.Kill()
			<-exited
			if ctx.Err() != nil {
				return nil // stopped while connecting
			}
			return withOutput(fmt.Errorf("local port %d did not open within %s", localPort, m.readyTimeout), stderr)
		}
	}

	now := time.Now().UTC()
	m.mu.Lock()
	connected := ctx.Err() == nil // Stop may have run while we waited; do not resurrect a stopped tunnel
	if connected {
		st.status.State = StateConnected
		st.status.ConnectedAt = &now
	}
	m.mu.Unlock()
	if connected {
		m.emit(st)
	}

	err = <-exited
	if err != nil && ctx.Err() == nil {
		return withOutput(fmt.Errorf("ssh session ended: %w", err), stderr)
	}
	return nil
}

// sshArgs builds the ssh command line for a tunnel.
func sshArgs(cfg TunnelConfig, localPort uint32) []string {
	// Format: -N -L [127.0.0.1:]<localPort>:<remoteHost>:<remotePort>
	forwardSpec := fmt.Sprintf("127.0.0.1:%d:%s:%d", localPort, cfg.RemoteHost, cfg.RemotePort)
	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = fmt.Sprintf("%s@%s", cfg.SSHUser, cfg.SSHHost)
	}

	// The daemon has no terminal, so ssh must never wait for an answer it cannot get. These options come before
	// cfg.ExtraArgs on purpose: for -o options ssh keeps the first value it sees, so a tunnel cannot undo them.
	args := []string{"-N", "-L", forwardSpec}
	if cfg.HasPassword {
		// BatchMode would forbid every prompt, including the one the askpass helper answers. Instead ssh asks the
		// helper (SSH_ASKPASS_REQUIRE=force, see sshEnv), which answers only password prompts, and a wrong
		// password costs one attempt rather than three.
		args = append(args, "-o", "NumberOfPasswordPrompts=1")
	} else {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args,
		"-o", "ConnectTimeout=10",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	)

	if cfg.IdentityFile != "" {
		args = append(args, "-i", cfg.IdentityFile)
	}
	args = append(args, cfg.ExtraArgs...)
	return append(args, target)
}

// sshEnv returns the environment for ssh: nil (inherit) normally, or the parent's plus the askpass settings and
// the tunnel's secret when it has one. The secret travels in ssh's environment, readable only by this user,
// rather than on a command line.
func (m *Manager) sshEnv(cfg TunnelConfig) ([]string, error) {
	if !cfg.HasPassword {
		return nil, nil
	}

	m.mu.RLock()
	lookup, program := m.secret, m.askpassPath
	m.mu.RUnlock()

	if lookup == nil {
		return nil, errors.New("this tunnel needs a stored password but no keyring is configured")
	}
	secret, err := lookup(cfg.ID)
	if err != nil {
		return nil, fmt.Errorf("reading the stored password: %w", err)
	}
	if program == "" {
		if program, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("locating the askpass helper: %w", err)
		}
	}
	return append(os.Environ(), askpass.Env(program, secret)...), nil
}

// tailBuffer is an io.Writer that keeps only the end of what is written to it (ssh's stderr).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

const tailLimit = 2048

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailLimit {
		t.buf = t.buf[len(t.buf)-tailLimit:]
	}
	return len(p), nil
}

// summary returns the last few non-empty lines on one line, with control characters removed.
func (t *tailBuffer) summary() string {
	t.mu.Lock()
	text := string(t.buf)
	t.mu.Unlock()

	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return -1
			}
			return r
		}, strings.TrimSpace(line))
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	out := strings.Join(lines, " | ")
	if len(out) > 300 {
		out = out[:297] + "..."
	}
	return out
}

// withOutput appends ssh's own explanation to err, so the status says "Permission denied (publickey)" rather
// than just "exit status 255".
func withOutput(err error, stderr *tailBuffer) error {
	if out := stderr.summary(); out != "" {
		return fmt.Errorf("%w: %s", err, out)
	}
	return err
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
