//go:build linux

package guest

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// guestPidsLimit caps total live processes inside the guest so a fork bomb
// exhausts forks (EAGAIN) instead of guest kernel memory, which would starve
// the agent. The worker can raise this as real workloads demand.
const guestPidsLimit = 256

// enablePidsLimit is a best-effort defense-in-depth control. When the kernel
// lacks the pids controller the guest still runs; an extreme fork bomb may
// then crash the VM, which the host already contains and cleans up.
func enablePidsLimit() error {
	const root = "/sys/fs/cgroup/pids"
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := unix.Mount("cgroup", root, "cgroup", 0, "pids"); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "pids.max"), []byte("256"), 0o644)
}
