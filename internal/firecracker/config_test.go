package firecracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{
		BootSource: BootSource{
			KernelImagePath: "/opt/yonk/vmlinux.bin",
			InitrdPath:      "/tmp/job/initramfs.cpio",
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
		},
		Drives: []Drive{{
			DriveID:      "workspace",
			PathOnHost:   "/tmp/job/workspace.ext4",
			IsRootDevice: false,
			IsReadOnly:   false,
		}},
		MachineConfig: MachineConfig{VCPUCount: 2, MemSizeMiB: 1024, SMT: false},
		VSock:         VSock{GuestCID: 3, UDSPath: "/tmp/job/v.sock"},
	}
	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	for _, key := range []string{"boot-source", "drives", "machine-config", "vsock"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("config missing key %q", key)
		}
	}
}
