//go:build !linux

package executor

import "log/slog"

// The orphan sweep inspects /proc, cgroups, and netlink and is Linux-only.
func sweepOrphans(string, *slog.Logger) {}
