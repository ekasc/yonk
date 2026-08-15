//go:build !linux

package guest

// resolv.conf handling is Linux-only; other hosts compile a stub.
func setupResolvConf() error { return nil }
