// Command yonk-guest is the static init binary baked into the worker rootfs.
// It must be built with CGO_ENABLED=0.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ekasc/yonk/internal/guest"
)

func main() {
	log := openConsole()
	if err := guest.Run(log); err != nil {
		fmt.Fprintf(log, "yonk-guest: fatal: %v\n", err)
		os.Exit(1)
	}
}

// openConsole returns /dev/console when available; diagnostics also reach the
// host over vsock, so a missing console node is not fatal.
func openConsole() io.Writer {
	file, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err != nil {
		return io.Discard
	}
	return file
}
