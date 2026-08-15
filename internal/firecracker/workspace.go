package firecracker

import (
	"fmt"
	"os"
	"os/exec"
)

// MkfsExt4 builds an ext4 disk image of at least sizeBytes, populated from
// root. It requires e2fsprogs with the mkfs.ext4 -d option (1.46 or newer)
// on the worker host.
func MkfsExt4(root, image string, sizeBytes int64) error {
	file, err := os.OpenFile(image, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create workspace disk image: %w", err)
	}
	if err := file.Truncate(sizeBytes); err != nil {
		_ = file.Close()
		return fmt.Errorf("size workspace disk image: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close workspace disk image: %w", err)
	}
	cmd := exec.Command("mkfs.ext4", "-q", "-F", "-m", "0", "-d", root, image)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4: %w: %s", err, out)
	}
	return nil
}
