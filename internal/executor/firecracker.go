package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/ekassinghchhabra/yonk/internal/firecracker"
	"github.com/ekassinghchhabra/yonk/internal/guestproto"
	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/workspace"
)

const (
	bootGrace       = 45 * time.Second
	teardownGrace   = 10 * time.Second
	killGrace       = 3 * time.Second
	maxVMLogBytes   = 64 << 10
	guestReplyGrace = 5 * time.Second
)

// FirecrackerConfig configures the Firecracker executor. All paths except
// WorkDir are validated at startup; WorkDir must allow Unix sockets.
type FirecrackerConfig struct {
	BinPath        string // firecracker binary
	KernelPath     string // guest kernel image (vmlinux)
	GuestAgentPath string // static yonk-guest binary
	BusyboxPath    string // static busybox binary
	WorkDir        string // per-job VM state directory
	MaxVCPU        int    // provider ceiling per guest
	MaxMemoryMB    int    // provider ceiling per guest (MiB)
}

// Firecracker executes each job inside an ephemeral Firecracker microVM.
type Firecracker struct {
	cfg     FirecrackerConfig
	logger  *slog.Logger
	mkfs    func(root, image string, sizeBytes int64) error
	applets func() ([]string, error)

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

var minimalApplets = []string{
	"sh", "echo", "ls", "cat", "rm", "mkdir", "cp", "mv",
	"grep", "sleep", "true", "false", "mount", "umount",
}

// NewFirecracker validates the environment and returns a ready executor.
func NewFirecracker(cfg FirecrackerConfig, logger *slog.Logger) (*Firecracker, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = os.TempDir()
	}
	if cfg.MaxVCPU == 0 {
		cfg.MaxVCPU = 4
	}
	if cfg.MaxMemoryMB == 0 {
		cfg.MaxMemoryMB = 4096
	}
	f := &Firecracker{
		cfg:     cfg,
		logger:  logger,
		mkfs:    firecracker.MkfsExt4,
		applets: func() ([]string, error) { return listApplets(cfg.BusyboxPath) },
		running: map[string]context.CancelFunc{},
	}
	if err := f.preflight(); err != nil {
		return nil, err
	}
	return f, nil
}

func listApplets(busyboxPath string) ([]string, error) {
	out, err := exec.Command(busyboxPath, "--list").Output()
	if err != nil {
		return append([]string(nil), minimalApplets...), nil
	}
	var applets []string
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if name := string(line); name != "" && name != "busybox" {
			applets = append(applets, name)
		}
	}
	if len(applets) == 0 {
		return append([]string(nil), minimalApplets...), nil
	}
	return applets, nil
}

func (f *Firecracker) preflight() error {
	for _, path := range []string{f.cfg.BinPath, f.cfg.KernelPath, f.cfg.GuestAgentPath, f.cfg.BusyboxPath} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("microvm asset %q: %w", path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("microvm asset %q is a directory", path)
		}
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("microvm executor requires KVM: %w", err)
	}
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return fmt.Errorf("microvm executor requires mkfs.ext4 (e2fsprogs): %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, f.cfg.BinPath, "--version").Output(); err != nil {
		return fmt.Errorf("run %q --version: %w: %s", f.cfg.BinPath, err, out)
	}
	return nil
}

// Capabilities reports microVM execution for linux/amd64 hosts.
func (f *Firecracker) Capabilities(context.Context) ([]job.ExecutorCapability, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, errors.New("firecracker executor requires linux/amd64")
	}
	return []job.ExecutorCapability{{
		Platform:  job.Platform{OS: "linux", Arch: "amd64"},
		Isolation: "microvm",
	}}, nil
}

