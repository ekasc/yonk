//go:build linux

package executor

import (
	"fmt"
	"os"

	cgroup2 "github.com/containerd/cgroups/v3/cgroup2"
)

const cgroupMount = "/sys/fs/cgroup"

// jobCgroup is a per-job cgroup v2 subtree that holds one Firecracker
// process, bounding the CPU, memory, and thread count the VM can use.
type jobCgroup struct {
	manager *cgroup2.Manager
}

func newJobCgroup(jobID string, vcpu, memoryMB int) (*jobCgroup, error) {
	// memory.max must cover the guest RAM plus headroom for Firecracker
	// overhead and page cache so legitimate guests are never killed by the
	// backstop. The guest's own memory allocation remains the real ceiling.
	memoryMax := int64(memoryMB)<<20 + 256<<20
	resources := &cgroup2.Resources{
		CPU:    &cgroup2.CPU{Max: cgroup2.CPUMax(fmt.Sprintf("%d 100000", vcpu*100000))},
		Memory: &cgroup2.Memory{Max: &memoryMax},
		Pids:   &cgroup2.Pids{Max: 1024},
	}
	manager, err := cgroup2.NewManager(cgroupMount, "/yonk-"+jobID, resources)
	if err != nil {
		return nil, fmt.Errorf("create job cgroup: %w", err)
	}
	return &jobCgroup{manager: manager}, nil
}

func (c *jobCgroup) addProcess(pid int) error {
	if c == nil || c.manager == nil {
		return nil
	}
	if err := c.manager.AddProc(uint64(pid)); err != nil {
		return fmt.Errorf("place firecracker in job cgroup: %w", err)
	}
	return nil
}

func (c *jobCgroup) delete() {
	if c == nil || c.manager == nil {
		return
	}
	_ = c.manager.Delete()
}

func cgroupV2Available() error {
	if _, err := os.Stat(cgroupMount + "/cgroup.controllers"); err != nil {
		return fmt.Errorf("cgroup v2 not available: %w", err)
	}
	return nil
}
