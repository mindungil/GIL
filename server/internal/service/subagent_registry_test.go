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
