// Package job defines Yonk's portable, versioned job protocol.
package job

import (
	"errors"
	"fmt"
	"time"
)

const (
	// ProtocolVersion is the job protocol version supported by this build.
	ProtocolVersion = 1

	maxCommandLength  = 4 * 1024
	maxArgumentLength = 4 * 1024
	maxArguments      = 128
	maxTotalArgBytes  = 32 * 1024
)

// Platform identifies an operating system and CPU architecture.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// String returns the conventional os/arch representation.
func (p Platform) String() string {
	return p.OS + "/" + p.Arch
}

// Resources describes resources requested by a job. The worker remains the
// authority and may reject or clamp these values.
type Resources struct {
	CPU      int `json:"cpu"`
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
}

// Job describes what to execute without naming a transport or executor.
type Job struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	Command        string    `json:"command"`
	Args           []string  `json:"args,omitempty"`
	CWD            string    `json:"cwd"`
	Platform       Platform  `json:"platform"`
	Resources      Resources `json:"resources"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	Artifacts      []string  `json:"artifacts,omitempty"`
}

// Validate rejects malformed or unsupported protocol input.
func (j Job) Validate() error {
	if j.Version != ProtocolVersion {
		return fmt.Errorf("unsupported job version %d", j.Version)
	}
	if !validID(j.ID) {
		return errors.New("job id must contain 1 to 128 ASCII letters, digits, underscores, or hyphens")
	}
	if j.Command == "" || len(j.Command) > maxCommandLength {
		return fmt.Errorf("command must contain 1 to %d bytes", maxCommandLength)
	}
	if len(j.Args) > maxArguments {
		return fmt.Errorf("job has too many arguments: maximum is %d", maxArguments)
	}
	total := 0
	for _, arg := range j.Args {
		if len(arg) > maxArgumentLength {
			return fmt.Errorf("argument exceeds %d bytes", maxArgumentLength)
		}
		total += len(arg)
	}
	if total > maxTotalArgBytes {
		return fmt.Errorf("arguments exceed %d bytes in total", maxTotalArgBytes)
	}
	if j.CWD != "." {
		return errors.New("milestone 1 only supports cwd \".\"")
	}
	if j.Platform.OS == "" || j.Platform.Arch == "" {
		return errors.New("job platform os and arch are required")
	}
	if j.Resources.CPU < 1 || j.Resources.MemoryMB < 1 || j.Resources.DiskMB < 1 {
		return errors.New("job cpu, memory_mb, and disk_mb must be positive")
	}
	if j.TimeoutSeconds < 1 || j.TimeoutSeconds > 300 {
		return errors.New("timeout_seconds must be between 1 and 300")
	}
	if len(j.Artifacts) != 0 {
		return errors.New("milestone 1 does not support artifacts")
	}
	return nil
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

// ResourceSnapshot describes host capacity at a point in time.
type ResourceSnapshot struct {
	CPUCores          int `json:"cpu_cores"`
	MemoryTotalMB     int `json:"memory_total_mb,omitempty"`
	MemoryAvailableMB int `json:"memory_available_mb,omitempty"`
}

// ExecutorCapability describes one execution target offered by a worker.
type ExecutorCapability struct {
	Platform  Platform `json:"platform"`
	Isolation string   `json:"isolation"`
}

// WorkerInfo is the worker capability response.
type WorkerInfo struct {
	Name      string               `json:"name"`
	Host      Platform             `json:"host"`
	Resources ResourceSnapshot     `json:"resources"`
	Executors []ExecutorCapability `json:"executors"`
}

// EventType identifies a streamed job event.
type EventType string

const (
	EventStatus     EventType = "status"
	EventStdout     EventType = "stdout"
	EventStderr     EventType = "stderr"
	EventCompletion EventType = "completion"
	EventFailure    EventType = "failure"
)

// Event is one newline-delimited message in a job stream.
type Event struct {
	Type    EventType `json:"type"`
	Data    []byte    `json:"data,omitempty"`
	Status  string    `json:"status,omitempty"`
	Result  *Result   `json:"result,omitempty"`
	Failure *Failure  `json:"failure,omitempty"`
}

// Result contains basic execution telemetry.
type Result struct {
	Worker            string    `json:"worker"`
	StartedAt         time.Time `json:"started_at"`
	EndedAt           time.Time `json:"ended_at"`
	DurationMillis    int64     `json:"duration_ms"`
	ExitCode          int       `json:"exit_code"`
	PeakMemoryBytes   int64     `json:"peak_memory_bytes,omitempty"`
	CPUTimeMillis     int64     `json:"cpu_time_ms,omitempty"`
	BytesUploaded     int64     `json:"bytes_uploaded"`
	BytesDownloaded   int64     `json:"bytes_downloaded"`
	TerminationReason string    `json:"termination_reason"`
}

// Failure describes a failure before a command result is available.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RunRequest starts a job. Workspace data will be added in milestone 2.
type RunRequest struct {
	Job Job `json:"job"`
}

// ErrorResponse is returned for an HTTP-level protocol error.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
