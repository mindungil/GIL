package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mindungil/gil/core/session"
)

// subagent_registry.go — S3 of docs/design/subagent.md. The
// daemon-wide registry that enforces the *system* safety net while
// the agent stays in charge of *what* to delegate: per-root concurrent
// count and absolute depth. Spec-level caps (max_iterations_per_subagent,
// etc.) live on the FrozenSpec.Subagent slot and are applied by
// subagent_slice.go.
//
// We deliberately keep this in-memory only (V1). spawn count is a
// liveness signal, not a durable claim; on daemon restart the counts
// reset to 0 and any orphan children are reaped by the existing
// session.Repo "stuck running" path.

const (
	// subagentMaxPerRoot caps the total number of concurrently active
	// subagents per root user-session. codex uses a comparable limit
	// (per-session "active_agents" registry, ~10). 8 is the V1 default;
	// can be moved to a config slot later if dogfood needs it.
	subagentMaxPerRoot = 8
	// subagentMaxDepth caps recursion (parent → child → grandchild …).
	// V1 hard cap was 1 (root can spawn children but children cannot
	// spawn further). P40 lifts to 2 so a top-level coordinator can
	// spawn sub-coordinators that themselves spawn workers — the natural
	// shape of complex autonomous work (e.g. "split a refactor into
	// per-module subagents, each of which farms out per-file edits").
	// Per-root fork-bomb safety is still subagentMaxPerRoot=8 across
	// the whole tree; per-session budget caps are still enforced via
	// the spec's Budget.MaxTotalTokens / MaxCostUSD. Going beyond
	// depth=2 would need a per-depth concurrent cap to stay safe;
	// future phase.
	subagentMaxDepth = 2
)

// errSubagentLimitReached is returned to spawn_agent when the registry
// is at capacity. The agent tool surfaces it as an IsError ToolResult so
// the LLM can choose to retry (after wait_agent reduces the count) or
// fold the work into the parent's own turns.
var errSubagentLimitReached = errors.New("subagent limit reached")

// errSubagentDepthExceeded blocks a subagent from spawning further
// subagents past the V1 depth cap.
var errSubagentDepthExceeded = errors.New("subagent depth exceeded")

// subagentRegistry tracks active child counts per root session. The
// "root" of any session chain is determined by walking parent links —
// session.Repo doesn't enforce that root == ParentSessionID="", so the
// registry needs a small helper.
type subagentRegistry struct {
	mu             sync.Mutex
	activePerRoot  map[string]int // rootSessionID → active count
	maxPerRoot     int
	maxDepth       int32
}

func newSubagentRegistry() *subagentRegistry {
	return &subagentRegistry{
		activePerRoot: make(map[string]int),
		maxPerRoot:    subagentMaxPerRoot,
		maxDepth:      subagentMaxDepth,
	}
}

// spawn checks limits and bumps the count. parentSess must be the
// parent session (already loaded — caller is the spawn_agent tool which
// already did the Get to validate). Returns the rootID so the tool
// can pass it to the child's CreateInput, and a release closure the
// tool should defer-call once the child reaches a terminal state
// (called from wait_agent).
//
// The depth check is on the *child's* would-be depth = parent.depth+1;
// V1 maxDepth=1 means parent must be root (depth=0) for spawn to
// succeed. Future relax: bump maxDepth or read from spec.
func (r *subagentRegistry) spawn(ctx context.Context, repo *session.Repo, parentSess session.Session) (rootID string, release func(), err error) {
	childDepth := parentSess.SubagentDepth + 1
	if childDepth > r.maxDepth {
		return "", nil, fmt.Errorf("%w: maxDepth=%d, child would be at %d",
			errSubagentDepthExceeded, r.maxDepth, childDepth)
	}

	rootID, err = resolveRootSessionID(ctx, repo, parentSess)
	if err != nil {
		return "", nil, fmt.Errorf("subagent.spawn resolve root: %w", err)
	}

	r.mu.Lock()
	if r.activePerRoot[rootID] >= r.maxPerRoot {
		r.mu.Unlock()
		return "", nil, fmt.Errorf("%w: %d active under root %s (max %d)",
			errSubagentLimitReached, r.activePerRoot[rootID], rootID, r.maxPerRoot)
	}
	r.activePerRoot[rootID]++
	r.mu.Unlock()

	return rootID, func() { r.release(rootID) }, nil
}

// release decrements the per-root count. Defensive: never go below 0
// even if called twice (the spawn_agent + wait_agent path are async and
// could race). Caller from wait_agent uses sync.Once to avoid double
// call.
func (r *subagentRegistry) release(rootID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.activePerRoot[rootID]; ok {
		if c <= 1 {
			delete(r.activePerRoot, rootID)
		} else {
			r.activePerRoot[rootID] = c - 1
		}
	}
}

// activeCount returns the current count under root. Used by
// agent_status tool for visibility.
func (r *subagentRegistry) activeCount(rootID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activePerRoot[rootID]
}

// resolveRootSessionID walks parent links until ParentSessionID is
// empty. Bounded by maxDepth+1 hops so a corrupted chain can't loop
// forever. Returns sess.ID when sess is already root.
func resolveRootSessionID(ctx context.Context, repo *session.Repo, sess session.Session) (string, error) {
	if sess.ParentSessionID == "" {
		return sess.ID, nil
	}
	// P40 maxDepth=2 → at most 3 hops (root → child → grandchild).
	// hopLimit=8 stays comfortably above that and gives headroom for
	// future bumps without needing a code change here. A corrupted
	// parent-link chain trips the limit and surfaces as a clear error.
	const hopLimit = 8
	cur := sess
	for hop := 0; hop < hopLimit; hop++ {
		if cur.ParentSessionID == "" {
			return cur.ID, nil
		}
		next, err := repo.Get(ctx, cur.ParentSessionID)
		if err != nil {
			return "", fmt.Errorf("walk parents: %w", err)
		}
		cur = next
	}
	return "", fmt.Errorf("session chain exceeds hop limit %d (corrupted parent links?)", hopLimit)
}
