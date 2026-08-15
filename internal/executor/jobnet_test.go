package executor

import (
	"net"
	"strings"
	"testing"
)

func TestSubnetAllocatorUniqueAndReusable(t *testing.T) {
	alloc := newJobNetAllocator()
	first := alloc.allocate()
	if first == nil {
		t.Fatal("allocate() returned nil")
	}
	second := alloc.allocate()
	if second == nil {
		t.Fatal("second allocate() returned nil")
	}
	if first.idx == second.idx {
		t.Fatalf("subnets collide: %d == %d", first.idx, second.idx)
	}
	if !first.hostIP.Equal(net.ParseIP("10.255.0.1")) || !first.guestIP.Equal(net.ParseIP("10.255.0.2")) {
		t.Fatalf("first subnet = host %s guest %s", first.hostIP, first.guestIP)
	}
	if !second.guestIP.Equal(net.ParseIP("10.255.0.6")) {
		t.Fatalf("second subnet guest = %s, want 10.255.0.6", second.guestIP)
	}
	alloc.release(first.idx)
	third := alloc.allocate()
	if third == nil {
		t.Fatal("third allocate() returned nil")
	}
	if third.idx == first.idx || third.idx == second.idx {
		t.Fatalf("released subnet immediately reused or collided: %d", third.idx)
	}
}

func TestTapNameDeterministicAndShort(t *testing.T) {
	a := tapName("job_abc")
	b := tapName("job_abc")
	if a != b {
		t.Fatalf("tap names differ: %q vs %q", a, b)
	}
	if len(a) > 15 {
		t.Fatalf("tap name %q exceeds IFNAMSIZ", a)
	}
	if !strings.HasPrefix(a, tapPrefix) {
		t.Fatalf("tap name %q missing prefix", a)
	}
}

func TestFcAPISockFromCmdline(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{args: []string{"firecracker", "--api-sock", "/tmp/vm-1/api.sock"}, want: "/tmp/vm-1/api.sock"},
		{args: []string{"firecracker", "--config-file", "/tmp/x.json"}, want: ""},
		{args: []string{"firecracker"}, want: ""},
	}
	for _, test := range cases {
		if got := fcAPISockFromCmdline(test.args); got != test.want {
			t.Fatalf("fcAPISockFromCmdline(%v) = %q, want %q", test.args, got, test.want)
		}
	}
}

func TestGuestBootNetArg(t *testing.T) {
	sub := subnetForIndex(0)
	arg := guestBootNetArg(sub, []string{"1.1.1.1", "9.9.9.9"})
	want := "ip=10.255.0.2::10.255.0.1:255.255.255.252::eth0:off:1.1.1.1:9.9.9.9"
	if arg != want {
		t.Fatalf("boot arg = %q, want %q", arg, want)
	}
}

func TestGuestMAC(t *testing.T) {
	if mac := guestMAC(0); mac != "02:00:00:00:00:00" {
		t.Fatalf("guestMAC(0) = %q", mac)
	}
}

func TestYonkNftRulesetContainsBoundary(t *testing.T) {
	rules := yonkNftRuleset()
	for _, want := range []string{
		`iifname "yk*" counter drop`,                   // host protection
		"100.64.0.0/10",                                // CGNAT / tailnet
		"192.168.0.0/16",                               // LAN
		"meta nfproto ipv6 counter drop",               // no v6
		"iifname \"yk*\" masquerade",                   // egress
		"oifname \"yk*\" ct state established,related", // inbound replies only
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("ruleset missing %q", want)
		}
	}
}
