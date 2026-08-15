//go:build !linux

package executor

import (
	"errors"

	"github.com/ekasc/yonk/internal/firecracker"
)

// jobNetwork is a no-op off Linux: real networking requires netlink and nft,
// and the Firecracker executor only runs in production on Linux/KVM workers.
type jobNetwork struct {
	ready bool
	err   error
}

func newJobNetwork(_, _ uint64, _ []string) *jobNetwork {
	return &jobNetwork{}
}

func (jn *jobNetwork) setupTap(string) (*jobSubnet, string, error) {
	return nil, "", errors.New("job networking requires linux")
}

func (jn *jobNetwork) teardownTap(string, *jobSubnet) {}

func (jn *jobNetwork) rateLimiter() *firecracker.RateLimiter { return nil }
