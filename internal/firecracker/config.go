// Package firecracker builds and manages host-side state for Firecracker
// microVMs: configuration files, initramfs images, and workspace disks.
package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the Firecracker --config-file document.
type Config struct {
	BootSource    BootSource    `json:"boot-source"`
	Drives        []Drive       `json:"drives"`
	MachineConfig MachineConfig `json:"machine-config"`
	VSock         VSock         `json:"vsock"`
	Serial        *Serial       `json:"serial,omitempty"`
}

// BootSource identifies the guest kernel and optional initramfs.
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	InitrdPath      string `json:"initrd_path"`
	BootArgs        string `json:"boot_args"`
}

// Drive is a block device attached to the guest.
type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// MachineConfig sizes the microVM.
type MachineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt"`
}

// VSock attaches the control channel.
type VSock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// Serial captures guest console output to a host file.
type Serial struct {
	SerialOutPath string `json:"serial_out_path"`
}

// WriteConfig writes a Firecracker configuration file.
func WriteConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode firecracker config: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}
	return nil
}
