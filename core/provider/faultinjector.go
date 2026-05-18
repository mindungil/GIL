package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// FaultInjector wraps a Provider and injects scripted faults so the
// retry / verify / stuck-recovery paths can be exercised live without
// waiting for organic upstream errors. Compose with NewRetry the same
// way RunService does its real provider:
//
//	prov := provider.NewMockToolProvider(turns)
//	prov  = &provider.FaultInjector{Wrapped: prov, Script: []provider.Fault{...}}
//	prov  = provider.NewRetry(prov)
//
// The Script is consumed left-to-right. Each Fault either passes through
// (FaultNone), errors retryable (FaultTransient), errors permanent
// (FaultPermanent), or short-circuits with a synthetic Response
// (FaultPartial — useful for "model returned an empty response" tests).
// When the script is exhausted, the wrapper passes through forever.
//
// Goroutine-safe: idx is atomic so concurrent Complete callers each take
// a distinct script slot.
type FaultInjector struct {
	Wrapped Provider
	Script  []Fault

	idx atomic.Int64
	mu  sync.Mutex // guards the synthetic-response branch (which allocates)
}

// FaultKind enumerates the failure shapes the injector can produce on
// a given Complete call.
type FaultKind int

const (
	// FaultNone is a pass-through — the wrapped provider runs and its
	// response is returned as-is.
	FaultNone FaultKind = iota
	// FaultTransient returns a retryable error. The wrapped provider is
	// NOT invoked. Use to exercise the NewRetry backoff path.
	FaultTransient
	// FaultPermanent returns a non-retryable error. Use to exercise the
	// "give up cleanly" path.
	FaultPermanent
	// FaultPartial short-circuits with a synthetic Response — useful for
	// "model returned empty text" or "model returned no tool calls"
	// scenarios that wouldn't naturally surface from a mock.
	FaultPartial
)

// Fault is one scripted call's behavior.
type Fault struct {
	Kind FaultKind
	// Message overrides the default error text. Empty → default per kind.
	Message string
	// Response is used only when Kind=FaultPartial. Ignored otherwise.
	Response Response
}

// Name implements Provider. Adds a "+fault" suffix so the un-wrapped
// provider's name stays inspectable in logs.
func (f *FaultInjector) Name() string {
	if f.Wrapped == nil {
		return "fault"
	}
	return f.Wrapped.Name() + "+fault"
}

// Complete pulls the next Fault from the script. On FaultNone it
// delegates to Wrapped. On FaultTransient/Permanent it returns the
// scripted error. On FaultPartial it returns the scripted Response.
// When the script is exhausted (idx >= len(Script)), all subsequent
// calls pass through.
func (f *FaultInjector) Complete(ctx context.Context, req Request) (Response, error) {
	i := f.idx.Add(1) - 1
	if int(i) >= len(f.Script) {
		// Script exhausted — pass through forever.
		if f.Wrapped == nil {
			return Response{}, errors.New("faultinjector: no wrapped provider")
		}
		return f.Wrapped.Complete(ctx, req)
	}
	fault := f.Script[i]
	switch fault.Kind {
	case FaultNone:
		if f.Wrapped == nil {
			return Response{}, errors.New("faultinjector: FaultNone with nil wrapped")
		}
		return f.Wrapped.Complete(ctx, req)
	case FaultTransient:
		msg := fault.Message
		if msg == "" {
			msg = "fault injection: transient 503 service unavailable"
		}
		return Response{}, errors.New(msg)
	case FaultPermanent:
		msg := fault.Message
		if msg == "" {
			msg = "fault injection: 401 unauthorized"
		}
		return Response{}, errors.New(msg)
	case FaultPartial:
		f.mu.Lock()
		out := fault.Response
		f.mu.Unlock()
		return out, nil
	default:
		// Unknown kind treats as pass-through.
		if f.Wrapped == nil {
			return Response{}, errors.New("faultinjector: unknown fault kind + nil wrapped")
		}
		return f.Wrapped.Complete(ctx, req)
	}
}

// Reset rewinds the script index to 0. Useful for tests that reuse a
// FaultInjector across subtests without reallocating the script.
func (f *FaultInjector) Reset() { f.idx.Store(0) }

// Consumed returns how many script slots have been consumed so far.
// Lets tests assert "exactly N faults fired" without inspecting state
// directly.
func (f *FaultInjector) Consumed() int { return int(f.idx.Load()) }
