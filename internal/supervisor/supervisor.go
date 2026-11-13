package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/terastal-hq/prot/internal/netutil"
	"github.com/terastal-hq/prot/internal/storage"
)

// Timing knobs for process supervision.
const (
	defaultStopTimeout = 3 * time.Second
	killGrace          = 2 * time.Second // how long Stop waits for the process to vanish after SIGKILL
	readyTimeout       = 10 * time.Second
	pipeDrainDelay     = 2 * time.Second // how long Wait lingers when a grandchild keeps the output pipes open
	restartBaseDelay   = time.Second
	restartMaxDelay    = 30 * time.Second
	stableRunTime      = 10 * time.Second // a run this long resets the crash-loop backoff
)

// ProcessState describes the live execution state of a supervised process.
type ProcessState struct {
	ID           string     `json:"id"`
	PID          int        `json:"pid"`
	PGID         int        `json:"pgid"`
	Status       string     `json:"status"` // one of the storage.Status* constants
	StartedAt    *time.Time `json:"started_at,omitempty"`
	AssignedPort uint32     `json:"assigned_port"`
	ExitCode     int        `json:"exit_code"`
	RestartCount int        `json:"restart_count"`
}

// run is a single launch of a service's process. done is closed once the process has exited and the
// supervisor has recorded its final state, so waiting on it observes the outcome.
type run struct {
	gen            uint64
	cmd            *exec.Cmd
	cancel         context.CancelFunc
	pgid           int
	started        time.Time
	stdout, stderr *lineWriter
	done           chan struct{}
}

type runningProcess struct {
	mu           sync.RWMutex
	svc          storage.Service
	state        ProcessState
	logs         *LogRing
	shouldRun    bool
	current      *run // nil while no process is alive
	gen          uint64
	failStreak   int
	restartTimer *time.Timer
}

// Supervisor coordinates starting, stopping, and logging child processes.
type Supervisor struct {
	mu        sync.RWMutex
	processes map[string]*runningProcess
	onStatus  func(serviceID, status string, port uint32)

	restartBase time.Duration // overridable in tests
}

// New constructs a Supervisor. onStatus, if set, is called (without any supervisor lock held) whenever a
// process changes state.
func New(onStatus func(serviceID, status string, port uint32)) *Supervisor {
	return &Supervisor{
		processes:   make(map[string]*runningProcess),
		onStatus:    onStatus,
		restartBase: restartBaseDelay,
	}
}

func (s *Supervisor) notify(id, status string, port uint32) {
	if s.onStatus != nil {
		s.onStatus(id, status, port)
	}
}

func (s *Supervisor) lookup(id string) (*runningProcess, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rp, ok := s.processes[id]
	return rp, ok
}

func (s *Supervisor) processFor(svc storage.Service) *runningProcess {
	s.mu.Lock()
	defer s.mu.Unlock()

	rp, ok := s.processes[svc.ID]
	if !ok {
		rp = &runningProcess{
			svc:   svc,
			logs:  NewLogRing(2000),
			state: ProcessState{ID: svc.ID, Status: storage.StatusStopped},
		}
		s.processes[svc.ID] = rp
	}
	return rp
}

// Start spawns a managed service child process. Starting a service that is already starting or running
// returns its current state.
func (s *Supervisor) Start(_ context.Context, svc storage.Service) (*ProcessState, error) {
	rp := s.processFor(svc)

	rp.mu.Lock()
	if rp.state.Status == storage.StatusRunning || rp.state.Status == storage.StatusStarting {
		st := rp.state
		rp.mu.Unlock()
		return &st, nil
	}

	rp.stopRestartTimerLocked() // a manual start supersedes a pending automatic restart
	rp.shouldRun = true
	rp.svc = svc
	rp.failStreak = 0
	rp.state.ExitCode = 0

	err := s.launchLocked(rp)
	if err != nil {
		rp.state.Status = storage.StatusCrashed
		rp.shouldRun = false
		rp.logs.Append(StreamSystem, "Failed to start: "+err.Error())
	}
	st := rp.state
	rp.mu.Unlock()

	if err != nil {
		s.notify(svc.ID, storage.StatusCrashed, 0)
		return &st, err
	}
	return &st, nil
}

