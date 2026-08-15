//go:build linux

package executor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/ekasc/yonk/internal/firecracker"
)

// jobNetwork owns the shared nftables table and per-job TAP devices.
type jobNetwork struct {
	alloc     *jobNetAllocator
	maxMbps   uint64
	maxPPS    uint64
	resolvers []string
	ready     bool
	err       error
}

// newJobNetwork initializes host-side job networking. A failure is recorded
// rather than fatal: network-less jobs still run, and egress jobs fail loudly.
func newJobNetwork(maxMbps, maxPPS uint64, resolvers []string) *jobNetwork {
	jn := &jobNetwork{
		alloc:     newJobNetAllocator(),
		maxMbps:   maxMbps,
		maxPPS:    maxPPS,
		resolvers: resolvers,
	}
	if err := jn.init(); err != nil {
		jn.err = err
	} else {
		jn.ready = true
	}
	return jn
}

func (jn *jobNetwork) init() error {
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return fmt.Errorf("job networking requires /dev/net/tun: %w", err)
	}
	_ = exec.Command("modprobe", "tun", "nf_tables", "nft_nat", "nft_masq", "nft_ct", "nf_nat", "nf_conntrack").Run()
	if err := writeSysctl("/proc/sys/net/ipv4/ip_forward", "1"); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}
	if err := applyNftRuleset(yonkNftRuleset()); err != nil {
		return fmt.Errorf("load job nftables rules: %w", err)
	}
	return nil
}

// setupTap creates a per-job TAP with the host's /30 address and returns the
// allocation plus the device name. Call before the VM boots.
func (jn *jobNetwork) setupTap(jobID string) (*jobSubnet, string, error) {
	sub := jn.alloc.allocate()
	if sub == nil {
		return nil, "", errors.New("no job subnets available")
	}
	name := tapName(jobID)
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	tap := &netlink.Tuntap{
		LinkAttrs: attrs,
		Mode:      netlink.TUNTAP_MODE_TAP,
		Flags:     netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR,
		Queues:    1,
	}
	fail := func(err error) (*jobSubnet, string, error) {
		jn.alloc.release(sub.idx)
		closeTapFds(tap)
		_ = netlink.LinkDel(tap)
		return nil, "", err
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fail(fmt.Errorf("create tap %s: %w", name, err))
	}
	if err := netlink.AddrAdd(tap, &netlink.Addr{IPNet: &net.IPNet{IP: sub.hostIP, Mask: sub.mask}}); err != nil {
		return fail(fmt.Errorf("assign host address to %s: %w", name, err))
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return fail(fmt.Errorf("bring up %s: %w", name, err))
	}
	_ = writeSysctl("/proc/sys/net/ipv4/conf/"+name+"/rp_filter", "1")
	_ = writeSysctl("/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", "1")
	// The device is persistent; close the creation fds so Firecracker can
	// attach to it (an open fd keeps the device busy and TUNSETIFF fails).
	closeTapFds(tap)
	return sub, name, nil
}

// closeTapFds closes the file descriptors held by a created Tuntap. The
// device persists on its own; the fds are only needed during setup.
func closeTapFds(tap *netlink.Tuntap) {
	for _, fd := range tap.Fds {
		_ = fd.Close()
	}
	tap.Fds = nil
}

// teardownTap removes the device and returns its subnet to the pool.
func (jn *jobNetwork) teardownTap(name string, sub *jobSubnet) {
	if link, err := netlink.LinkByName(name); err == nil {
		_ = netlink.LinkDel(link)
	}
	if sub != nil {
		jn.alloc.release(sub.idx)
	}
}

// rateLimiter builds the Firecracker token-bucket pair for a job interface.
func (jn *jobNetwork) rateLimiter() *firecracker.RateLimiter {
	if jn.maxMbps == 0 && jn.maxPPS == 0 {
		return nil
	}
	limiter := &firecracker.RateLimiter{}
	if jn.maxMbps > 0 {
		limiter.Bandwidth = &firecracker.TokenBucket{Size: jn.maxMbps * 1024 * 1024 / 8, RefillTime: 1000}
	}
	if jn.maxPPS > 0 {
		limiter.Ops = &firecracker.TokenBucket{Size: jn.maxPPS, RefillTime: 1000}
	}
	return limiter
}

func writeSysctl(path, value string) error {
	return os.WriteFile(path, []byte(value), 0o644)
}

func applyNftRuleset(ruleset string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
}
