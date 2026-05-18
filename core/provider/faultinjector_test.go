package provider

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// P58 — FaultInjector test matrix. Demonstrates each FaultKind +
// composition with NewRetry + script exhaustion + concurrent safety.

func TestFaultInjector_FaultNone_PassesThrough(t *testing.T) {
	wrapped := NewMock([]string{"hello", "world"})
	fi := &FaultInjector{
		Wrapped: wrapped,
		Script: []Fault{
			{Kind: FaultNone},
			{Kind: FaultNone},
		},
	}
	r1, err := fi.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Equal(t, "hello", r1.Text)

	r2, err := fi.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Equal(t, "world", r2.Text)

	require.Equal(t, 2, fi.Consumed())
}

func TestFaultInjector_FaultTransient_DefaultMessageIsRetryable(t *testing.T) {
	// FaultTransient default message contains "503" + "service unavailable"
	// → isRetryable should treat it as retryable.
	fi := &FaultInjector{
		Wrapped: NewMock(nil),
		Script:  []Fault{{Kind: FaultTransient}},
	}
	_, err := fi.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "503")
	require.True(t, isRetryable(err), "default FaultTransient must trip isRetryable")
}

func TestFaultInjector_FaultPermanent_DefaultMessageIsNotRetryable(t *testing.T) {
	fi := &FaultInjector{
		Wrapped: NewMock(nil),
		Script:  []Fault{{Kind: FaultPermanent}},
	}
	_, err := fi.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
	require.False(t, isRetryable(err), "default FaultPermanent must NOT trip isRetryable")
}

func TestFaultInjector_FaultPartial_ReturnsSyntheticResponse(t *testing.T) {
	fi := &FaultInjector{
		Wrapped: NewMock(nil),
		Script: []Fault{{
			Kind: FaultPartial,
			Response: Response{
				Text:         "synthetic body",
				InputTokens:  42,
				OutputTokens: 7,
				StopReason:   "end_turn",
			},
		}},
	}
	r, err := fi.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Equal(t, "synthetic body", r.Text)
	require.Equal(t, int64(42), r.InputTokens)
	require.Equal(t, "end_turn", r.StopReason)
}

func TestFaultInjector_ScriptExhaustedPassesThrough(t *testing.T) {
	wrapped := NewMock([]string{"first", "after"})
	fi := &FaultInjector{
		Wrapped: wrapped,
		Script:  []Fault{{Kind: FaultNone}}, // only 1 slot
	}
	r1, _ := fi.Complete(context.Background(), Request{})
	require.Equal(t, "first", r1.Text)
	// Second call exhausts script → passes through directly to wrapped.
	r2, _ := fi.Complete(context.Background(), Request{})
	require.Equal(t, "after", r2.Text)
}

func TestFaultInjector_NewRetry_RecoversFromInjectedTransient(t *testing.T) {
	// Compose with NewRetry the way the production stack does. 2
	// transient faults then a pass-through; NewRetry should converge.
	wrapped := NewMock([]string{"recovered"})
	fi := &FaultInjector{
		Wrapped: wrapped,
		Script: []Fault{
			{Kind: FaultTransient},
			{Kind: FaultTransient},
			{Kind: FaultNone},
		},
	}
	retried := &Retry{Wrapped: fi, MaxAttempts: 4}
	r, err := retried.Complete(context.Background(), Request{})
	require.NoError(t, err, "Retry must recover from 2 transient injections")
	require.Equal(t, "recovered", r.Text)
	require.Equal(t, 3, fi.Consumed(), "3 fault-slots consumed: 2 fail + 1 success")
}

func TestFaultInjector_NewRetry_GivesUpOnPermanent(t *testing.T) {
	fi := &FaultInjector{
		Wrapped: NewMock([]string{"never"}),
		Script:  []Fault{{Kind: FaultPermanent}},
	}
	retried := &Retry{Wrapped: fi, MaxAttempts: 4}
	_, err := retried.Complete(context.Background(), Request{})
	require.Error(t, err, "permanent faults must NOT be retried")
	require.Equal(t, 1, fi.Consumed(), "only 1 fault-slot consumed; no retry")
}

func TestFaultInjector_Reset_RewindsScript(t *testing.T) {
	fi := &FaultInjector{
		Wrapped: NewMock([]string{"a", "b", "c", "d"}),
		Script:  []Fault{{Kind: FaultNone}, {Kind: FaultNone}},
	}
	_, _ = fi.Complete(context.Background(), Request{})
	_, _ = fi.Complete(context.Background(), Request{})
	require.Equal(t, 2, fi.Consumed())

	fi.Reset()
	require.Equal(t, 0, fi.Consumed())

	// Mock has been advanced past its first 2 — Reset doesn't rewind
	// the underlying mock. Consuming next slot via FaultNone advances
	// the mock to its 3rd response.
	r, _ := fi.Complete(context.Background(), Request{})
	require.Equal(t, "c", r.Text)
}

func TestFaultInjector_Name_AppendsSuffix(t *testing.T) {
	fi := &FaultInjector{Wrapped: NewMock(nil)}
	require.Equal(t, "mock+fault", fi.Name())
}

func TestFaultInjector_Concurrent_EachSlotConsumedAtMostOnce(t *testing.T) {
	// 100 concurrent Complete calls against a 100-slot script (all
	// FaultNone passing through to a 100-response mock). Each script
	// slot consumed exactly once; no duplicate slots; total = 100.
	const N = 100
	responses := make([]string, N)
	for i := 0; i < N; i++ {
		responses[i] = "r"
	}
	wrapped := NewMock(responses)
	script := make([]Fault, N)
	for i := 0; i < N; i++ {
		script[i].Kind = FaultNone
	}
	fi := &FaultInjector{Wrapped: wrapped, Script: script}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = fi.Complete(context.Background(), Request{})
		}()
	}
	wg.Wait()

	require.Equal(t, N, fi.Consumed(),
		"each script slot must be consumed by exactly one Complete call")
}

func TestFaultInjector_CustomMessage_IsHonored(t *testing.T) {
	fi := &FaultInjector{
		Wrapped: NewMock(nil),
		Script: []Fault{
			{Kind: FaultTransient, Message: "custom upstream borked"},
			{Kind: FaultPermanent, Message: "custom auth refused"},
		},
	}
	_, e1 := fi.Complete(context.Background(), Request{})
	require.True(t, strings.Contains(e1.Error(), "custom upstream borked"))
	_, e2 := fi.Complete(context.Background(), Request{})
	require.True(t, strings.Contains(e2.Error(), "custom auth refused"))
}
