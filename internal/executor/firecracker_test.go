package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/workspace"
)

func TestFirecrackerRunStreamsOutput(t *testing.T) {
	f := newTestFirecracker(t)
	workRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workRoot, "marker.txt"), []byte("workspace inside vm"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec := testVMJob("echo")
	var stdout strings.Builder
	result, err := f.Run(context.Background(), spec, workspace.Workspace{Root: workRoot}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "hello from vm\n" {
		t.Fatalf("stdout = %q", got)
	}
	if result.ExitCode != 0 || result.TerminationReason != "completed" {
		t.Fatalf("result = %+v", result)
	}
	assertCleanWorkDir(t, f)
}

func TestFirecrackerRunCancelled(t *testing.T) {
	f := newTestFirecracker(t)
	workRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var result job.Result
	var runErr error
	go func() {
		result, runErr = f.Run(ctx, testVMJob("sleep"), workspace.Workspace{Root: workRoot}, io.Discard, io.Discard)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}
	if runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	if result.TerminationReason != "cancelled" {
		t.Fatalf("result = %+v, want cancelled", result)
	}
	assertCleanWorkDir(t, f)
}

func TestFirecrackerRunGuestCrash(t *testing.T) {
	f := newTestFirecracker(t)
	_, err := f.Run(context.Background(), testVMJob("crash"), workspace.Workspace{Root: t.TempDir()}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "without a result") {
		t.Fatalf("Run() error = %v, want guest crash", err)
	}
	assertCleanWorkDir(t, f)
}

func TestFirecrackerKill(t *testing.T) {
	f := newTestFirecracker(t)
	spec := testVMJob("sleep")
	spec.ID = "job_kill"

	done := make(chan struct{})
	var result job.Result
	go func() {
		var err error
		result, err = f.Run(context.Background(), spec, workspace.Workspace{Root: t.TempDir()}, io.Discard, io.Discard)
		if err != nil {
			t.Errorf("Run() error = %v", err)
		}
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	if err := f.Kill(context.Background(), "job_kill"); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after Kill()")
	}
	if result.TerminationReason != "cancelled" {
		t.Fatalf("result = %+v, want cancelled", result)
	}
	assertCleanWorkDir(t, f)
}

func newTestFirecracker(t *testing.T) *Firecracker {
	t.Helper()
	workDir, err := os.MkdirTemp("/tmp", "yonk-vmtest-")
	if err != nil {
		t.Fatalf("create short work dir: %v", err)
	}
	return &Firecracker{
		cfg: FirecrackerConfig{
			BinPath:        buildFakeFirecracker(t),
			KernelPath:     filepath.Join(t.TempDir(), "vmlinux.bin"),
			GuestAgentPath: writeStubBinary(t, "guest-agent"),
			BusyboxPath:    writeStubBinary(t, "busybox"),
			WorkDir:        workDir,
			MaxVCPU:        2,
			MaxMemoryMB:    1024,
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		mkfs: func(root, image string, _ int64) error {
			return os.WriteFile(image, []byte("fake-ext4"), 0o600)
		},
		applets: func() ([]string, error) { return []string{"sh", "echo"}, nil },
		running: map[string]context.CancelFunc{},
	}
}

func buildFakeFirecracker(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-firecracker")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fake-firecracker")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake firecracker: %v: %s", err, out)
	}
	return bin
}

func writeStubBinary(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testVMJob(command string) job.Job {
	return job.Job{
		Version:        job.ProtocolVersion,
		ID:             "job_test",
		Command:        command,
		Args:           []string{"hello from vm"},
		CWD:            ".",
		Platform:       job.Platform{OS: "linux", Arch: "amd64"},
		Resources:      job.Resources{CPU: 1, MemoryMB: 128, DiskMB: 64},
		TimeoutSeconds: 30,
	}
}

func assertCleanWorkDir(t *testing.T, f *Firecracker) {
	t.Helper()
	entries, err := os.ReadDir(f.cfg.WorkDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("VM state not cleaned up: %v", names)
	}
}
