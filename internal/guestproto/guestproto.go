// Package guestproto defines the control protocol between yonkd and the
// agent running inside a Firecracker microVM.
package guestproto

import "github.com/ekasc/yonk/internal/job"

const (
	// HostCID is the fixed virtio-vsock host context ID.
	HostCID = uint32(2)
	// GuestCID is the context ID assigned to the microVM.
	GuestCID = uint32(3)
	// HostPort is the vsock port the guest agent dials.
	HostPort = uint32(9666)
)

// Message types.
const (
	MsgHello    = "hello"
	MsgLog      = "log"
	MsgJob      = "job"
	MsgStdout   = "stdout"
	MsgStderr   = "stderr"
	MsgCancel   = "cancel"
	MsgResult   = "result"
	MsgArtifact = "artifact"
)

// Message is one newline-delimited JSON control message.
type Message struct {
	Type   string   `json:"type"`
	Job    *job.Job `json:"job,omitempty"`
	Name   string   `json:"name,omitempty"`
	Data   []byte   `json:"data,omitempty"`
	Result *Result  `json:"result,omitempty"`
}

// Result is reported by the guest when the command finishes or is terminated.
type Result struct {
	ExitCode          int    `json:"exit_code"`
	TerminationReason string `json:"termination_reason"`
	DurationMillis    int64  `json:"duration_ms,omitempty"`
	CPUTimeMillis     int64  `json:"cpu_time_ms,omitempty"`
	PeakMemoryBytes   int64  `json:"peak_memory_bytes,omitempty"`
	Message           string `json:"message,omitempty"`
}
