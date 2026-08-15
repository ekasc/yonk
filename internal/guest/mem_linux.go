//go:build linux

package guest

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// samplePeakMemory polls the total RSS of the job's process group and
// records the peak until stop is closed.
func samplePeakMemory(peak *atomic.Int64, pgid int, stop <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if rss := processGroupRSS(pgid); rss > peak.Load() {
				peak.Store(rss)
			}
		}
	}
}

// processGroupRSS sums resident bytes of processes in the given process
// group by parsing /proc/*/stat.
func processGroupRSS(pgid int) int64 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// Fields after the final ')' of comm: state ppid pgrp ... rss
		// (rss is the 22nd field after comm, in pages).
		close := bytes.LastIndexByte(data, ')')
		if close < 0 || close+2 >= len(data) {
			continue
		}
		fields := strings.Fields(string(data[close+2:]))
		if len(fields) < 22 {
			continue
		}
		pgrp, err := strconv.Atoi(fields[2])
		if err != nil || pgrp != pgid {
			continue
		}
		pages, err := strconv.ParseInt(fields[21], 10, 64)
		if err != nil {
			continue
		}
		total += pages * 4096
	}
	return total
}
