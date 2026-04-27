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
//
// If the wrapped provider returns a typed error carrying a server-supplied
// Retry-After hint (*ProviderRateLimit / *ProviderTransient), that hint
// supersedes the exponential delay for that one attempt — but never below
// the configured BaseDelay floor and always interruptible via ctx.
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
		// If the caller's own ctx is done, that's a cancellation —
		// surface it directly and don't retry. We check ctx.Err() rather
		// than relying on errors.Is(err, context.DeadlineExceeded) on the
		// returned error because http.Client.Timeout produces an error
		// that ALSO satisfies errors.Is(_, context.DeadlineExceeded)
		// when the request carried a ctx, even though the upstream
		// caller never cancelled — which would otherwise misclassify a
		// genuine network timeout as caller cancellation.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, ctxErr
		}
		if !isRetryable(err) {
			return resp, err
		}
		if attempt == max {
			break
		}
		wait := delay
		if hint := retryAfterHint(err); hint > wait {
			wait = hint
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
		delay *= 2
	}
	return Response{}, lastErr
}

// retryAfterHint pulls a server-supplied Retry-After delay off a typed
// provider error, if present. Returns 0 when the error type doesn't carry
// one (or the upstream didn't send the header). Both *ProviderRateLimit
// and *ProviderTransient may carry the hint — some 503s use it for
// maintenance windows, not just 429s.
func retryAfterHint(err error) time.Duration {
	var rl *ProviderRateLimit
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		return rl.RetryAfter
	}
	var tr *ProviderTransient
	if errors.As(err, &tr) && tr.RetryAfter > 0 {
		return tr.RetryAfter
	}
	return 0
}

// isRetryable returns true if err looks like a transient HTTP/network/rate-limit
// failure that's worth retrying.
//
// Note on ctx errors: callers that have access to the live context should
// short-circuit on ctx.Err() *before* calling isRetryable — see Retry.Complete.
// We deliberately don't reject errors that wrap context.DeadlineExceeded
// here, because http.Client.Timeout produces such an error even when the
// caller's ctx never expired, and we want those treated as retryable
// network timeouts. The substring matcher below catches the "Client.Timeout"
// wording; raw context.Canceled / context.DeadlineExceeded (which read
// "context canceled" / "context deadline exceeded") match no entry in the
// retryable list and so still return false.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range []string{
		"500", "502", "503", "504", "529",
		"timeout", "connection reset", "connection refused",
		"no such host", "network is unreachable", "broken pipe",
		"eof", "unexpected eof",
		"overloaded", "rate_limit", "rate limit",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
