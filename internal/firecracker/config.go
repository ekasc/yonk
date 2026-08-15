// Package firecracker builds and manages host-side state for Firecracker
// microVMs: API requests, initramfs images, and workspace disks.
package firecracker

// BootSource identifies the guest kernel and optional initramfs.
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	InitrdPath      string `json:"initrd_path,omitempty"`
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

// WriteConfig was removed in favor of the API socket: Firecracker's config
// file schema skips the serial device, so the executor boots VMs over the
// API (see ApiClient).
