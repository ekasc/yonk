// Job networking for the Firecracker executor: per-job TAP devices carved
// from a private /16, and a shared nftables table enforcing egress-only
// access. Platform-neutral pieces live here; netlink/nft execution is in
// jobnet_linux.go.
package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
)

const (
	tapPrefix   = "yk"            // tap interface name prefix ("yk" + 8 hex chars)
	netPoolCIDR = "10.255.0.0/16" // pool carved into /30 subnets
	subnetBits  = 30
	hostOffset  = 1 // host IP offset within the /30
	guestOffset = 2 // guest IP offset within the /30
)

// jobSubnet is a unique /30 allocation for one job.
type jobSubnet struct {
	idx     uint32
	hostIP  net.IP
	guestIP net.IP
	mask    net.IPMask
}

// jobNetAllocator hands out unique /30 subnets from the pool.
type jobNetAllocator struct {
	mu    sync.Mutex
	next  uint32
	used  map[uint32]bool
	limit uint32
}

func newJobNetAllocator() *jobNetAllocator {
	return &jobNetAllocator{used: map[uint32]bool{}, limit: 1 << 14}
}

func (a *jobNetAllocator) allocate() *jobSubnet {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := uint32(0); i < a.limit; i++ {
		idx := (a.next + i) % a.limit
		if a.used[idx] {
			continue
		}
		a.used[idx] = true
		a.next = idx + 1
		return subnetForIndex(idx)
	}
	return nil
}

func (a *jobNetAllocator) release(idx uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.used, idx)
}

func subnetForIndex(idx uint32) *jobSubnet {
	base := byte((idx & 0xff) << 2)
	host := net.IPv4(10, 255, byte(idx>>8), base+hostOffset)
	guest := net.IPv4(10, 255, byte(idx>>8), base+guestOffset)
	return &jobSubnet{
		idx:     idx,
		hostIP:  host,
		guestIP: guest,
		mask:    net.CIDRMask(subnetBits, 32),
	}
}

// guestMAC derives a locally administered unicast MAC from the subnet index.
func guestMAC(idx uint32) string {
	return fmt.Sprintf("02:00:00:00:%02x:%02x", byte(idx>>8), byte(idx&0xff))
}

// tapName derives a deterministic, short tap name from the job ID.
func tapName(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	return tapPrefix + hex.EncodeToString(sum[:4])
}

// guestBootNetArg renders the kernel ip= static-config parameter for a job
// subnet, with DNS resolvers in the trailing fields.
func guestBootNetArg(sub *jobSubnet, resolvers []string) string {
	arg := fmt.Sprintf("ip=%s::%s:255.255.255.252::eth0:off", sub.guestIP, sub.hostIP)
	for _, resolver := range resolvers {
		arg += ":" + resolver
	}
	return arg
}

// yonkNftRuleset is the shared egress-only table. Jobs may reach public
// destinations only: inbound from any job tap is dropped at INPUT (host
// protection), private/CGNAT/reserved and IPv6 destinations are dropped at
// FORWARD (LAN protection), and masquerade handles egress.
func yonkNftRuleset() string {
	return `table inet yonk {
  chain tap-input {
    type filter hook input priority filter; policy accept;
    iifname "yk*" counter drop
  }
  chain tap-forward {
    type filter hook forward priority filter; policy accept;
    iifname "yk*" ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, 127.0.0.0/8, 224.0.0.0/4, 240.0.0.0/4 } counter drop
    iifname "yk*" meta nfproto ipv6 counter drop
    iifname "yk*" ct state new,established counter accept
    oifname "yk*" ct state established,related counter accept
    oifname "yk*" counter drop
  }
  chain tap-nat {
    type nat hook postrouting priority srcnat; policy accept;
    iifname "yk*" masquerade
  }
}`
}
