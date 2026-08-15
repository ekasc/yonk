// Package eventstream serializes job events as newline-delimited JSON.
package eventstream

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/ekassinghchhabra/yonk/internal/job"
)

// Sink serializes events to one writer, optionally flushing after each event.
type Sink struct {
	mu        sync.Mutex
	enc       *json.Encoder
	flush     func()
	dataBytes int64
}

// NewSink returns a sink writing to w. flush may be nil.
func NewSink(w io.Writer, flush func()) *Sink {
	return &Sink{enc: json.NewEncoder(w), flush: flush}
}

// Emit writes one event.
func (s *Sink) Emit(ev job.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(ev); err != nil {
		return fmt.Errorf("encode job event: %w", err)
	}
	s.dataBytes += int64(len(ev.Data))
	if s.flush != nil {
		s.flush()
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