// launchLocked starts the process for rp.svc. The caller must hold rp.mu.
func (s *Supervisor) launchLocked(rp *runningProcess) error {
	svc := rp.svc
	if strings.TrimSpace(svc.Command) == "" {
		return errors.New("command cannot be empty")
	}

	port := svc.Port
	if svc.AutoPort {
		free, err := netutil.FreePort()
		if err != nil {
			return fmt.Errorf("allocating auto port: %w", err)
		}
		port = free
	}

	script := shellCommand(svc.Command, svc.Args)
	ctx, cancel := context.WithCancel(context.Background())

	// Commands run through the shell so pipelines and flags work. Process groups let us signal the whole tree.
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = svc.Cwd
	cmd.Env = processEnv(svc, port)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Writers (not StdoutPipe) so Cmd.Wait drains all output before returning: the last lines of a process that
	// exits or crashes are the ones that matter most.
	stdout := newLineWriter(rp.logs, StreamStdout)
	stderr := newLineWriter(rp.logs, StreamStderr)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = pipeDrainDelay
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, syscall.SIGKILL) }

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start process: %w", err)
	}

	now := time.Now().UTC()
	pid := cmd.Process.Pid
	rp.gen++
	r := &run{
		gen: rp.gen, cmd: cmd, cancel: cancel, pgid: pid, started: now,
		stdout: stdout, stderr: stderr, done: make(chan struct{}),
	}
	rp.current = r
	rp.state.PID = pid
	rp.state.PGID = pid
	rp.state.StartedAt = &now
	rp.state.AssignedPort = port
	rp.state.Status = storage.StatusStarting
	rp.logs.Append(StreamSystem, fmt.Sprintf("Process started with PID %d (command: %s)", pid, script))

	go s.supervise(rp, r)
	go s.awaitReady(ctx, rp, r, port)
	return nil
}

// awaitReady marks the run as running once its port accepts connections (immediately when no port is known).
func (s *Supervisor) awaitReady(ctx context.Context, rp *runningProcess, r *run, port uint32) {
	if port > 0 && !netutil.WaitForPort(ctx, port, readyTimeout) {
		if ctx.Err() == nil {
			rp.logs.Append(StreamSystem, fmt.Sprintf(
				"Port %d did not open on 127.0.0.1 or ::1 within %s; "+
					"check that the command listens on this port and on localhost", port, readyTimeout))
		}
		return
	}

	rp.mu.Lock()
	if rp.current != r || rp.state.Status != storage.StatusStarting {
		rp.mu.Unlock()
		return
	}
	rp.state.Status = storage.StatusRunning
	id := rp.svc.ID
	if port > 0 {
		rp.logs.Append(StreamSystem, fmt.Sprintf("Service is ready and listening on port %d", port))
	}
	rp.mu.Unlock()

	s.notify(id, storage.StatusRunning, port)
}

// supervise waits for a run to exit, records the outcome and schedules a restart if the policy asks for one.
func (s *Supervisor) supervise(rp *runningProcess, r *run) {
	err := r.cmd.Wait()
	r.stdout.Flush()
	r.stderr.Flush()
	r.cancel()
	exitCode := exitCodeOf(r.cmd, err)

	rp.mu.Lock()
	id := rp.svc.ID
	policy := rp.svc.RestartPolicy
	rp.current = nil
	rp.state.PID = 0
	rp.state.PGID = 0
	rp.state.ExitCode = exitCode

	var status string
	switch {
	case !rp.shouldRun:
		status = storage.StatusStopped
		rp.logs.Append(StreamSystem, fmt.Sprintf("Process stopped (exit code %d)", exitCode))
	case exitCode == 0 && policy != storage.RestartAlways:
		// A clean exit that the policy does not ask us to repeat is a finished job, not a crash.
		status = storage.StatusStopped
		rp.shouldRun = false
		rp.logs.Append(StreamSystem, "Process exited cleanly (exit code 0)")
	default:
		status = storage.StatusCrashed
		rp.logs.Append(StreamSystem, fmt.Sprintf("Process exited unexpectedly with code %d", exitCode))
		if policy == storage.RestartAlways || policy == storage.RestartOnFailure {
			if time.Since(r.started) >= stableRunTime {
				rp.failStreak = 0
			}
			s.scheduleRestartLocked(rp, r.gen)
		} else {
			rp.shouldRun = false
		}
	}
	rp.state.Status = status
	rp.mu.Unlock()

	close(r.done) // after the final state is recorded, so Stop observes the outcome
	s.notify(id, status, 0)
}

// scheduleRestartLocked arms the restart timer with exponential backoff. The caller must hold rp.mu.
func (s *Supervisor) scheduleRestartLocked(rp *runningProcess, gen uint64) {
	rp.failStreak++
	rp.state.RestartCount++
	delay := s.restartDelay(rp.failStreak)
	rp.logs.Append(StreamSystem, fmt.Sprintf(
		"Restart policy %q triggered; restarting in %s (attempt %d)...",
		rp.svc.RestartPolicy, delay, rp.state.RestartCount,
	))
	rp.restartTimer = time.AfterFunc(delay, func() { s.restart(rp, gen) })
}

func (s *Supervisor) restartDelay(streak int) time.Duration {
	delay := s.restartBase
	for i := 1; i < streak && delay < restartMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, restartMaxDelay)
}

