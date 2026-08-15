// Package guest implements the agent that runs inside the Firecracker
// microVM: init duties, vsock control, workspace mount, and job execution.
package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/ekassinghchhabra/yonk/internal/guestproto"
	"github.com/ekassinghchhabra/yonk/internal/job"
)

const (
	workspaceDevice = "/dev/vdb"
	workspaceMount  = "/workspace"
)

// Run performs init duties, connects to the host, and runs the submitted job.
// The agent must be the first process (PID 1) of the initramfs.
func Run(log io.Writer) error {
	logSink := &teeLog{console: log}
	if err := mountBasics(); err != nil {
		return err
	}
	fmt.Fprintf(logSink, "yonk-guest: mounts ready\n")
	if err := enablePidsLimit(); err != nil {
		fmt.Fprintf(logSink, "yonk-guest: pids limit unavailable: %v\n", err)
	}
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	if err := os.MkdirAll("/tmp/home", 0o700); err != nil {
		return fmt.Errorf("create /tmp/home: %w", err)
	}
	os.Setenv("HOME", "/tmp/home")

	conn, err := dialHost()
	if err != nil {
		return fmt.Errorf("connect to host: %w", err)
	}
	defer conn.Close()
	enc := newLockedEncoder(conn)
	logSink.enc = enc
	fmt.Fprintf(logSink, "yonk-guest: vsock connected\n")

	dec := json.NewDecoder(conn)
	if err := enc.Encode(guestproto.Message{Type: guestproto.MsgHello}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	var msg guestproto.Message
	if err := dec.Decode(&msg); err != nil {
		return fmt.Errorf("receive job: %w", err)
	}
	if msg.Type != guestproto.MsgJob || msg.Job == nil {
		return fmt.Errorf("expected job message, got %q", msg.Type)
	}

	if msg.Job.Network == "egress" {
		if err := setupResolvConf(); err != nil {
			fmt.Fprintf(logSink, "yonk-guest: resolv.conf setup failed: %v\n", err)
		}
	}

	if err := mountWorkspace(); err != nil {
		sendResult(enc, guestproto.Result{
			ExitCode:          137,
			TerminationReason: "executor_error",
			Message:           err.Error(),
		})
		return err
	}
	defer func() { _ = unix.Unmount(workspaceMount, 0) }()

	return runJob(msg.Job, enc, dec, logSink)
}

func dialHost() (*vsock.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		conn, err := vsock.Dial(guestproto.HostCID, guestproto.HostPort, nil)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return nil, lastErr
}

func mountBasics() error {
	return mountBasicsPlatform()
}

func mountWorkspace() error {
	return mountWorkspacePlatform()
}

func runJob(spec *job.Job, enc *lockedEncoder, dec *json.Decoder, log io.Writer) error {
	start := time.Now().UTC()
	result := guestproto.Result{TerminationReason: "executor_error"}

	if err := os.Chdir(workspaceMount); err != nil {
		result.Message = err.Error()
		sendResult(enc, result)
		return err
	}
	commandPath, err := exec.LookPath(spec.Command)
	if err != nil {
		result.ExitCode = 127
		result.TerminationReason = "command_not_found"
		result.Message = fmt.Sprintf("%s: %v", spec.Command, err)
		sendResult(enc, result)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(spec.TimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, commandPath, spec.Args...)
	cmd.Dir = workspaceMount
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/tmp/home",
		"TERM=xterm",
		"LANG=C.UTF-8",
		"PWD=" + workspaceMount,
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		result.ExitCode = 126
		result.Message = err.Error()
		sendResult(enc, result)
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		result.ExitCode = 126
		result.Message = err.Error()
		sendResult(enc, result)
		return err
	}
	if err := cmd.Start(); err != nil {
		result.ExitCode = 126
		result.TerminationReason = "command_failed"
		result.Message = err.Error()
		sendResult(enc, result)
		return nil
	}

	var peakBytes atomic.Int64
	stopSampler := make(chan struct{})
	go samplePeakMemory(&peakBytes, cmd.Process.Pid, stopSampler)

	cancelCh := make(chan struct{})
	go watchCancellation(dec, cancelCh)

	killGroup := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	go func() {
		select {
		case <-cancelCh:
			killGroup()
		case <-ctx.Done():
			killGroup()
		}
	}()

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(guestEventWriter{enc: enc, eventType: guestproto.MsgStdout}, stdoutPipe)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(guestEventWriter{enc: enc, eventType: guestproto.MsgStderr}, stderrPipe)
	}()

	runErr := cmd.Wait()
	close(stopSampler)
	copyWG.Wait()

	// A shell may exit while its background children keep running (a fork
	// bomb does exactly this). Reap orphans and bound them by the job
	// timeout so completion is never reported while work is still alive.
	reapOrphans(ctx, killGroup)

	result.DurationMillis = time.Since(start).Milliseconds()
	result.PeakMemoryBytes = peakBytes.Load()
	if cmd.ProcessState != nil {
		result.CPUTimeMillis = cmd.ProcessState.UserTime().Milliseconds() + cmd.ProcessState.SystemTime().Milliseconds()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.ExitCode = 128 + int(status.Signal())
		} else if code := cmd.ProcessState.ExitCode(); code >= 0 {
			result.ExitCode = code
		}
	}
	switch {
	case isCancelled(cancelCh):
		result.ExitCode = 130
		result.TerminationReason = "cancelled"
	case ctx.Err() == context.DeadlineExceeded:
		result.ExitCode = 124
		result.TerminationReason = "timeout"
	case cmd.ProcessState != nil:
		result.TerminationReason = "completed"
	default:
		result.ExitCode = 126
		result.TerminationReason = "command_failed"
		result.Message = fmt.Sprint(runErr)
	}
	fmt.Fprintf(log, "yonk-guest: job finished reason=%s code=%d\n", result.TerminationReason, result.ExitCode)
	sendResult(enc, result)
	fmt.Fprintf(log, "yonk-guest: result sent\n")
	return nil
}

