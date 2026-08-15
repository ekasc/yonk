//go:build !linux

package guest

import "sync/atomic"

// Peak-memory sampling reads /proc and is Linux-only.
func samplePeakMemory(peak *atomic.Int64, _ int, _ <-chan struct{}) {}
