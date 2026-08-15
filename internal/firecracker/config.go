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

// NetworkInterface attaches a virtio-net device to the guest.
type NetworkInterface struct {
	IfaceID       string       `json:"iface_id"`
	HostDevName   string       `json:"host_dev_name"`
	GuestMAC      string       `json:"guest_mac,omitempty"`
	RxRateLimiter *RateLimiter `json:"rx_rate_limiter,omitempty"`
	TxRateLimiter *RateLimiter `json:"tx_rate_limiter,omitempty"`
}

// RateLimiter is a Firecracker token-bucket pair for one direction.
type RateLimiter struct {
	Bandwidth *TokenBucket `json:"bandwidth,omitempty"`
	Ops       *TokenBucket `json:"ops,omitempty"`
}

// TokenBucket limits bytes or packets to size per refill_time milliseconds.
type TokenBucket struct {
	Size       uint64 `json:"size"`
	RefillTime uint64 `json:"refill_time"`
}

// WriteConfig was removed in favor of the API socket: Firecracker's config
// file schema skips the serial device, so the executor boots VMs over the
// API (see ApiClient).
