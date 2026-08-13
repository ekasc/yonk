// Package executor contains replaceable workload execution backends.
package executor

import (
	"context"
	"io"

	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/workspace"
)

// Executor executes jobs and reports the platforms it can serve.
type Executor interface {
	Capabilities(ctx context.Context) ([]job.ExecutorCapability, error)
	Run(ctx context.Context, spec job.Job, work workspace.Workspace, stdout, stderr io.Writer) (job.Result, error)
	Kill(ctx context.Context, jobID string) error
}
