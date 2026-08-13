package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ekassinghchhabra/yonk/internal/executor"
	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "yonkd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	listen := flag.String("listen", "127.0.0.1:9665", "address on which to listen")
	name := flag.String("name", hostname, "worker name shown to clients")
	maxConcurrent := flag.Int("max-concurrent", 4, "maximum concurrent milestone jobs")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	restricted := executor.NewRestricted()
	capabilities, err := restricted.Capabilities(context.Background())
	if err != nil {
		return fmt.Errorf("read executor capabilities: %w", err)
	}
	memoryTotal, memoryAvailable := memorySnapshot()
	info := job.WorkerInfo{
		Name: *name,
		Host: job.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Resources: job.ResourceSnapshot{
			CPUCores:          runtime.NumCPU(),
			MemoryTotalMB:     memoryTotal,
			MemoryAvailableMB: memoryAvailable,
		},
		Executors: capabilities,
	}
	protocolServer, err := worker.NewServer(info, restricted, logger, *maxConcurrent)
	if err != nil {
		return fmt.Errorf("configure worker: %w", err)
	}
	httpServer := worker.HTTPServer(*listen, protocolServer.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("worker listening",
			"address", *listen,
			"name", *name,
			"executor", "restricted-host-process",
			"warning", "milestone executor only permits echo and is not a sandbox",
		)
		serveErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve worker protocol: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down worker: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve worker protocol: %w", err)
	}
	return nil
}

func memorySnapshot() (totalMB, availableMB int) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		valueKB, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalMB = int(valueKB / 1024)
		case "MemAvailable:":
			availableMB = int(valueKB / 1024)
		}
	}
	return totalMB, availableMB
}
