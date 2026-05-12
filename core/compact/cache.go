package compact

import "github.com/mindungil/gil/core/provider"

// MarkCacheBreakpoints applies the Anthropic "system-and-3" cache control
// strategy to a message slice. It MUTATES the slice in place — callers that
// need the originals preserved must pass a copy.
//
// Strategy:
//  1. The system block is marked separately by the caller via
//     Request.SystemCacheControl (this function does not touch Request).
//  2. The LAST 3 messages have CacheControl=true.
//  3. All other messages have CacheControl=false.
//
// With fewer than 3 messages, marks every message present.
//
// Returns the same slice for fluent use.
func MarkCacheBreakpoints(msgs []provider.Message) []provider.Message {
	// Clear all first (idempotent — repeated calls always yield the same shape)
	for i := range msgs {
		msgs[i].CacheControl = false
	}
	// Mark last 3 (or all if fewer)
	n := len(msgs)
	start := n - 3
	if start < 0 {
		start = 0
	}
	for i := start; i < n; i++ {
		msgs[i].CacheControl = true
	}
	return msgs
}