// Run boots a microVM, transfers the workspace, executes the job inside the
// guest, and tears everything down.
func (f *Firecracker) Run(ctx context.Context, spec job.Job, work workspace.Workspace, stdout, stderr io.Writer) (job.Result, error) {
	started := time.Now().UTC()
	result := job.Result{StartedAt: started, TerminationReason: "executor_error"}
	if work.Root == "" {
		return result, errors.New("workspace root is required")
	}

	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second+bootGrace+teardownGrace)
	if !f.register(spec.ID, cancel) {
		cancel()
		return result, fmt.Errorf("job %q is already running", spec.ID)
	}
	defer func() {
		cancel()
		f.unregister(spec.ID)
	}()

	jobDir, err := os.MkdirTemp(f.cfg.WorkDir, "vm-")
	if err != nil {
		return result, fmt.Errorf("create VM state directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(jobDir) }()

	workspaceImage := filepath.Join(jobDir, "workspace.ext4")
	imageBytes := workspaceImageSize(work.Stats.UncompressedBytes, int64(spec.Resources.DiskMB))
	if err := f.mkfs(work.Root, workspaceImage, imageBytes); err != nil {
		return result, fmt.Errorf("prepare workspace disk: %w", err)
	}

	applets, err := f.applets()
	if err != nil {
		return result, fmt.Errorf("enumerate busybox applets: %w", err)
	}
	initramfsPath := filepath.Join(jobDir, "initramfs.cpio")
	if err := firecracker.BuildInitramfs(f.cfg.GuestAgentPath, f.cfg.BusyboxPath, applets, initramfsPath); err != nil {
		return result, fmt.Errorf("build initramfs: %w", err)
	}

	vsockPath := filepath.Join(jobDir, "v")
	cfg := firecracker.Config{
		BootSource: firecracker.BootSource{
			KernelImagePath: f.cfg.KernelPath,
			InitrdPath:      initramfsPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
		},
		Drives: []firecracker.Drive{{
			DriveID:      "workspace",
			PathOnHost:   workspaceImage,
			IsRootDevice: false,
			IsReadOnly:   false,
		}},
		MachineConfig: firecracker.MachineConfig{
			VCPUCount:  clampInt(spec.Resources.CPU, 1, f.cfg.MaxVCPU),
			MemSizeMiB: clampInt(spec.Resources.MemoryMB, 128, f.cfg.MaxMemoryMB),
			SMT:        false,
		},
		VSock: firecracker.VSock{GuestCID: guestproto.GuestCID, UDSPath: vsockPath},
		Serial: &firecracker.Serial{
			SerialOutPath: filepath.Join(jobDir, "console.log"),
		},
	}
	cfgPath := filepath.Join(jobDir, "config.json")
	if err := firecracker.WriteConfig(cfgPath, cfg); err != nil {
		return result, err
	}

	// Guest-initiated connections arrive on {uds_path}_{port}; listen before
	// the guest boots so the agent's dial never races the host.
	listener, err := net.Listen("unix", fmt.Sprintf("%s_%d", vsockPath, guestproto.HostPort))
	if err != nil {
		return result, fmt.Errorf("open vsock listener: %w", err)
	}
	defer listener.Close()

	cmd := exec.CommandContext(jobCtx, f.cfg.BinPath,
		"--api-sock", filepath.Join(jobDir, "api.sock"),
		"--config-file", cfgPath)
	vmLog := &syncBuffer{}
	cmd.Stdout = vmLog
	cmd.Stderr = vmLog
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start firecracker: %w", err)
	}
	processExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(processExited)
	}()

	conn, err := f.acceptGuest(jobCtx, cmd, listener, processExited, vmLog)
	if conn == nil {
		return finishEarly(result, started, err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	if err := enc.Encode(guestproto.Message{Type: guestproto.MsgJob, Job: &spec}); err != nil {
		return result, fmt.Errorf("send job to guest: %w", err)
	}

	readDone := make(chan struct{})
	go func() {
		select {
		case <-jobCtx.Done():
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = enc.Encode(guestproto.Message{Type: guestproto.MsgCancel})
			select {
			case <-readDone:
			case <-time.After(guestReplyGrace):
				_ = conn.Close()
			}
		case <-processExited:
		}
	}()

	result, runErr := f.readGuest(jobCtx, conn, spec, stdout, stderr, result, started)
	close(readDone)
	killProcess(cmd, processExited, killGrace)
	return result, runErr
}

func (f *Firecracker) acceptGuest(jobCtx context.Context, cmd *exec.Cmd, listener net.Listener, processExited <-chan struct{}, vmLog *syncBuffer) (net.Conn, error) {
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		acceptCh <- acceptResult{conn, err}
	}()

	select {
	case ar := <-acceptCh:
		if ar.err != nil {
			if jobCtx.Err() != nil {
				return nil, jobCtx.Err()
			}
			return nil, fmt.Errorf("accept guest connection: %w", ar.err)
		}
		return ar.conn, nil
	case <-time.After(bootGrace):
		killProcess(cmd, processExited, killGrace)
		return nil, errors.New("guest did not connect within the startup grace period")
	case <-processExited:
		return nil, fmt.Errorf("firecracker exited before the guest connected: %s", vmLog.String())
	case <-jobCtx.Done():
		killProcess(cmd, processExited, killGrace)
		return nil, jobCtx.Err()
	}
}

