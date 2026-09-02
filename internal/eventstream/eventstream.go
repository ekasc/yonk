// Package eventstream serializes job events as newline-delimited JSON.
package eventstream

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/ekasc/yonk/internal/job"
)

// Sink serializes events to one writer, optionally flushing after each event.
type Sink struct {
	mu        sync.Mutex
	w         io.Writer
	flush     func()
	dataBytes int64
}

// NewSink returns a sink writing to w. flush may be nil.
func NewSink(w io.Writer, flush func()) *Sink {
	return &Sink{w: w, flush: flush}
}

// Emit writes one event without holding the lock across blocking I/O.
func (s *Sink) Emit(ev job.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode job event: %w", err)
	}
	data = append(data, '\n')
	s.mu.Lock()
	_, writeErr := s.w.Write(data)
	if writeErr == nil {
		s.dataBytes += int64(len(ev.Data))
	}
	flush := s.flush
	s.mu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("encode job event: %w", writeErr)
	}
	if flush != nil {
		flush()
	}
	return nil
}

// DataBytes reports how many data bytes have been emitted.
func (s *Sink) DataBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataBytes
}

// Writer adapts a Sink to io.Writer for a fixed event type.
type Writer struct {
	Sink      *Sink
	EventType job.EventType
}

func (w Writer) Write(p []byte) (int, error) {
	if err := w.Sink.Emit(job.Event{Type: w.EventType, Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}