// restart relaunches a crashed process when its backoff timer fires, unless the process was stopped, started
// manually or already replaced in the meantime. A failed relaunch is logged and retried with more backoff.
func (s *Supervisor) restart(rp *runningProcess, gen uint64) {
	rp.mu.Lock()
	if !rp.shouldRun || rp.gen != gen || rp.current != nil {
		rp.mu.Unlock()
		return
	}
	rp.restartTimer = nil

	if err := s.launchLocked(rp); err != nil {
		rp.logs.Append(StreamSystem, "Restart failed: "+err.Error())
		s.scheduleRestartLocked(rp, gen)
	}
	rp.mu.Unlock()
}

func (rp *runningProcess) stopRestartTimerLocked() {
	if rp.restartTimer != nil {
		rp.restartTimer.Stop()
		rp.restartTimer = nil
	}
}

// Stop terminates the process group: SIGTERM first, SIGKILL after timeout. It returns once the process has
// exited, so callers can safely start it again.
func (s *Supervisor) Stop(id string, timeout time.Duration) error {
	rp, ok := s.lookup(id)
	if !ok {
		return storage.ErrNotFound
	}
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}

	rp.mu.Lock()
	rp.shouldRun = false
	rp.stopRestartTimerLocked()
	r := rp.current
	if r == nil {
		rp.state.Status = storage.StatusStopped
		rp.mu.Unlock()
		return nil
	}
	rp.logs.Append(StreamSystem, "Sending SIGTERM to process group...")
	rp.mu.Unlock()

	_ = signalGroup(r.pgid, syscall.SIGTERM)
	select {
	case <-r.done:
		return nil
	case <-time.After(timeout):
	}

	rp.logs.Append(StreamSystem, "Timeout reached; sending SIGKILL to process group")
	_ = signalGroup(r.pgid, syscall.SIGKILL)
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-time.After(killGrace):
		return fmt.Errorf("process group %d did not exit after SIGKILL", r.pgid)
	}
}

// StopAll terminates all running managed processes in parallel.
func (s *Supervisor) StopAll(timeout time.Duration) {
	s.mu.RLock()
	ids := make([]string, 0, len(s.processes))
	for id := range s.processes {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Stop(id, timeout)
		}()
	}
	wg.Wait()
}

// Restart stops and restarts the process.
func (s *Supervisor) Restart(ctx context.Context, id string) (*ProcessState, error) {
	rp, ok := s.lookup(id)
	if !ok {
		return nil, storage.ErrNotFound
	}

	rp.mu.RLock()
	svc := rp.svc
	rp.mu.RUnlock()

	if err := s.Stop(id, defaultStopTimeout); err != nil {
		return nil, fmt.Errorf("stopping before restart: %w", err)
	}
	return s.Start(ctx, svc)
}

// GetState returns a snapshot of the current process state.
func (s *Supervisor) GetState(id string) (*ProcessState, bool) {
	rp, ok := s.lookup(id)
	if !ok {
		return nil, false
	}

	rp.mu.RLock()
	st := rp.state
	rp.mu.RUnlock()
	return &st, true
}

// GetLogs returns recent logs for a service.
func (s *Supervisor) GetLogs(id string, limit int) ([]LogEntry, error) {
	rp, ok := s.lookup(id)
	if !ok {
		return nil, storage.ErrNotFound
	}
	return rp.logs.GetRecent(limit), nil
}

// FollowLogsWithHistory returns the last limit log lines plus a live subscription, without gaps or duplicates.
func (s *Supervisor) FollowLogsWithHistory(id string, limit int) ([]LogEntry, <-chan LogEntry, func(), error) {
	rp, ok := s.lookup(id)
	if !ok {
		return nil, nil, nil, storage.ErrNotFound
	}

	history, ch, unsub := rp.logs.SubscribeWithHistory(limit)
	return history, ch, unsub, nil
}

// FollowLogs subscribes to live logs for a service.
func (s *Supervisor) FollowLogs(id string) (<-chan LogEntry, func(), error) {
	rp, ok := s.lookup(id)
	if !ok {
		return nil, nil, storage.ErrNotFound
	}

	ch, unsub := rp.logs.Subscribe()
	return ch, unsub, nil
}

func processEnv(svc storage.Service, port uint32) []string {
	env := os.Environ()
	for k, v := range svc.Env {
		env = append(env, k+"="+v)
	}
	if port > 0 {
		env = append(env, fmt.Sprintf("PORT=%d", port))
	}
	return env
}

// shellCommand appends args to command, quoting each so spaces and shell metacharacters stay literal.
func shellCommand(command string, args []string) string {
	if len(args) == 0 {
		return command
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return command + " " + strings.Join(quoted, " ")
}

// signalGroup sends sig to every process in the group led by pgid. A group that is already gone is not an error.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func exitCodeOf(cmd *exec.Cmd, waitErr error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode() // -1 when killed by a signal
	}
	if waitErr != nil {
		return -1
	}
	return 0
}
