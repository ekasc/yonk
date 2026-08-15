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
	BinPath        string   // firecracker binary
	KernelPath     string   // guest kernel image (vmlinux)
	RootfsPath     string   // read-only base rootfs with toolchains and yonk-guest
	WorkDir        string   // per-job VM state directory
	MaxVCPU        int      // provider ceiling per guest
	MaxMemoryMB    int      // provider ceiling per guest (MiB)
	MaxEgressMbps  uint64   // per-job egress bandwidth ceiling (0 disables)
	MaxEgressPPS   uint64   // per-job egress packet ceiling (0 disables)
	GuestResolvers []string // DNS resolvers for egress jobs
}

// Firecracker executes each job inside an ephemeral Firecracker microVM.
type Firecracker struct {
	cfg    FirecrackerConfig
	logger *slog.Logger
	mkfs   func(root, image string, sizeBytes int64) error
	net    *jobNetwork

	mu      sync.Mutex
	running map[string]context.CancelFunc
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
		running: map[string]context.CancelFunc{},
	}
	f.net = newJobNetwork(cfg.MaxEgressMbps, cfg.MaxEgressPPS, cfg.GuestResolvers)
	if err := f.preflight(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Firecracker) preflight() error {
	for _, path := range []string{f.cfg.BinPath, f.cfg.KernelPath, f.cfg.RootfsPath} {
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
	if err := cgroupV2Available(); err != nil {
		return err
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

// NetworkModes reports the network modes this worker can provide.
func (f *Firecracker) NetworkModes() []string {
	if f.net != nil && f.net.ready {
		return []string{"none", "egress"}
	}
	return []string{"none"}
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

	vcpu := clampInt(spec.Resources.CPU, 1, f.cfg.MaxVCPU)
	memoryMB := clampInt(spec.Resources.MemoryMB, 128, f.cfg.MaxMemoryMB)

	// Resource limits are created up front and always removed, so a job can
	// never outlive its cgroup even if the VM start path fails midway.
	limits, err := newJobCgroup(spec.ID, vcpu, memoryMB)
	if err != nil {
		return result, fmt.Errorf("prepare resource limits: %w", err)
	}
	defer limits.delete()

	vsockPath := filepath.Join(jobDir, "v")
	bootArgs := "root=/dev/vda ro console=ttyS0 reboot=k panic=1 pci=off init=/usr/sbin/yonk-guest"
	var networkInterface *firecracker.NetworkInterface
	var jobSub *jobSubnet
	var tapName string
	if spec.Network == "egress" {
		if f.net == nil || !f.net.ready {
			if f.net != nil && f.net.err != nil {
				return result, fmt.Errorf("job networking unavailable: %w", f.net.err)
			}
			return result, errors.New("job networking unavailable on this worker")
		}
		var err error
		jobSub, tapName, err = f.net.setupTap(spec.ID)
		if err != nil {
			return result, fmt.Errorf("prepare job network: %w", err)
		}
		defer func() { f.net.teardownTap(tapName, jobSub) }()
		bootArgs += " " + guestBootNetArg(jobSub, f.cfg.GuestResolvers)
		networkInterface = &firecracker.NetworkInterface{
			IfaceID:       "eth0",
			HostDevName:   tapName,
			GuestMAC:      guestMAC(jobSub.idx),
			TxRateLimiter: f.net.rateLimiter(),
			RxRateLimiter: f.net.rateLimiter(),
		}
	}
	bootSource := firecracker.BootSource{
		KernelImagePath: f.cfg.KernelPath,
		BootArgs:        bootArgs,
	}
	drives := []firecracker.Drive{
		{
			DriveID:      "rootfs",
			PathOnHost:   f.cfg.RootfsPath,
			IsRootDevice: true,
			IsReadOnly:   true,
		},
		{
			DriveID:      "workspace",
			PathOnHost:   workspaceImage,
			IsRootDevice: false,
			IsReadOnly:   false,
		},
	}
	machineConfig := firecracker.MachineConfig{
		VCPUCount:  vcpu,
		MemSizeMiB: memoryMB,
		SMT:        false,
	}
	vsockConfig := firecracker.VSock{GuestCID: guestproto.GuestCID, UDSPath: vsockPath}
	serialConfig := firecracker.Serial{SerialOutPath: filepath.Join(jobDir, "console.log")}

	// Guest-initiated connections arrive on {uds_path}_{port}; listen before
	// the guest boots so the agent's dial never races the host.
	listener, err := net.Listen("unix", fmt.Sprintf("%s_%d", vsockPath, guestproto.HostPort))
	if err != nil {
		return result, fmt.Errorf("open vsock listener: %w", err)
	}
	defer listener.Close()

	cmd := exec.CommandContext(jobCtx, f.cfg.BinPath,
		"--api-sock", filepath.Join(jobDir, "api.sock"))
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
	if err := limits.addProcess(cmd.Process.Pid); err != nil {
		killProcess(cmd, processExited, killGrace)
		return result, err
	}

	apiSocket := filepath.Join(jobDir, "api.sock")
	api := firecracker.NewApiClient(apiSocket)
	if err := f.configureVM(jobCtx, api, apiSocket, bootSource, drives, machineConfig, vsockConfig, serialConfig, networkInterface); err != nil {
		killProcess(cmd, processExited, killGrace)
		return result, err
	}

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
	if runErr != nil {
		if tail := consoleTail(jobDir); tail != "" {
			f.logger.Warn("guest failed", "job_id", spec.ID, "error", runErr, "console_tail", tail)
		}
	}
	return result, runErr
}

// consoleTail returns the last lines of the guest serial console log.
func consoleTail(jobDir string) string {
	data, err := os.ReadFile(filepath.Join(jobDir, "console.log"))
	if err != nil {
		return ""
	}
	const maxTail = 8 << 10
	if len(data) > maxTail {
		data = data[len(data)-maxTail:]
	}
	return string(data)
}

// configureVM waits for the API socket, applies the VM configuration, and
// boots the guest.
func (f *Firecracker) configureVM(ctx context.Context, api *firecracker.ApiClient, apiSocket string, bootSource firecracker.BootSource, drives []firecracker.Drive, machineConfig firecracker.MachineConfig, vsockConfig firecracker.VSock, serialConfig firecracker.Serial, networkInterface *firecracker.NetworkInterface) error {
	if err := firecracker.WaitForSocket(ctx, apiSocket); err != nil {
		return err
	}
	type apiStep struct {
		path string
		body any
	}
	steps := []apiStep{
		{"/boot-source", bootSource},
		{"/machine-config", machineConfig},
		{"/vsock", vsockConfig},
		{"/serial", serialConfig},
		{"/entropy", struct{}{}},
	}
	for _, drive := range drives {
		steps = append(steps, apiStep{path: "/drives/" + drive.DriveID, body: drive})
	}
	if networkInterface != nil {
		steps = append(steps, apiStep{path: "/network-interfaces/" + networkInterface.IfaceID, body: networkInterface})
	}
	steps = append(steps, apiStep{path: "/actions", body: map[string]string{"action_type": "InstanceStart"}})
	for _, step := range steps {
		if err := api.Put(ctx, step.path, step.body); err != nil {
			return fmt.Errorf("configure firecracker: %w", err)
		}
	}
	return nil
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
			result.PeakMemoryBytes = msg.Result.PeakMemoryBytes
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
