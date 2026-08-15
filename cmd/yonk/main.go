package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/ekasc/yonk/internal/client"
	"github.com/ekasc/yonk/internal/job"
	"github.com/ekasc/yonk/internal/workspace"
)

const clientFailureExitCode = 125

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		printUsage(stderr)
		return 2
	}
	workerEndpoint, command, commandArgs, options, err := parseRunArgs(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "yonk: %v\n", err)
		printUsage(stderr)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	protocolClient, err := client.New(workerEndpoint, nil)
	if err != nil {
		fmt.Fprintf(stderr, "yonk: configure worker: %v\n", err)
		return clientFailureExitCode
	}
	info, err := protocolClient.WorkerInfo(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "yonk: worker information: %v\n", err)
		return clientFailureExitCode
	}
	if len(info.Executors) == 0 {
		fmt.Fprintln(stderr, "yonk: worker has no executors")
		return clientFailureExitCode
	}
	jobID, err := client.NewJobID()
	if err != nil {
		fmt.Fprintf(stderr, "yonk: %v\n", err)
		return clientFailureExitCode
	}
	network := options.network
	if network != "" && !containsString(info.NetworkModes, network) {
		fmt.Fprintf(stderr, "yonk: worker does not support network mode %q (supported: %v)\n", network, info.NetworkModes)
		return clientFailureExitCode
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "yonk: identify workspace: %v\n", err)
		return clientFailureExitCode
	}

	fmt.Fprintf(stdout, "worker: %s\n", info.Name)
	fmt.Fprintln(stdout, "syncing workspace...")
	archive, err := workspace.CreateArchive(ctx, workingDirectory, options.exclusions)
	if err != nil {
		fmt.Fprintf(stderr, "yonk: package workspace: %v\n", err)
		return clientFailureExitCode
	}
	defer func() {
		if err := archive.Remove(); err != nil {
			fmt.Fprintf(stderr, "yonk: %v\n", err)
		}
	}()
	archiveFile, err := archive.Open()
	if err != nil {
		fmt.Fprintf(stderr, "yonk: %v\n", err)
		return clientFailureExitCode
	}
	defer archiveFile.Close()

	spec := job.Job{
		Version:        job.ProtocolVersion,
		ID:             jobID,
		Command:        command,
		Args:           commandArgs,
		CWD:            ".",
		Platform:       info.Executors[0].Platform,
		Resources:      job.Resources{CPU: options.cpu, MemoryMB: options.memoryMB, DiskMB: options.diskMB},
		TimeoutSeconds: options.timeoutSeconds,
		Network:        network,
		Artifacts:      options.artifacts,
	}
	result, err := protocolClient.Run(ctx, spec, archiveFile, func(event job.Event) error {
		switch event.Type {
		case job.EventStdout:
			_, err := stdout.Write(event.Data)
			return err
		case job.EventStderr:
			_, err := stderr.Write(event.Data)
			return err
		case job.EventArtifact:
			return writeArtifact(workingDirectory, event.Name, event.Data, stdout)
		default:
			return nil
		}
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			fmt.Fprintln(stderr, "yonk: job cancelled")
		} else {
			fmt.Fprintf(stderr, "yonk: %v\n", err)
		}
		return clientFailureExitCode
	}
	fmt.Fprintf(stdout, "duration: %.1fs\n", float64(result.DurationMillis)/1000)
	if result.PeakMemoryBytes > 0 {
		fmt.Fprintf(stdout, "peak memory: %.1f MB\n", float64(result.PeakMemoryBytes)/(1<<20))
	}
	fmt.Fprintf(stdout, "exit: %d\n", result.ExitCode)
	return result.ExitCode
}

// runOptions carries resource requests and workspace exclusions.
type runOptions struct {
	exclusions       []string
	noDefaultExclude bool
	cpu              int
	memoryMB         int
	diskMB           int
	timeoutSeconds   int
	network          string
	artifacts        []string
}

func defaultRunOptions() runOptions {
	return runOptions{
		exclusions:     workspace.DefaultExclusions(),
		cpu:            2,
		memoryMB:       1024,
		diskMB:         512,
		timeoutSeconds: 60,
	}
}

