package provider

// Failure-injection harness for the Retry + OpenAI adapter pair.
//
// The unit tests in retry_test.go exercise Retry against an in-process
// stub provider; this file exercises the same wrapper but plumbed through
// the real OpenAI adapter (which means we go through net/http, the typed
// error classifier, and the Retry-After header parser). The point is to
// catch regressions in the *interaction* between layers — e.g. an OpenAI
// error type that no longer matches isRetryable, a Retry-After header
// that gets ignored, a network-level dial failure that the substring
// matcher mistakenly classifies permanent.
//
// Every case spins httptest.NewServer (or pretends to, for the dial-fail
// case) so nothing reaches the real internet. The whole file is fast: the
// only deliberately-slow scenarios are the Retry-After honour test and
// the network timeout test, both bounded to ~1.5s of wall time.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// helper: minimal valid OpenAI chat.completions response body.
const okBody = `{
    "choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
    "usage":{"prompt_tokens":1,"completion_tokens":1}
}`

// scriptedServer returns an httptest.Server whose handler runs steps
// sequentially: the first request triggers steps[0], the second triggers
// steps[1], and so on. If the request count exceeds len(steps) the last
// step is reused (handy for "always 503" cases).
type step func(w http.ResponseWriter, r *http.Request)

func scriptedServer(steps ...step) (*httptest.Server, *int64) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt64(&calls, 1)) - 1
		if idx >= len(steps) {
			idx = len(steps) - 1
		}
		steps[idx](w, r)
	}))
	return srv, &calls
}

// newRetryThroughOpenAI builds an OpenAI adapter pointed at srv, wraps it
// in Retry with the given attempts and a tiny base delay, and returns the
// wrapper. Tests that need to override the http client (timeout cases) can
// poke o.HTTP after construction.
func newRetryThroughOpenAI(srvURL string, attempts int) (*OpenAI, *Retry) {
	o := NewOpenAI("test-key", srvURL)
	o.HTTP = &http.Client{Timeout: 5 * time.Second}
	r := &Retry{Wrapped: o, MaxAttempts: attempts, BaseDelay: 1 * time.Millisecond}
	return o, r
}

func sampleReq() Request {
	return Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}
}

// 1. control: 200 passes through, exactly one call, response parsed.
func TestRetryInjection_2xxPassThrough(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(okBody))
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	resp, err := r.Complete(context.Background(), sampleReq())
	require.NoError(t, err)
	require.Equal(t, "hello", resp.Text)
	require.EqualValues(t, 1, atomic.LoadInt64(calls))
}

// 2. one transient then success: should retry once and succeed.
func TestRetryInjection_503OnceThen200(t *testing.T) {
	srv, calls := scriptedServer(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"down for maintenance"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(okBody))
		},
	)
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	resp, err := r.Complete(context.Background(), sampleReq())
	require.NoError(t, err)
	require.Equal(t, "hello", resp.Text)
	require.EqualValues(t, 2, atomic.LoadInt64(calls))
}

