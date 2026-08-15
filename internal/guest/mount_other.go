//go:build !linux

package guest

import "errors"

// The guest agent only runs inside a Linux microVM. These stubs keep the
// package buildable on other hosts (the binary is cross-compiled).
func mountBasicsPlatform() error {
	return errors.New("guest agent requires linux")
}

func mountWorkspacePlatform() error {
	return errors.New("guest agent requires linux")
}
