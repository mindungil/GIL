package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Retry wraps a Provider and retries transient errors (5xx, timeouts,
// rate limits) with exponential backoff. Non-retryable errors (e.g., auth
// failures, bad requests) are propagated immediately.
type Retry struct {
	Wrapped     Provider
	MaxAttempts int           // total attempts (1 = no retry); default 4
	BaseDelay   time.Duration // initial backoff; default 500ms; doubles each attempt
}

// NewRetry returns a Retry around inner with sensible defaults
// (4 attempts, 500ms initial backoff).
func NewRetry(inner Provider) *Retry {
	return &Retry{Wrapped: inner, MaxAttempts: 4, BaseDelay: 500 * time.Millisecond}
}

// Name implements Provider.
func (r *Retry) Name() string { return r.Wrapped.Name() + "+retry" }

// Complete tries the wrapped provider up to MaxAttempts times, with
// exponential backoff between attempts on retryable errors. Returns the
// last error if all attempts fail. Respects ctx cancellation during waits.
func (r *Retry) Complete(ctx context.Context, req Request) (Response, error) {
	max := r.MaxAttempts
	if max <= 0 {
		max = 4
	}
	delay := r.BaseDelay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}

	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		resp, err := r.Wrapped.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return resp, err
		}
		if attempt == max {
			break
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
		delay *= 2
	}
	return Response{}, lastErr
}

// isRetryable returns true if err looks like a transient HTTP/network/rate-limit
// failure that's worth retrying. Caller-cancellations are not retryable.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range []string{
		"500", "502", "503", "504", "529",
		"timeout", "connection reset", "eof",
		"overloaded", "rate_limit", "rate limit",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
