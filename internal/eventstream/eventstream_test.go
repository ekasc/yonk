package eventstream

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ekasc/yonk/internal/job"
)

func TestSinkAndWriter(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink(&buf, nil)
	writer := Writer{Sink: sink, EventType: job.EventStdout}
	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if sink.DataBytes() != int64(len("hello\n")) {
		t.Fatalf("DataBytes() = %d", sink.DataBytes())
	}
	var event job.Event
	if err := json.NewDecoder(&buf).Decode(&event); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if event.Type != job.EventStdout || string(event.Data) != "hello\n" {
		t.Fatalf("event = %+v", event)
	}
}
