package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMock_ScriptedResponses(t *testing.T) {
	p := NewMock([]string{"hello", "world"})

	resp1, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	require.NoError(t, err)
	require.Equal(t, "hello", resp1.Text)

	resp2, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi again"}}})
	require.NoError(t, err)
	require.Equal(t, "world", resp2.Text)

	// exhausted
	_, err = p.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "exhausted"))
}

func TestMock_Name(t *testing.T) {
	p := NewMock(nil)
	require.Equal(t, "mock", p.Name())
}

func TestMock_ConcurrentSafe(t *testing.T) {
	p := NewMock([]string{"a", "b", "c", "d"})
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, _ = p.Complete(context.Background(), Request{})
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	// Total of 4 calls — index moved to 4
	_, err := p.Complete(context.Background(), Request{})
	require.Error(t, err) // exhausted
}