// 3. always-503 with attempts=3: should give up, return wrapped transient
// error, NOT panic / NOT recurse forever, exactly 3 calls.
func TestRetryInjection_503AlwaysExhaustsBudget(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"persistent outage"}`))
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 3)
	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	var tr *ProviderTransient
	require.True(t, errors.As(err, &tr), "expected ProviderTransient, got %T: %v", err, err)
	require.Equal(t, 503, tr.StatusCode)
	require.EqualValues(t, 3, atomic.LoadInt64(calls), "should not exceed MaxAttempts")
}

// 4. 429 with Retry-After: 1 — Retry must wait at least 1s before the
// next attempt. We set BaseDelay tiny on purpose so any "≥1s" elapsed
// proves Retry-After (not BaseDelay) is what governed the wait.
func TestRetryInjection_429HonorsRetryAfterSeconds(t *testing.T) {
	srv, calls := scriptedServer(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(okBody))
		},
	)
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	start := time.Now()
	_, err := r.Complete(context.Background(), sampleReq())
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.GreaterOrEqual(t, elapsed, 1*time.Second, "Retry-After: 1 must yield ≥1s wait")
	require.Less(t, elapsed, 3*time.Second, "should not wait absurdly long")
	require.EqualValues(t, 2, atomic.LoadInt64(calls))
}

// 4b. Retry-After in HTTP-date form. We pick a date 3s in the future
// (the IMF-fixdate format only has second precision, so a 1s-in-the-
// future date can round down to ~0s remaining by the time the parser
// runs). The lower bound of 1.5s is comfortably above the noise floor
// while still well under the 3s the server requested. Locks down the
// date-form parser end-to-end.
func TestRetryInjection_429HonorsRetryAfterHTTPDate(t *testing.T) {
	srv, calls := scriptedServer(
		func(w http.ResponseWriter, r *http.Request) {
			when := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
			w.Header().Set("Retry-After", when)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(okBody))
		},
	)
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	start := time.Now()
	_, err := r.Complete(context.Background(), sampleReq())
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.GreaterOrEqual(t, elapsed, 1500*time.Millisecond,
		"HTTP-date Retry-After ~3s in the future should yield a multi-second wait")
	require.Less(t, elapsed, 5*time.Second, "and shouldn't wait absurdly long")
	require.EqualValues(t, 2, atomic.LoadInt64(calls))
}

// 5. 429 without Retry-After — should fall back to BaseDelay-driven
// exponential backoff. Total time must be far below what a Retry-After
// hint would have imposed.
func TestRetryInjection_429NoRetryAfterFallsBackToBackoff(t *testing.T) {
	srv, calls := scriptedServer(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(okBody))
		},
	)
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	start := time.Now()
	_, err := r.Complete(context.Background(), sampleReq())
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Less(t, elapsed, 250*time.Millisecond,
		"with no Retry-After and BaseDelay=1ms the second attempt should be near-instant")
	require.EqualValues(t, 2, atomic.LoadInt64(calls))
}

// 6. Network timeout: server hangs, http.Client.Timeout fires. The
// resulting error contains "timeout" so isRetryable matches and Retry
// burns the full attempt budget. This case used to misclassify as
// caller-cancellation because http.Client.Timeout produces an error that
// wraps context.DeadlineExceeded — Retry now distinguishes the two by
// checking ctx.Err() directly.
func TestRetryInjection_NetworkTimeoutIsTransient(t *testing.T) {
	gate := make(chan struct{})
	// IMPORTANT: release the gate BEFORE srv.Close, otherwise httptest's
	// internal WaitGroup (one entry per in-flight handler) blocks Close
	// for ~5s and prints "blocked in Close after 5 seconds". t.Cleanup
	// runs in LIFO order so the cleanup registered LAST runs FIRST.
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		<-gate // park until cleanup releases us
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(gate) })

	o := NewOpenAI("k", srv.URL)
	o.HTTP = &http.Client{Timeout: 80 * time.Millisecond}
	r := &Retry{Wrapped: o, MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}

	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	require.EqualValues(t, 3, atomic.LoadInt64(&calls),
		"all three attempts should fire — each times out independently")
	// Sanity-check the wording so a future Go release that changes the
	// error text fails this test instead of silently regressing Retry.
	require.Contains(t, err.Error(), "Client.Timeout")
}

// 7. Connection refused: server is closed before Complete runs, so the
// dial fails. Pre-fix this was misclassified permanent because the error
// string didn't match any known substring. Locks down the fix.
func TestRetryInjection_ConnectionRefusedIsTransient(t *testing.T) {
	// Reserve a port by listening then closing — practically guarantees
	// the next dial will see ECONNREFUSED. Using httptest.NewServer +
	// immediate Close() does the same job and is closer to the spec.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	o := NewOpenAI("k", url)
	o.HTTP = &http.Client{Timeout: 1 * time.Second}
	r := &Retry{Wrapped: o, MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}

	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	require.True(t, isRetryable(err),
		"connection refused must be classified retryable, got: %v", err)
	// And the error should carry network signal so the user can debug.
	require.Contains(t, err.Error(), "refused")
}

// 8. 400 Bad Request — permanent, no retry, exactly one call.
func TestRetryInjection_400IsPermanent(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad model"}`))
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	var pe *ProviderPermanent
	require.True(t, errors.As(err, &pe), "expected ProviderPermanent, got %T: %v", err, err)
	require.Equal(t, 400, pe.StatusCode)
	require.EqualValues(t, 1, atomic.LoadInt64(calls), "must not retry permanent 4xx")
}

// 9. 401 Unauthorized — permanent, no retry, exactly one call.
// (Bug guard: a buggy substring matcher could see "unauthorized" → "auth"
// and misclassify; assert it doesn't.)
func TestRetryInjection_401IsPermanent(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	var pe *ProviderPermanent
	require.True(t, errors.As(err, &pe))
	require.Equal(t, 401, pe.StatusCode)
	require.EqualValues(t, 1, atomic.LoadInt64(calls))
}

// 10. Malformed JSON in a 200 response. SEMANTICS: we treat this as
// permanent — the upstream gave us a 200 (so this isn't a transport-level
// flake) but the body doesn't parse. Retrying is unlikely to help and
// the error message is useful for debugging an upstream that's returning
// HTML where JSON was expected. Locks down that policy.
func TestRetryInjection_MalformedJSONIsPermanent(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	require.False(t, isRetryable(err), "malformed-JSON 200 should not be retried")
	require.EqualValues(t, 1, atomic.LoadInt64(calls))
}

// 11. Empty 200 body. Same family as malformed JSON; the adapter rejects
// it (no choices) and the error propagates. Documents the chosen
// behaviour: don't burn the retry budget on a clearly broken upstream.
func TestRetryInjection_EmptyBodyIsPermanent(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 4)
	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	require.False(t, isRetryable(err), "empty 200 body should not be retried")
	require.EqualValues(t, 1, atomic.LoadInt64(calls))
}

