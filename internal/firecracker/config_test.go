package firecracker

import (
	"encoding/json"
	"testing"
)

// The API request bodies use Firecracker's exact field names.
func TestRequestBodyJSON(t *testing.T) {
	bodies := []struct {
		name string
		body any
		want map[string]any
	}{
		{
			name: "boot-source",
			body: BootSource{KernelImagePath: "/k", InitrdPath: "/i", BootArgs: "console=ttyS0"},
			want: map[string]any{"kernel_image_path": "/k", "initrd_path": "/i", "boot_args": "console=ttyS0"},
		},
		{
			name: "machine-config",
			body: MachineConfig{VCPUCount: 2, MemSizeMiB: 1024, SMT: false},
			want: map[string]any{"vcpu_count": float64(2), "mem_size_mib": float64(1024), "smt": false},
		},
		{
			name: "vsock",
			body: VSock{GuestCID: 3, UDSPath: "/tmp/v"},
			want: map[string]any{"guest_cid": float64(3), "uds_path": "/tmp/v"},
		},
		{
			name: "drive",
			body: Drive{DriveID: "workspace", PathOnHost: "/tmp/w", IsRootDevice: false, IsReadOnly: false},
			want: map[string]any{"drive_id": "workspace", "path_on_host": "/tmp/w", "is_root_device": false, "is_read_only": false},
		},
		{
			name: "serial",
			body: Serial{SerialOutPath: "/tmp/console.log"},
			want: map[string]any{"serial_out_path": "/tmp/console.log"},
		},
	}
	for _, test := range bodies {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range test.want {
				if got[key] != want {
					t.Fatalf("%s = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}
