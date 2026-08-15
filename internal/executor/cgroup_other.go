//go:build !linux

package executor

// jobCgroup is a no-op off Linux: the Firecracker executor never runs in
// production outside a Linux/KVM worker, and host test doubles do not need
// real resource control.
type jobCgroup struct{}

func newJobCgroup(_ string, _, _ int) (*jobCgroup, error) {
	return &jobCgroup{}, nil
}

func (c *jobCgroup) addProcess(_ int) error { return nil }

func (c *jobCgroup) delete() {}

func cgroupV2Available() error { return nil }
