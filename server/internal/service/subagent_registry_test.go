package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
)

// subagent_registry_test.go — S3 invariants: per-root concurrency cap,
// depth cap, root resolution via parent-link walk, release idempotency.

func TestSubagentRegistry_SpawnDecrementsOnRelease(t *testing.T) {
	r := newSubagentRegistry()
	r.maxPerRoot = 3
	r.maxDepth = 2

	repo := newTestRepo(t)
	rootSess, err := repo.Create(context.Background(), session.CreateInput{})
	require.NoError(t, err)

	_, release1, err := r.spawn(context.Background(), repo, rootSess)
	require.NoError(t, err)
	require.Equal(t, 1, r.activeCount(rootSess.ID))

	_, release2, err := r.spawn(context.Background(), repo, rootSess)
	require.NoError(t, err)
	require.Equal(t, 2, r.activeCount(rootSess.ID))

	release1()
	require.Equal(t, 1, r.activeCount(rootSess.ID))

	release2()
	require.Equal(t, 0, r.activeCount(rootSess.ID))
}

func TestSubagentRegistry_RejectsAtConcurrencyLimit(t *testing.T) {
	r := newSubagentRegistry()
	r.maxPerRoot = 2
	r.maxDepth = 2

	repo := newTestRepo(t)
	rootSess, _ := repo.Create(context.Background(), session.CreateInput{})

	_, _, err := r.spawn(context.Background(), repo, rootSess)
	require.NoError(t, err)
	_, _, err = r.spawn(context.Background(), repo, rootSess)
	require.NoError(t, err)
	_, _, err = r.spawn(context.Background(), repo, rootSess)
	require.ErrorIs(t, err, errSubagentLimitReached)
}

func TestSubagentRegistry_RejectsBeyondDepth(t *testing.T) {
	r := newSubagentRegistry()
	r.maxPerRoot = 5
	r.maxDepth = 1

	repo := newTestRepo(t)
	rootSess, _ := repo.Create(context.Background(), session.CreateInput{})
	// Simulate that this session is already a child at depth=1.
	rootSess.SubagentDepth = 1

	_, _, err := r.spawn(context.Background(), repo, rootSess)
	require.ErrorIs(t, err, errSubagentDepthExceeded)
}

// P40 — depth=2 allows root → child → grandchild but blocks
// great-grandchildren. Uses the production default (newSubagentRegistry,
// no overrides) so the test pins the actual shipped behavior.

func TestSubagentRegistry_AllowsGrandchildAtDepth2(t *testing.T) {
	r := newSubagentRegistry() // production defaults: maxDepth=2

	repo := newTestRepo(t)
	// Simulate a session already at depth=1 (a child); spawn should
	// produce a depth=2 grandchild — allowed.
	parent, _ := repo.Create(context.Background(), session.CreateInput{
		SubagentDepth: 1,
	})

	_, release, err := r.spawn(context.Background(), repo, parent)
	require.NoError(t, err, "depth=1 parent must be allowed to spawn a depth=2 grandchild")
	require.NotNil(t, release)
	release()
}

func TestSubagentRegistry_RejectsGreatGrandchildAtDepth3(t *testing.T) {
	r := newSubagentRegistry() // production defaults: maxDepth=2

	repo := newTestRepo(t)
	// Simulate a session already at depth=2 (a grandchild); spawn would
	// produce a depth=3 great-grandchild — blocked.
	parent, _ := repo.Create(context.Background(), session.CreateInput{
		SubagentDepth: 2,
	})

	_, _, err := r.spawn(context.Background(), repo, parent)
	require.ErrorIs(t, err, errSubagentDepthExceeded,
		"depth=2 grandchild must NOT be allowed to spawn further")
}

func TestSubagentRegistry_PerRootCapStillAppliesAcrossTree(t *testing.T) {
	// Even with depth=2, the per-root cap protects against fork bombs.
	// 8 active subagents total under one root (regardless of distribution
	// across depth=1 and depth=2 layers).
	r := newSubagentRegistry()
	r.maxPerRoot = 3 // tighten for the test so we exhaust quickly

	repo := newTestRepo(t)
	root, _ := repo.Create(context.Background(), session.CreateInput{})

	// Spawn 3 direct children at depth=1 — fills the per-root cap.
	for i := 0; i < 3; i++ {
		_, release, err := r.spawn(context.Background(), repo, root)
		require.NoError(t, err, "spawn %d should succeed within per-root cap", i)
		_ = release // keep slot active
	}

	// 4th spawn — direct child OR grandchild — both blocked by cap.
	_, _, err := r.spawn(context.Background(), repo, root)
	require.ErrorIs(t, err, errSubagentLimitReached,
		"4th spawn under same root must hit per-root cap regardless of depth")
}

func TestResolveRootSessionID_WalksToTopOfChain(t *testing.T) {
	repo := newTestRepo(t)
	root, _ := repo.Create(context.Background(), session.CreateInput{})
	child, _ := repo.Create(context.Background(), session.CreateInput{
		ParentSessionID: root.ID,
		SubagentDepth:   1,
	})

	got, err := resolveRootSessionID(context.Background(), repo, child)
	require.NoError(t, err)
	require.Equal(t, root.ID, got)
}

func TestResolveRootSessionID_RootIsItself(t *testing.T) {
	repo := newTestRepo(t)
	root, _ := repo.Create(context.Background(), session.CreateInput{})

	got, err := resolveRootSessionID(context.Background(), repo, root)
	require.NoError(t, err)
	require.Equal(t, root.ID, got)
}

func TestSubagentReleaseRegistry_FireIsIdempotent(t *testing.T) {
	called := 0
	rr := newSubagentReleaseRegistry()
	rr.set("c1", func() { called++ })

	rr.fire("c1")
	rr.fire("c1")
	require.Equal(t, 1, called, "fire is single-shot")
}
