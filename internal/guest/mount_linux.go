//go:build linux

package guest

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func mountBasicsPlatform() error {
	mounts := []struct{ src, dst, fstype string }{
		{"proc", "/proc", "proc"},
		{"sysfs", "/sys", "sysfs"},
		{"devtmpfs", "/dev", "devtmpfs"},
		{"tmpfs", "/tmp", "tmpfs"},
		{"tmpfs", "/run", "tmpfs"},
	}
	for _, m := range mounts {
		if err := os.MkdirAll(m.dst, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", m.dst, err)
		}
		if err := unix.Mount(m.src, m.dst, m.fstype, 0, ""); err != nil {
			return fmt.Errorf("mount %s on %s: %w", m.fstype, m.dst, err)
		}
	}
	return nil
}

func mountWorkspacePlatform() error {
	if err := os.MkdirAll(workspaceMount, 0o755); err != nil {
		return fmt.Errorf("create workspace mount point: %w", err)
	}
	if err := unix.Mount(workspaceDevice, workspaceMount, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount workspace disk %s: %w", workspaceDevice, err)
	}
	return nil
}
