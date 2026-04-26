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
