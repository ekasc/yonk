package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/workspace"
)

var errNotRunning = errors.New("job is not running")

// Restricted is a milestone-only executor that permits /bin/echo and nothing
// else. It is intentionally not a sandbox and must be replaced before general
// commands or untrusted clients are accepted.
type Restricted struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewRestricted creates the deliberately constrained milestone executor.
func NewRestricted() *Restricted {
	return &Restricted{running: make(map[string]context.CancelFunc)}
}

// Capabilities reports the host platform because this temporary executor runs
// the allowlisted command on the host.
func (e *Restricted) Capabilities(context.Context) ([]job.ExecutorCapability, error) {
	return []job.ExecutorCapability{{
		Platform:  job.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Isolation: "restricted-host-process",
	}}, nil
}

// Run executes /bin/echo without invoking a shell.
func (e *Restricted) Run(ctx context.Context, spec job.Job, work workspace.Workspace, stdout, stderr io.Writer) (job.Result, error) {
	started := time.Now().UTC()
	result := job.Result{StartedAt: started, TerminationReason: "executor_error"}

	if spec.Command != "echo" {
		return result, fmt.Errorf("milestone executor rejects command %q: only echo is allowed", spec.Command)
	}

	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
	if !e.register(spec.ID, cancel) {
		cancel()
		return result, fmt.Errorf("job %q is already running", spec.ID)
	}
	defer func() {
		cancel()
		e.unregister(spec.ID)
	}()

	cmd := exec.CommandContext(jobCtx, "/bin/echo", spec.Args...)
	cmd.Dir = work.Root
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	ended := time.Now().UTC()
	result.EndedAt = ended
	result.DurationMillis = ended.Sub(started).Milliseconds()
	result.ExitCode = 0
	result.TerminationReason = "completed"
	if cmd.ProcessState != nil {
		result.CPUTimeMillis = cmd.ProcessState.UserTime().Milliseconds() + cmd.ProcessState.SystemTime().Milliseconds()
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			result.ExitCode = code
		}
	}

	if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
		result.ExitCode = 124
		result.TerminationReason = "timeout"
		return result, nil
	}
	if errors.Is(jobCtx.Err(), context.Canceled) {
		result.ExitCode = 130
		result.TerminationReason = "cancelled"
		return result, nil
	}
	if err != nil {
		result.TerminationReason = "command_failed"
		return result, fmt.Errorf("run command: %w", err)
	}
	return result, nil
}

// Kill cancels a running job.
func (e *Restricted) Kill(ctx context.Context, jobID string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("kill job: %w", ctx.Err())
	default:
	}

	e.mu.Lock()
	cancel, ok := e.running[jobID]
	e.mu.Unlock()
	if !ok {
		return errNotRunning
	}
	cancel()
	return nil
}

func (e *Restricted) register(jobID string, cancel context.CancelFunc) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.running[jobID]; exists {
		return false
	}
	e.running[jobID] = cancel
	return true
}

func (e *Restricted) unregister(jobID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.running, jobID)
}
