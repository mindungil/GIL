package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// flakyProvider fails the first failsLeft times then succeeds.
type flakyProvider struct {
	failsLeft int
	failErr   error
	calls     int
}

func (f *flakyProvider) Name() string { return "flaky" }
func (f *flakyProvider) Complete(ctx context.Context, req Request) (Response, error) {
	f.calls++
	if f.failsLeft > 0 {
		f.failsLeft--
		return Response{}, f.failErr
	}
	return Response{Text: "ok"}, nil
}

// StreamComplete satisfies the P68c streaming Provider interface; the
// retry tests only need the success/failure shape, so the stream-text
// callback is a no-op here. Retry tests run through Complete anyway.
func (f *flakyProvider) StreamComplete(ctx context.Context, req Request, onText func(string)) (Response, error) {
	return f.Complete(ctx, req)
}

func TestRetry_RetriesTransient(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 2, failErr: errors.New("status 503 service unavailable")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 4, BaseDelay: 1 * time.Millisecond}
	resp, err := r.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Text)
	require.Equal(t, 3, flaky.calls)
}

func TestRetry_GivesUpAfterMax(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 100, failErr: errors.New("503 transient")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}
	_, err := r.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.Equal(t, 3, flaky.calls) // exactly MaxAttempts
}

func TestRetry_NonRetryablePropagatesImmediately(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 100, failErr: errors.New("invalid api key")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 4, BaseDelay: 1 * time.Millisecond}
	_, err := r.Complete(context.Background(), Request{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid api key")
	require.Equal(t, 1, flaky.calls) // no retries
}

func TestRetry_RecognizesRateLimit(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 1, failErr: errors.New("rate_limit_error: too many requests")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}
	_, err := r.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Equal(t, 2, flaky.calls)
}

func TestRetry_ContextCancelledDuringBackoff(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 100, failErr: errors.New("503 timeout")}
	r := &Retry{Wrapped: flaky, MaxAttempts: 5, BaseDelay: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := r.Complete(ctx, Request{})
	require.Error(t, err)
	// Should be ctx error, not the wrapped error
	require.True(t, errors.Is(err, context.DeadlineExceeded), "got: %v", err)
}

func TestRetry_OnRetryFiresOncePerRetry(t *testing.T) {
	// 2 transient failures + 1 success → callback fires twice (once
	// per retry attempt), never on the final success.
	flaky := &flakyProvider{failsLeft: 2, failErr: errors.New("status 503 transient")}
	type call struct {
		attempt, max int
		errMsg       string
		wait         time.Duration
	}
	var calls []call
	r := &Retry{
		Wrapped:     flaky,
		MaxAttempts: 4,
		BaseDelay:   1 * time.Millisecond,
		OnRetry: func(attempt, max int, err error, wait time.Duration) {
			calls = append(calls, call{attempt, max, err.Error(), wait})
		},
	}
	_, err := r.Complete(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, calls, 2, "callback should fire once per retry, not on success")
	require.Equal(t, 1, calls[0].attempt)
	require.Equal(t, 4, calls[0].max)
	require.Contains(t, calls[0].errMsg, "503")
	require.Greater(t, calls[1].wait, calls[0].wait, "delay should grow each retry")
}

func TestRetry_OnRetryNotFiredForNonRetryable(t *testing.T) {
	flaky := &flakyProvider{failsLeft: 1, failErr: errors.New("invalid api key")}
	called := 0
	r := &Retry{
		Wrapped:     flaky,
		MaxAttempts: 4,
		BaseDelay:   1 * time.Millisecond,
		OnRetry:     func(int, int, error, time.Duration) { called++ },
	}
	_, _ = r.Complete(context.Background(), Request{})
	require.Equal(t, 0, called, "non-retryable error should propagate without firing callback")
}

func TestRetry_NameSuffix(t *testing.T) {
	flaky := &flakyProvider{}
	r := NewRetry(flaky)
	require.Equal(t, "flaky+retry", r.Name())
}

func TestIsRetryable(t *testing.T) {
	require.True(t, isRetryable(errors.New("HTTP 503 Service Unavailable")))
	require.True(t, isRetryable(errors.New("connection reset by peer")))
	require.True(t, isRetryable(errors.New("rate_limit_error")))
	require.False(t, isRetryable(errors.New("invalid_request_error: bad model")))
	require.False(t, isRetryable(context.Canceled))
	require.False(t, isRetryable(context.DeadlineExceeded))
	require.False(t, isRetryable(nil))
}
