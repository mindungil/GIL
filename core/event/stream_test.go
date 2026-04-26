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
