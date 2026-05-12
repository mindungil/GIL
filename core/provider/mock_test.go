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

func TestMockLoop_CyclesPastEnd(t *testing.T) {
	// NewMockLoop wraps idx instead of erroring once exhausted —
	// fixes the dogfood crash where `gil chat --provider mock` died
	// on turn 3 of an open-ended interview because the daemon shipped
	// only 2 hard-coded responses.
	p := NewMockLoop([]string{"alpha", "beta"})
	got := []string{}
	for i := 0; i < 5; i++ {
		resp, err := p.Complete(context.Background(), Request{})
		require.NoError(t, err)
		got = append(got, resp.Text)
	}
	require.Equal(t, []string{"alpha", "beta", "alpha", "beta", "alpha"}, got)
}

func TestMockLoop_EmptyResponsesStillErrors(t *testing.T) {
	// Empty list is a usage error — looping forever over nothing
	// would silently misbehave. Ensure the loop variant still surfaces
	// a clear error in that case.
	p := NewMockLoop(nil)
	_, err := p.Complete(context.Background(), Request{})
	require.Error(t, err)
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