func parseRunArgs(args []string) (worker, command string, commandArgs []string, options runOptions, err error) {
	if len(args) < 3 || args[0] == "" {
		return "", "", nil, runOptions{}, errors.New("run requires a worker and a command after --")
	}
	options = defaultRunOptions()
	separator := -1
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--":
			separator = index
			index = len(args)
		case "--exclude":
			if index+1 >= len(args) || args[index+1] == "" {
				return "", "", nil, runOptions{}, errors.New("--exclude requires a path")
			}
			options.exclusions = append(options.exclusions, args[index+1])
			index++
		case "--no-default-excludes":
			options.noDefaultExclude = true
			options.exclusions = nil
		case "--cpu":
			if options.cpu, err = parseRunInt(args, index, "--cpu", 1, 1024); err != nil {
				return "", "", nil, runOptions{}, err
			}
			index++
		case "--memory-mb":
			if options.memoryMB, err = parseRunInt(args, index, "--memory-mb", 1, 1<<20); err != nil {
				return "", "", nil, runOptions{}, err
			}
			index++
		case "--disk-mb":
			if options.diskMB, err = parseRunInt(args, index, "--disk-mb", 1, 1<<20); err != nil {
				return "", "", nil, runOptions{}, err
			}
			index++
		case "--timeout":
			if options.timeoutSeconds, err = parseRunInt(args, index, "--timeout", 1, 7200); err != nil {
				return "", "", nil, runOptions{}, err
			}
			index++
		case "--network":
			if index+1 >= len(args) {
				return "", "", nil, runOptions{}, errors.New("--network requires a value")
			}
			switch args[index+1] {
			case "none", "egress":
				options.network = args[index+1]
			default:
				return "", "", nil, runOptions{}, fmt.Errorf("--network must be none or egress")
			}
			index++
		case "--artifact":
			if index+1 >= len(args) || args[index+1] == "" {
				return "", "", nil, runOptions{}, errors.New("--artifact requires a workspace-relative path")
			}
			options.artifacts = append(options.artifacts, args[index+1])
			index++
		default:
			return "", "", nil, runOptions{}, fmt.Errorf("unknown run option %q", args[index])
		}
	}
	if separator == -1 || separator+1 >= len(args) || args[separator+1] == "" {
		return "", "", nil, runOptions{}, errors.New("run requires a command after --")
	}
	return args[0], args[separator+1], args[separator+2:], options, nil
}

func parseRunInt(args []string, index int, flag string, min, max int) (int, error) {
	if index+1 >= len(args) {
		return 0, fmt.Errorf("%s requires a value", flag)
	}
	value, err := strconv.Atoi(args[index+1])
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", flag, min, max)
	}
	return value, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// writeArtifact stores a returned artifact next to the workspace, using only
// its base name so a hostile artifact cannot escape the working directory.
func writeArtifact(dir, name string, data []byte, stdout io.Writer) error {
	if name != filepath.Base(name) {
		return fmt.Errorf("worker returned unsafe artifact name %q", name)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write artifact %s: %w", name, err)
	}
	fmt.Fprintf(stdout, "artifact: %s (%d bytes)\n", name, len(data))
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: yonk run <worker-endpoint> [options] -- <command> [args...]")
	fmt.Fprintln(w, "options:")
	fmt.Fprintln(w, "  --exclude <path>        exclude a path from the workspace (repeatable)")
	fmt.Fprintln(w, "  --no-default-excludes   transfer everything, including node_modules and build dirs")
	fmt.Fprintln(w, "  --cpu <n>               requested vCPUs (default 2)")
	fmt.Fprintln(w, "  --memory-mb <n>         requested memory MiB (default 1024)")
	fmt.Fprintln(w, "  --disk-mb <n>           requested workspace disk MiB (default 512)")
	fmt.Fprintln(w, "  --timeout <secs>        job timeout, 1-7200 (default 60)")
	fmt.Fprintln(w, "  --network <mode>         none or egress (default none)")
	fmt.Fprintln(w, "  --artifact <path>        workspace-relative path to return after the job (repeatable)")
}
