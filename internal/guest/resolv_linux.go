//go:build linux

package guest

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// setupResolvConf writes a working resolver into /run (a tmpfs mounted by
// mountBasics) and bind-mounts it over /etc/resolv.conf, which the read-only
// rootfs cannot modify. Nameservers come from /proc/net/pnp, populated by the
// kernel ip= boot parameter.
func setupResolvConf() error {
	nameservers, err := pnpNameservers()
	if err != nil || len(nameservers) == 0 {
		nameservers = []string{"1.1.1.1"}
	}
	var builder strings.Builder
	for _, server := range nameservers {
		fmt.Fprintf(&builder, "nameserver %s\n", server)
	}
	if err := os.WriteFile("/run/resolv.conf", []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write /run/resolv.conf: %w", err)
	}
	if err := unix.Mount("/run/resolv.conf", "/etc/resolv.conf", "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind-mount resolv.conf: %w", err)
	}
	return nil
}

// pnpNameservers reads nameserver lines from /proc/net/pnp.
func pnpNameservers() ([]string, error) {
	data, err := os.ReadFile("/proc/net/pnp")
	if err != nil {
		return nil, err
	}
	var nameservers []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			nameservers = append(nameservers, fields[1])
		}
	}
	return nameservers, nil
}