func watchCancellation(dec *json.Decoder, cancelCh chan struct{}) {
	for {
		var msg guestproto.Message
		if err := dec.Decode(&msg); err != nil {
			return
		}
		if msg.Type == guestproto.MsgCancel {
			close(cancelCh)
			return
		}
	}
}

func isCancelled(cancelCh chan struct{}) bool {
	select {
	case <-cancelCh:
		return true
	default:
		return false
	}
}

// reapOrphans waits for the job's remaining children. At the deadline it
// kills the job's process group and drains every child before returning.
func reapOrphans(ctx context.Context, killGroup func()) {
	for {
		select {
		case <-ctx.Done():
			killGroup()
			for {
				if _, err := unix.Wait4(-1, nil, 0, nil); err != nil {
					return
				}
			}
		default:
		}
		pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
		if errors.Is(err, unix.ECHILD) {
			return
		}
		if pid == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
	}
}

func sendResult(enc *lockedEncoder, result guestproto.Result) {
	_ = enc.Encode(guestproto.Message{Type: guestproto.MsgResult, Result: &result})
}

// lockedEncoder serializes every write to the vsock connection so concurrent
// event streams and the final result cannot interleave on the wire.
type lockedEncoder struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newLockedEncoder(w io.Writer) *lockedEncoder {
	return &lockedEncoder{enc: json.NewEncoder(w)}
}

func (l *lockedEncoder) Encode(v any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enc.Encode(v)
}

// guestEventWriter streams command output as guest protocol messages.
type guestEventWriter struct {
	enc       *lockedEncoder
	eventType string
}

func (w guestEventWriter) Write(p []byte) (int, error) {
	if err := w.enc.Encode(guestproto.Message{Type: w.eventType, Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// teeLog writes diagnostics to the serial console and, once the vsock
// connection exists, also forwards them to the host as log messages.
type teeLog struct {
	mu      sync.Mutex
	console io.Writer
	enc     *lockedEncoder
}

func (t *teeLog) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.console != nil {
		_, _ = t.console.Write(p)
	}
	if t.enc != nil {
		_ = t.enc.Encode(guestproto.Message{Type: guestproto.MsgLog, Data: p})
	}
	return len(p), nil
}
