package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ekasc/yonk/internal/client"
	"github.com/ekasc/yonk/internal/executor"
	"github.com/ekasc/yonk/internal/job"
	"github.com/ekasc/yonk/internal/workspace"
)

func TestServerRunStreamsEcho(t *testing.T) {
	protocolClient, closeServer := testClient(t)
	defer closeServer()

	ctx := context.Background()
	info, err := protocolClient.WorkerInfo(ctx)
	if err != nil {
		t.Fatalf("WorkerInfo() error = %v", err)
	}
	if info.Name != "test-worker" {
		t.Fatalf("WorkerInfo().Name = %q", info.Name)
	}

	spec := validJob("echo")
	var stdout strings.Builder
	archiveFile, removeArchive := testArchive(t)
	defer removeArchive()
	result, err := protocolClient.Run(ctx, spec, archiveFile, func(event job.Event) error {
		if event.Type == job.EventStdout {
			if _, err := stdout.Write(event.Data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "hello from yonk\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if result.ExitCode != 0 || result.TerminationReason != "completed" {
		t.Fatalf("result = %+v", result)
	}
	if result.Worker != "test-worker" {
		t.Fatalf("result.Worker = %q", result.Worker)
	}
}

func TestServerTransfersWorkspaceAndCleansJobState(t *testing.T) {
	probe := &workspaceProbeExecutor{}
	protocolClient, closeServer := testClientWithExecutor(t, probe)
	defer closeServer()

	archiveFile, removeArchive := testArchive(t)
	defer removeArchive()
	var stdout strings.Builder
	_, err := protocolClient.Run(context.Background(), validJob("echo"), archiveFile, func(event job.Event) error {
		if event.Type == job.EventStdout {
			_, err := stdout.Write(event.Data)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "workspace reached worker"; got != want {
		t.Fatalf("workspace content = %q, want %q", got, want)
	}
	if _, err := os.Stat(probe.workspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists after completion: %v", err)
	}
}

func TestServerRejectsNonAllowlistedCommand(t *testing.T) {
	protocolClient, closeServer := testClient(t)
	defer closeServer()

	archiveFile, removeArchive := testArchive(t)
	defer removeArchive()
	_, err := protocolClient.Run(context.Background(), validJob("sh"), archiveFile, nil)
	var remoteErr *client.RemoteError
	if !errors.As(err, &remoteErr) {
		t.Fatalf("Run() error = %v, want RemoteError", err)
	}
	if remoteErr.Code != "executor_failure" || !strings.Contains(remoteErr.Message, "only echo is allowed") {
		t.Fatalf("Run() error = %+v", remoteErr)
	}
}

func testClient(t *testing.T) (*client.Client, func()) {
	t.Helper()
	return testClientWithExecutor(t, executor.NewRestricted())
}

func testClientWithExecutor(t *testing.T, exec executor.Executor) (*client.Client, func()) {
	t.Helper()
	platform := job.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	info := job.WorkerInfo{
		Name:      "test-worker",
		Host:      platform,
		Resources: job.ResourceSnapshot{CPUCores: runtime.NumCPU()},
		Executors: []job.ExecutorCapability{{Platform: platform, Isolation: "restricted-host-process"}},
	}
	server, err := NewServer(info, exec, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	protocolClient, err := client.New(httpServer.URL, httpServer.Client())
	if err != nil {
		httpServer.Close()
		t.Fatalf("client.New() error = %v", err)
	}
	return protocolClient, httpServer.Close
}

type workspaceProbeExecutor struct {
	workspaceRoot string
}

func (e *workspaceProbeExecutor) Capabilities(context.Context) ([]job.ExecutorCapability, error) {
	return []job.ExecutorCapability{{
		Platform:  job.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Isolation: "test",
	}}, nil
}

func (e *workspaceProbeExecutor) Run(_ context.Context, _ job.Job, work workspace.Workspace, stdout, _ io.Writer, _ func(string, []byte) error) (job.Result, error) {
	e.workspaceRoot = work.Root
	content, err := os.ReadFile(filepath.Join(work.Root, "marker.txt"))
	if err != nil {
		return job.Result{}, err
	}
	if _, err := stdout.Write(content); err != nil {
		return job.Result{}, err
	}
	return job.Result{ExitCode: 0, TerminationReason: "completed"}, nil
}

func (*workspaceProbeExecutor) Kill(context.Context, string) error {
	return errors.New("job is not running")
}

func testArchive(t *testing.T) (*os.File, func()) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("workspace reached worker"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	archive, err := workspace.CreateArchive(context.Background(), root, workspace.DefaultExclusions())
	if err != nil {
		t.Fatalf("CreateArchive() error = %v", err)
	}
	file, err := archive.Open()
	if err != nil {
		_ = archive.Remove()
		t.Fatalf("Archive.Open() error = %v", err)
	}
	return file, func() {
		_ = file.Close()
		_ = archive.Remove()
	}
}

func validJob(command string) job.Job {
	return job.Job{
		Version:        job.ProtocolVersion,
		ID:             "job_test",
		Command:        command,
		Args:           []string{"hello from yonk"},
		CWD:            ".",
		Platform:       job.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Resources:      job.Resources{CPU: 1, MemoryMB: 128, DiskMB: 64},
		TimeoutSeconds: 30,
	}
}
