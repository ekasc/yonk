//go:build !linux

package guest

// The pids limit is a guest-side (Linux) control; other hosts compile a stub.
func enablePidsLimit() error { return nil }
