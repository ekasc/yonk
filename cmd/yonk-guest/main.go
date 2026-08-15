// Command yonk-guest is the static init binary packed into each job's
// initramfs. It must be built with CGO_ENABLED=0.
package main

import (
	"fmt"
	"os"

	"github.com/ekassinghchhabra/yonk/internal/guest"
)

func main() {
	console, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err != nil {
		os.Exit(1)
	}
	if err := guest.Run(console); err != nil {
		fmt.Fprintf(console, "yonk-guest: fatal: %v\n", err)
		os.Exit(1)
	}
}
