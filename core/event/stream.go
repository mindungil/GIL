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
	subs   []*Subscription
}

func NewStream() *Stream {
	return &Stream{}
}

func (s *Stream) Append(e Event) (int64, error) {
	s.mu.Lock()
	if e.ID != 0 {
		s.mu.Unlock()
		return 0, ErrDuplicateID
	}
	s.curID++
	e.ID = s.curID
	s.events = append(s.events, e)
	subs := append([]*Subscription(nil), s.subs...)
	s.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub.ch <- e:
		default:
			// slow consumer drop. TODO Phase 2: dead-letter log
		}
	}
	return e.ID, nil
}

func (s *Stream) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

type Subscription struct {
	ch     chan Event
	mu     sync.Mutex
	closed bool
	stream *Stream
}

func (sub *Subscription) Events() <-chan Event {
	return sub.ch
}

func (sub *Subscription) Close() {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	close(sub.ch)
	sub.stream.removeSubscription(sub)
}

func (s *Stream) Subscribe(buffer int) *Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &Subscription{
		ch:     make(chan Event, buffer),
		stream: s,
	}
	s.subs = append(s.subs, sub)
	return sub
}

func (s *Stream) removeSubscription(target *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sub := range s.subs {
		if sub == target {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			return
		}
	}
}