func (f *Firecracker) readGuest(jobCtx context.Context, conn net.Conn, spec job.Job, stdout, stderr io.Writer, result job.Result, started time.Time) (job.Result, error) {
	dec := json.NewDecoder(conn)
	for {
		var msg guestproto.Message
		if err := dec.Decode(&msg); err != nil {
			if ctxErr := jobCtx.Err(); ctxErr != nil {
				code, reason := classifyContext(ctxErr)
				result.ExitCode = code
				result.TerminationReason = reason
				result.EndedAt = time.Now().UTC()
				result.DurationMillis = result.EndedAt.Sub(started).Milliseconds()
				return result, nil
			}
			result.ExitCode = 137
			result.TerminationReason = "guest_crash"
			result.EndedAt = time.Now().UTC()
			result.DurationMillis = result.EndedAt.Sub(started).Milliseconds()
			return result, fmt.Errorf("guest closed the connection without a result: %w", err)
		}
		switch msg.Type {
		case guestproto.MsgStdout:
			if _, err := stdout.Write(msg.Data); err != nil {
				return result, fmt.Errorf("write guest stdout: %w", err)
			}
		case guestproto.MsgStderr:
			if _, err := stderr.Write(msg.Data); err != nil {
				return result, fmt.Errorf("write guest stderr: %w", err)
			}
		case guestproto.MsgResult:
			if msg.Result == nil {
				return result, errors.New("guest sent a result message without a result")
			}
			result.ExitCode = msg.Result.ExitCode
			result.TerminationReason = msg.Result.TerminationReason
			result.CPUTimeMillis = msg.Result.CPUTimeMillis
			result.DurationMillis = time.Since(started).Milliseconds()
			if msg.Result.Message != "" {
				f.logger.Warn("guest reported a problem", "job_id", spec.ID, "message", msg.Result.Message)
			}
			result.EndedAt = time.Now().UTC()
			return result, nil
		case guestproto.MsgHello:
			f.logger.Debug("guest connected", "job_id", spec.ID)
		case guestproto.MsgLog:
			f.logger.Debug("guest log", "job_id", spec.ID, "message", string(msg.Data))
		default:
			f.logger.Warn("unknown guest message", "job_id", spec.ID, "type", msg.Type)
		}
	}
}

func finishEarly(result job.Result, started time.Time, err error) (job.Result, error) {
	if err == nil {
		return result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code, reason := classifyContext(err)
		result.ExitCode = code
		result.TerminationReason = reason
		result.EndedAt = time.Now().UTC()
		result.DurationMillis = result.EndedAt.Sub(started).Milliseconds()
		return result, nil
	}
	result.EndedAt = time.Now().UTC()
	result.DurationMillis = result.EndedAt.Sub(started).Milliseconds()
	return result, err
}

func classifyContext(err error) (int, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return 124, "timeout"
	}
	return 130, "cancelled"
}

func killProcess(cmd *exec.Cmd, exited <-chan struct{}, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exited:
		return
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-exited
	}
}

func (f *Firecracker) register(jobID string, cancel context.CancelFunc) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.running[jobID]; exists {
		return false
	}
	f.running[jobID] = cancel
	return true
}

func (f *Firecracker) unregister(jobID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, jobID)
}

// Kill cancels a running job, which tears down its microVM.
func (f *Firecracker) Kill(ctx context.Context, jobID string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("kill job: %w", ctx.Err())
	default:
	}
	f.mu.Lock()
	cancel, ok := f.running[jobID]
	f.mu.Unlock()
	if !ok {
		return errNotRunning
	}
	cancel()
	return nil
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len()+len(p) > maxVMLogBytes {
		tail := b.buf.Bytes()
		if len(tail) > maxVMLogBytes {
			tail = tail[len(tail)-maxVMLogBytes:]
		}
		b.buf.Reset()
		b.buf.Write(tail)
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// workspaceImageSize computes the disk image size from the workspace bytes
// and the job's disk request. The provider's request cap always wins.
func workspaceImageSize(contentBytes, requestedDiskMB int64) int64 {
	const minImageBytes = 16 << 20
	if contentBytes < 0 {
		contentBytes = 0
	}
	size := contentBytes + contentBytes/10 + 8<<20 // content + ~10% + metadata headroom
	if size < minImageBytes {
		size = minImageBytes
	}
	if requestedDiskMB < 1 {
		requestedDiskMB = 64
	}
	if max := requestedDiskMB << 20; size > max {
		size = max
	}
	return size
}