// 12. Truncated body via Hijacker: the server promises Content-Length:N
// then writes fewer bytes and closes the connection. The client sees an
// "unexpected EOF" while reading the body — that contains "eof" so
// isRetryable matches and Retry burns its budget trying again. Locks
// down the EOF-as-transient policy.
func TestRetryInjection_TruncatedBodyIsTransient(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not implement Hijacker")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		// Content-Length lies — promise 200 bytes, send 5, close.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 200\r\n\r\n")
		_, _ = buf.WriteString(`{"ch`)
		_ = buf.Flush()
		_ = conn.Close()
	})
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	// Disable HTTP/2 keep-alive shenanigans by giving the client a
	// disposable transport — keeps each attempt independent.
	o.HTTP = &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	r := &Retry{Wrapped: o, MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}

	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	require.True(t, isRetryable(err), "truncated body / unexpected EOF should be retryable, got: %v", err)
	require.EqualValues(t, 3, atomic.LoadInt64(calls), "should burn full retry budget on transient EOF")
}

// 13. Cancel ctx mid-retry: the first attempt fails with 503, then while
// Retry is sleeping we cancel ctx. Retry must return ctx.Err quickly and
// must NOT make a second upstream attempt.
func TestRetryInjection_CtxCancelMidBackoff(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"down"}`))
	})
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	o.HTTP = &http.Client{Timeout: 1 * time.Second}
	// BaseDelay long enough that we're definitely sleeping when cancel hits.
	r := &Retry{Wrapped: o, MaxAttempts: 5, BaseDelay: 500 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := r.Complete(ctx, sampleReq())
	elapsed := time.Since(start)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled), "expected ctx.Canceled, got: %v", err)
	require.Less(t, elapsed, 400*time.Millisecond, "should bail quickly, not wait full BaseDelay")
	// Exactly one upstream call: the initial attempt that returned 503.
	require.EqualValues(t, 1, atomic.LoadInt64(calls), "must not retry after ctx cancel")
}

// 14. MaxAttempts is honoured: with attempts=3 and a server that always
// fails, we see exactly 3 calls — never 4, never an infinite loop.
// Mirrors case 3 but tightens the assertion: this is the explicit
// "no-busy-loop" guard.
func TestRetryInjection_MaxAttemptsHonored(t *testing.T) {
	srv, calls := scriptedServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	defer srv.Close()

	_, r := newRetryThroughOpenAI(srv.URL, 3)
	_, err := r.Complete(context.Background(), sampleReq())
	require.Error(t, err)
	require.EqualValues(t, 3, atomic.LoadInt64(calls))

	// And once more with attempts=1 — should be a single call, no retry sleep.
	atomic.StoreInt64(calls, 0)
	_, r2 := newRetryThroughOpenAI(srv.URL, 1)
	start := time.Now()
	_, err = r2.Complete(context.Background(), sampleReq())
	elapsed := time.Since(start)
	require.Error(t, err)
	require.EqualValues(t, 1, atomic.LoadInt64(calls), "attempts=1 means no retry")
	require.Less(t, elapsed, 200*time.Millisecond, "single attempt should not sleep")
}

// --- focused unit tests for the Retry-After parser ------------------------
//
// These don't go through HTTP — they hit parseRetryAfter directly. The
// integration cases above prove the value flows end-to-end; this table
// proves the parser handles every documented form (and the sketchy ones).

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration // approximate; HTTP-date check uses ≥
	}{
		{"empty", "", 0},
		{"whitespace", "   ", 0},
		{"zero seconds", "0", 0},
		{"negative seconds", "-5", 0},
		{"one second", "1", 1 * time.Second},
		{"large delta", "120", 120 * time.Second},
		{"non-integer junk", "soon", 0},
		{"past HTTP-date", "Wed, 21 Oct 2015 07:28:00 GMT", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, parseRetryAfter(c.in))
		})
	}

	// future HTTP-date: separate because want is approximate.
	when := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(when)
	require.Greater(t, got, 500*time.Millisecond)
	require.LessOrEqual(t, got, 3*time.Second)
}

// --- defence-in-depth: dialErrorHelper -------------------------------------
//
// Belt-and-braces check that the host-OS error text for a refused dial
// actually contains a substring isRetryable knows about. If the platform
// ever changes the wording (it has happened in past Go releases) this
// test fails loudly so we can update the substring list.
func TestRetryInjection_DialRefusedSubstring(t *testing.T) {
	// Pick a port that's definitely closed: bind, capture, close, re-dial.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	_, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	require.Error(t, derr)
	require.True(t, isRetryable(derr),
		"isRetryable must recognise the OS dial-refused message: %q", derr.Error())
}
