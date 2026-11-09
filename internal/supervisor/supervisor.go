package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/storage"
)

// ProcessState describes the live execution state of a supervised process.
type ProcessState struct {
	ID           string     `json:"id"`
	PID          int        `json:"pid"`
	PGID         int        `json:"pgid"`
	Status       string     `json:"status"` // "stopped", "starting", "running", "crashed"
	StartedAt    *time.Time `json:"started_at,omitempty"`
	AssignedPort uint32     `json:"assigned_port"`
	ExitCode     int        `json:"exit_code"`
	RestartCount int        `json:"restart_count"`
}

type runningProcess struct {
	mu           sync.RWMutex
	svc          storage.Service
	cmd          *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
	state        ProcessState
	logs         *LogRing
	shouldRun    bool
	restartTimer *time.Timer
}

// Supervisor coordinates starting, stopping, and logging child processes.
type Supervisor struct {
	mu        sync.RWMutex
	processes map[string]*runningProcess
	onStatus  func(serviceID, status string, port uint32)
}

// New constructs a Supervisor.
func New(onStatus func(serviceID, status string, port uint32)) *Supervisor {
	return &Supervisor{
		processes: make(map[string]*runningProcess),
		onStatus:  onStatus,
	}
}

// Start spawns a managed service child process.
func (s *Supervisor) Start(ctx context.Context, svc storage.Service) (*ProcessState, error) {
	s.mu.Lock()
	rp, exists := s.processes[svc.ID]
	if !exists {
		rp = &runningProcess{
			svc:  svc,
			logs: NewLogRing(2000),
			state: ProcessState{
				ID:     svc.ID,
				Status: "stopped",
			},
		}
		s.processes[svc.ID] = rp
	}
	s.mu.Unlock()

	rp.mu.Lock()
	if rp.state.Status == "running" || rp.state.Status == "starting" {
		st := rp.state
		rp.mu.Unlock()
		return &st, nil
	}

	rp.shouldRun = true
	rp.svc = svc
	rp.state.Status = "starting"
	rp.state.ExitCode = 0
	rp.mu.Unlock()

	if err := s.launchProcess(rp); err != nil {
		rp.mu.Lock()
		rp.state.Status = "crashed"
		rp.shouldRun = false
		st := rp.state
		rp.mu.Unlock()
		if s.onStatus != nil {
			s.onStatus(svc.ID, "crashed", 0)
		}
		return &st, err
	}

	rp.mu.RLock()
	st := rp.state
	rp.mu.RUnlock()
	return &st, nil
}

func (s *Supervisor) launchProcess(rp *runningProcess) error {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	svc := rp.svc
	if svc.Command == "" {
		return errors.New("command cannot be empty")
	}

	// Port assignment
	assignedPort := svc.Port
	if svc.AutoPort {
		freePort, err := netutil.FreePort()
		if err != nil {
			return fmt.Errorf("allocating auto port: %w", err)
		}
		assignedPort = freePort
	}
	rp.state.AssignedPort = assignedPort

	// Prepare environment
	env := os.Environ()
	for k, v := range svc.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	if assignedPort > 0 {
		env = append(env, fmt.Sprintf("PORT=%d", assignedPort))
	}

	// Prepare command execution (using shell to support pipelines / flags)
	ctx, cancel := context.WithCancel(context.Background())
	rp.ctx = ctx
	rp.cancel = cancel

	fullCmd := svc.Command
	if len(svc.Args) > 0 {
		fullCmd = fmt.Sprintf("%s %s", svc.Command, strings.Join(svc.Args, " "))
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", fullCmd)
	if svc.Cwd != "" {
		cmd.Dir = svc.Cwd
	}
	cmd.Env = env

	// Use Process Groups so killing terminates all child processes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe error: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe error: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start process: %w", err)
	}

	pid := cmd.Process.Pid
	now := time.Now().UTC()
	rp.cmd = cmd
	rp.state.PID = pid
	rp.state.PGID = pid
	rp.state.StartedAt = &now
	rp.state.Status = "starting"

	rp.logs.Append("system", fmt.Sprintf("Process started with PID %d (command: %s)", pid, fullCmd))

	// Stream logs asynchronously
	go captureOutput(stdoutPipe, rp.logs, "stdout")
	go captureOutput(stderrPipe, rp.logs, "stderr")

	// Wait loop and supervision goroutine
	go s.monitorProcess(rp, cmd, assignedPort)

	return nil
}

