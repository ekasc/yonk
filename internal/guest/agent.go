// Package guest implements the agent that runs inside the Firecracker
// microVM: init duties, vsock control, workspace mount, and job execution.
package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/ekassinghchhabra/yonk/internal/guestproto"
	"github.com/ekassinghchhabra/yonk/internal/job"
)

const (
	workspaceDevice = "/dev/vda"
	workspaceMount  = "/workspace"
)

// Run performs init duties, connects to the host, and runs the submitted job.
// The agent must be the first process (PID 1) of the initramfs.
func Run(log io.Writer) error {
	if err := mountBasics(); err != nil {
		return err
	}
	fmt.Fprintf(log, "yonk-guest: mounts ready\n")
	os.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
	if err := os.MkdirAll("/root", 0o700); err != nil {
		return fmt.Errorf("create /root: %w", err)
	}

	conn, err := dialHost()
	if err != nil {
		return fmt.Errorf("connect to host: %w", err)
	}
	defer conn.Close()
	fmt.Fprintf(log, "yonk-guest: vsock connected\n")

	enc := newLockedEncoder(conn)
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

	if err := mountWorkspace(); err != nil {
		sendResult(enc, guestproto.Result{
			ExitCode:          137,
			TerminationReason: "executor_error",
			Message:           err.Error(),
		})
		return err
	}
	defer func() { _ = unix.Unmount(workspaceMount, 0) }()

	return runJob(msg.Job, enc, dec, log)
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
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"TERM=xterm",
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
	copyWG.Wait()

	result.DurationMillis = time.Since(start).Milliseconds()
	if cmd.ProcessState != nil {
		result.CPUTimeMillis = cmd.ProcessState.UserTime().Milliseconds() + cmd.ProcessState.SystemTime().Milliseconds()
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
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
