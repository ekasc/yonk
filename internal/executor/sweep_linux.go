//go:build linux

package executor

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	cgroup2 "github.com/containerd/cgroups/v3/cgroup2"
	"github.com/vishvananda/netlink"
)

// sweepOrphans removes VM state left behind by a daemon that died mid-job:
// job directories, cgroups, and TAP devices. State owned by a live
// Firecracker process is always kept, so a running VM from another daemon
// instance is never disturbed.
func sweepOrphans(workDir string, logger *slog.Logger) {
	orphanFCs := make([]int, 0)
	orphanJobDirs := make(map[string]bool)
	liveJobDirs := make(map[string]bool)

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(args) == 0 || filepath.Base(args[0]) != "firecracker" {
			continue
		}
		apiSock := fcAPISockFromCmdline(args)
		jobDir := filepath.Dir(apiSock)
		if apiSock == "" || (jobDir != workDir && !strings.HasPrefix(jobDir, workDir+"/")) {
			continue // not one of our VMs
		}
		if isOrphanedProcess(pid) {
			orphanFCs = append(orphanFCs, pid)
			orphanJobDirs[jobDir] = true
		} else {
			liveJobDirs[jobDir] = true
		}
	}

	// VMs whose owning daemon is gone cannot run jobs; terminate them so the
	// box does not burn resources for up to the guest's timeout.
	for _, pid := range orphanFCs {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if len(orphanFCs) > 0 {
		time.Sleep(2 * time.Second)
		for _, pid := range orphanFCs {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(time.Second)
		logger.Warn("terminated firecracker processes orphaned by a crashed daemon", "pids", orphanFCs)
	}
	for dir := range orphanJobDirs {
		delete(liveJobDirs, dir)
	}

	// Job directories not owned by a live VM.
	if err := os.MkdirAll(workDir, 0o700); err == nil {
		if entries, err := os.ReadDir(workDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "vm-") {
					continue
				}
				path := filepath.Join(workDir, entry.Name())
				if liveJobDirs[path] {
					continue
				}
				if err := os.RemoveAll(path); err != nil {
					logger.Warn("remove orphaned job directory", "path", path, "error", err)
				} else {
					logger.Info("removed orphaned job directory", "path", path)
				}
			}
		}
	}

	// Cgroups with no live processes; the surviving ones identify live jobs.
	liveJobIDs := make(map[string]bool)
	if cgEntries, err := os.ReadDir("/sys/fs/cgroup"); err == nil {
		for _, entry := range cgEntries {
			name := entry.Name()
			if !strings.HasPrefix(name, "yonk-") {
				continue
			}
			procs, err := os.ReadFile("/sys/fs/cgroup/" + name + "/cgroup.procs")
			if err != nil || len(strings.TrimSpace(string(procs))) != 0 {
				liveJobIDs[strings.TrimPrefix(name, "yonk-")] = true
				continue
			}
			if manager, err := cgroup2.Load("/" + name); err == nil {
				if err := manager.Delete(); err == nil {
					logger.Info("removed orphaned cgroup", "cgroup", name)
				}
			}
		}
	}

	// TAP devices whose owning job has no live cgroup.
	liveTaps := make(map[string]bool)
	for jobID := range liveJobIDs {
		liveTaps[tapName(jobID)] = true
	}
	if links, err := netlink.LinkList(); err == nil {
		for _, link := range links {
			name := link.Attrs().Name
			if !strings.HasPrefix(name, tapPrefix) || liveTaps[name] {
				continue
			}
			if err := netlink.LinkDel(link); err != nil {
				logger.Warn("remove orphaned tap", "tap", name, "error", err)
			} else {
				logger.Info("removed orphaned tap", "tap", name)
			}
		}
	}
}

// isOrphanedProcess reports whether pid is a child that was reparented to
// init because its parent (the daemon) died.
func isOrphanedProcess(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	// Fields after the final ')' of comm: state ppid pgrp ...
	close := strings.LastIndex(string(stat), ")")
	if close < 0 || close+2 >= len(stat) {
		return true
	}
	fields := strings.Fields(string(stat[close+2:]))
	if len(fields) < 2 {
		return true
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return true
	}
	return ppid == 1
}
