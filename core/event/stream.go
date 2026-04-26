package event

import (
	"errors"
	"sync"
)

var ErrDuplicateID = errors.New("event already has ID assigned")

type Stream struct {
	mu     sync.Mutex
	events []Event
	curID  int64
}

func NewStream() *Stream {
	return &Stream{}
}

func (s *Stream) Append(e Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e.ID != 0 {
		return 0, ErrDuplicateID
	}
	s.curID++
	e.ID = s.curID
	s.events = append(s.events, e)
	return e.ID, nil
}

// Len returns the number of events currently in the stream.
func (s *Stream) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}
