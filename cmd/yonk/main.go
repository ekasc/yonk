package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ekassinghchhabra/yonk/internal/client"
	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/workspace"
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
	workerEndpoint, command, commandArgs, exclusions, err := parseRunArgs(args[1:])
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
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "yonk: identify workspace: %v\n", err)
		return clientFailureExitCode
	}

	fmt.Fprintf(stdout, "worker: %s\n", info.Name)
	fmt.Fprintln(stdout, "syncing workspace...")
	archive, err := workspace.CreateArchive(ctx, workingDirectory, exclusions)
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
		Resources:      job.Resources{CPU: 1, MemoryMB: 128, DiskMB: 8192},
		TimeoutSeconds: 30,
	}
	result, err := protocolClient.Run(ctx, spec, archiveFile, func(event job.Event) error {
		switch event.Type {
		case job.EventStdout:
			_, err := stdout.Write(event.Data)
			return err
		case job.EventStderr:
			_, err := stderr.Write(event.Data)
			return err
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
	fmt.Fprintf(stdout, "exit: %d\n", result.ExitCode)
	return result.ExitCode
}

func parseRunArgs(args []string) (worker, command string, commandArgs, exclusions []string, err error) {
	if len(args) < 3 || args[0] == "" {
		return "", "", nil, nil, errors.New("run requires a worker and a command after --")
	}
	exclusions = workspace.DefaultExclusions()
	separator := -1
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--":
			separator = index
			index = len(args)
		case "--exclude":
			if index+1 >= len(args) || args[index+1] == "" {
				return "", "", nil, nil, errors.New("--exclude requires a path")
			}
			exclusions = append(exclusions, args[index+1])
			index++
		default:
			return "", "", nil, nil, fmt.Errorf("unknown run option %q", args[index])
		}
	}
	if separator == -1 || separator+1 >= len(args) || args[separator+1] == "" {
		return "", "", nil, nil, errors.New("run requires a command after --")
	}
	return args[0], args[separator+1], args[separator+2:], exclusions, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: yonk run <worker-endpoint> [--exclude <path>]... -- <command> [args...]")
}
