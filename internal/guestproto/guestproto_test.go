package guestproto

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ekasc/yonk/internal/job"
)

func TestMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	dec := json.NewDecoder(&buf)

	spec := job.Job{Version: job.ProtocolVersion, ID: "job_test", Command: "echo", Args: []string{"hi"}, CWD: "."}
	original := Message{Type: MsgJob, Job: &spec}
	if err := enc.Encode(original); err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Type != MsgJob || got.Job == nil || got.Job.Command != "echo" {
		t.Fatalf("decoded message = %+v", got)
	}
}

func TestResultBinaryData(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(Message{Type: MsgStdout, Data: []byte("line\nwith \x00 binary")}); err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "line\nwith \x00 binary" {
		t.Fatalf("data = %q", got.Data)
	}
}
