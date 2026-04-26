package event

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStream_Append_AssignsIDs(t *testing.T) {
	s := NewStream()

	id1, err := s.Append(Event{Type: "test", Timestamp: time.Now()})
	require.NoError(t, err)
	require.Equal(t, int64(1), id1)

	id2, err := s.Append(Event{Type: "test", Timestamp: time.Now()})
	require.NoError(t, err)
	require.Equal(t, int64(2), id2)
}

func TestStream_Append_DuplicateIDFails(t *testing.T) {
	s := NewStream()

	_, err := s.Append(Event{ID: 5, Type: "test"})
	require.Error(t, err)
}

func TestStream_Len(t *testing.T) {
	s := NewStream()
	require.Equal(t, 0, s.Len())

	_, err := s.Append(Event{Type: "test"})
	require.NoError(t, err)
	require.Equal(t, 1, s.Len())

	_, err = s.Append(Event{Type: "test"})
	require.NoError(t, err)
	require.Equal(t, 2, s.Len())
}

func TestStream_Subscribe_ReceivesAppendedEvents(t *testing.T) {
	s := NewStream()
	sub := s.Subscribe(10)
	defer sub.Close()

	go func() {
		s.Append(Event{Type: "first"})
		s.Append(Event{Type: "second"})
	}()

	got1 := <-sub.Events()
	got2 := <-sub.Events()
	require.Equal(t, "first", got1.Type)
	require.Equal(t, "second", got2.Type)
}

func TestStream_Subscribe_MultipleSubscribers(t *testing.T) {
	s := NewStream()
	sub1 := s.Subscribe(5)
	defer sub1.Close()
	sub2 := s.Subscribe(5)
	defer sub2.Close()

	s.Append(Event{Type: "broadcast"})

	r1 := <-sub1.Events()
	r2 := <-sub2.Events()
	require.Equal(t, "broadcast", r1.Type)
	require.Equal(t, "broadcast", r2.Type)
}