func (s *Supervisor) monitorProcess(rp *runningProcess, cmd *exec.Cmd, port uint32) {
	// Probe readiness if port is known
	if port > 0 {
		go func() {
			rp.mu.RLock()
			procCtx := rp.ctx
			rp.mu.RUnlock()

			if netutil.WaitForPort(procCtx, port, 10*time.Second) {
				rp.mu.Lock()
				if rp.state.Status == "starting" {
					rp.state.Status = "running"
					rp.logs.Append("system", fmt.Sprintf("Service is ready and listening on port %d", port))
				}
				rp.mu.Unlock()
				if s.onStatus != nil {
					s.onStatus(rp.svc.ID, "running", port)
				}
			}
		}()
	} else {
		rp.mu.Lock()
		rp.state.Status = "running"
		rp.mu.Unlock()
		if s.onStatus != nil {
			s.onStatus(rp.svc.ID, "running", 0)
		}
	}

	// Wait for process completion
	err := cmd.Wait()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	rp.mu.Lock()
	rp.state.PID = 0
	rp.state.PGID = 0
	rp.state.ExitCode = exitCode

	if !rp.shouldRun || (exitCode == 0 && rp.svc.RestartPolicy == "no") {
		rp.state.Status = "stopped"
		rp.shouldRun = false
		rp.logs.Append("system", fmt.Sprintf("Process stopped (exit code %d)", exitCode))
		rp.mu.Unlock()
		if s.onStatus != nil {
			s.onStatus(rp.svc.ID, "stopped", 0)
		}
		return
	}

	// Process exited unexpectedly
	rp.state.Status = "crashed"
	rp.logs.Append("system", fmt.Sprintf("Process exited unexpectedly with code %d", exitCode))

	shouldRestart := rp.svc.RestartPolicy == "always" || (rp.svc.RestartPolicy == "on-failure" && exitCode != 0)
	if shouldRestart {
		rp.state.RestartCount++
		rp.logs.Append(
			"system",
			fmt.Sprintf(
				"Restart policy %q triggered; restarting in 1s (attempt %d)...",
				rp.svc.RestartPolicy,
				rp.state.RestartCount,
			),
		)
		rp.mu.Unlock()
		if s.onStatus != nil {
			s.onStatus(rp.svc.ID, "crashed", 0)
		}

		time.Sleep(1 * time.Second)
		rp.mu.RLock()
		desired := rp.shouldRun
		rp.mu.RUnlock()
		if desired {
			_ = s.launchProcess(rp)
		}
	} else {
		rp.mu.Unlock()
		if s.onStatus != nil {
			s.onStatus(rp.svc.ID, "crashed", 0)
		}
	}
}

func captureOutput(r io.Reader, ring *LogRing, stream string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		ring.Append(stream, scanner.Text())
	}
}

// Stop terminates the process group gracefully.
func (s *Supervisor) Stop(id string, timeout time.Duration) error {
	s.mu.RLock()
	rp, exists := s.processes[id]
	s.mu.RUnlock()
	if !exists {
		return storage.ErrNotFound
	}

	rp.mu.Lock()
	rp.shouldRun = false
	if rp.state.Status == "stopped" || rp.cmd == nil || rp.cmd.Process == nil {
		rp.state.Status = "stopped"
		rp.mu.Unlock()
		return nil
	}

	pgid := rp.state.PGID
	rp.logs.Append("system", "Sending SIGTERM to process group...")
	rp.mu.Unlock()

	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	// Send SIGTERM to entire process group (-pgid)
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}

	// Wait up to timeout for process to exit without leaking goroutine
	done := make(chan struct{})
	stopPolling := make(chan struct{})
	defer close(stopPolling)

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopPolling:
				return
			case <-ticker.C:
				rp.mu.RLock()
				st := rp.state.Status
				rp.mu.RUnlock()
				if st == "stopped" || st == "crashed" {
					close(done)
					return
				}
			}
		}
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		// Force kill
		if pgid > 0 {
			rp.logs.Append("system", "Timeout reached; sending SIGKILL to process group")
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		if rp.cancel != nil {
			rp.cancel()
		}
	}

	return nil
}

// StopAll terminates all running managed processes.
func (s *Supervisor) StopAll(timeout time.Duration) {
	s.mu.RLock()
	ids := make([]string, 0, len(s.processes))
	for id := range s.processes {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	for _, id := range ids {
		_ = s.Stop(id, timeout)
	}
}

// Restart stops and restarts the process.
func (s *Supervisor) Restart(ctx context.Context, id string) (*ProcessState, error) {
	s.mu.RLock()
	rp, exists := s.processes[id]
	s.mu.RUnlock()
	if !exists {
		return nil, storage.ErrNotFound
	}

	svc := rp.svc
	_ = s.Stop(id, 3*time.Second)
	return s.Start(ctx, svc)
}

// GetState returns the current process state.
func (s *Supervisor) GetState(id string) (*ProcessState, bool) {
	s.mu.RLock()
	rp, exists := s.processes[id]
	s.mu.RUnlock()
	if !exists {
		return nil, false
	}

	rp.mu.RLock()
	st := rp.state
	rp.mu.RUnlock()
	return &st, true
}

// GetLogs returns recent logs for a service.
func (s *Supervisor) GetLogs(id string, limit int) ([]LogEntry, error) {
	s.mu.RLock()
	rp, exists := s.processes[id]
	s.mu.RUnlock()
	if !exists {
		return nil, storage.ErrNotFound
	}

	return rp.logs.GetRecent(limit), nil
}

// FollowLogs subscribes to live logs for a service.
func (s *Supervisor) FollowLogs(id string) (<-chan LogEntry, func(), error) {
	s.mu.RLock()
	rp, exists := s.processes[id]
	s.mu.RUnlock()
	if !exists {
		return nil, nil, storage.ErrNotFound
	}

	ch, unsub := rp.logs.Subscribe()
	return ch, unsub, nil
}
