package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

func TestSessionMetaFor_CachesSpecAndLatestActivity(t *testing.T) {
	clearSessionMetaCache()
	t.Cleanup(clearSessionMetaCache)
	clearFrozenSpecCache()
	t.Cleanup(clearFrozenSpecCache)

	oldTTL := sessionMetaCacheTTL
	sessionMetaCacheTTL = time.Hour
	t.Cleanup(func() { sessionMetaCacheTTL = oldTTL })

	oldLoadSpec := loadFrozenSpecForSession
	oldLoadLatest := loadLatestEventSummary
	t.Cleanup(func() {
		loadFrozenSpecForSession = oldLoadSpec
		loadLatestEventSummary = oldLoadLatest
	})

	specCalls := 0
	latestCalls := 0
	loadFrozenSpecForSession = func(sessionDir string) (*gilv1.FrozenSpec, error) {
		specCalls++
		return &gilv1.FrozenSpec{
			Goal: &gilv1.Goal{OneLiner: "cached goal"},
		}, nil
	}
	loadLatestEventSummary = func(path string) (string, time.Time) {
		latestCalls++
		return "tool_call", time.Unix(123, 0)
	}

	sessionsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, "01TESTSESSION"), 0o755))
	sess := &sdk.Session{
		ID:         "01TESTSESSION",
		WorkingDir: "/tmp/work",
	}
	first := sessionMetaFor(sess, sessionsDir)
	second := sessionMetaFor(sess, sessionsDir)

	require.Equal(t, "cached goal", first.frozenGoal)
	require.Equal(t, "tool_call", first.latestType)
	require.Equal(t, "tool_call", second.latestType)
	require.Equal(t, 1, specCalls)
	require.Equal(t, 1, latestCalls)
}

func TestBuildSummaryRows_ParallelizesMetadataLoads(t *testing.T) {
	clearSessionMetaCache()
	t.Cleanup(clearSessionMetaCache)
	clearFrozenSpecCache()
	t.Cleanup(clearFrozenSpecCache)

	oldLoadSpec := loadFrozenSpecForSession
	oldLoadLatest := loadLatestEventSummary
	t.Cleanup(func() {
		loadFrozenSpecForSession = oldLoadSpec
		loadLatestEventSummary = oldLoadLatest
	})

	started := make(chan struct{})
	release := make(chan struct{})
	var active int
	var maxActive int
	var mu sync.Mutex

	loadFrozenSpecForSession = func(sessionDir string) (*gilv1.FrozenSpec, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		if active == 2 {
			close(started)
		}
		mu.Unlock()

		<-release

		mu.Lock()
		active--
		mu.Unlock()
		return &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parallel"}}, nil
	}
	loadLatestEventSummary = func(path string) (string, time.Time) {
		return "tool_call", time.Unix(123, 0)
	}

	sessionsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, "01AAAA"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, "01BBBB"), 0o755))
	sessions := []*sdk.Session{
		{ID: "01AAAA", WorkingDir: "", CreatedAt: time.Unix(1, 0)},
		{ID: "01BBBB", WorkingDir: "", CreatedAt: time.Unix(2, 0)},
	}
	done := make(chan []summaryRow, 1)
	go func() {
		done <- buildSummaryRows(sessions, sessionsDir)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected metadata loads to overlap")
	}
	close(release)
	rows := <-done
	require.Len(t, rows, 2)
	require.GreaterOrEqual(t, maxActive, 2)
}

func TestLoadFrozenSpecForSession_PrefersSummarySidecar(t *testing.T) {
	dir := t.TempDir()
	summary := `{"goal":{"one_liner":"summary goal","detailed":"details","tasks":["a","b"],"success_criteria_natural":["ok"],"non_goals":["x"]}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.summary.json"), []byte(summary), 0o644))

	got, err := loadFrozenSpecForSession(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Goal)
	require.Equal(t, "summary goal", got.Goal.OneLiner)
	require.Equal(t, "details", got.Goal.Detailed)
	require.Equal(t, []string{"a", "b"}, got.Goal.Tasks)
	require.Equal(t, []string{"ok"}, got.Goal.SuccessCriteriaNatural)
	require.Equal(t, []string{"x"}, got.Goal.NonGoals)
}

func TestLoadFrozenSpecForSession_PrefersDBSummary(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "01TESTSESSION")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	dbPath := filepath.Join(root, "sessions.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			frozen_goal_one_liner TEXT,
			frozen_goal_detailed TEXT,
			frozen_goal_success_criteria_json TEXT,
			frozen_goal_non_goals_json TEXT,
			frozen_goal_tasks_json TEXT
		)
	`)
	require.NoError(t, err)
	successJSON, err := json.Marshal([]string{"db ok"})
	require.NoError(t, err)
	nonGoalsJSON, err := json.Marshal([]string{"db no"})
	require.NoError(t, err)
	tasksJSON, err := json.Marshal([]string{"db task"})
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO sessions (
			id, frozen_goal_one_liner, frozen_goal_detailed,
			frozen_goal_success_criteria_json, frozen_goal_non_goals_json,
			frozen_goal_tasks_json
		) VALUES (?, ?, ?, ?, ?, ?)
	`, "01TESTSESSION", "db goal", "db details", string(successJSON), string(nonGoalsJSON), string(tasksJSON))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spec.summary.json"), []byte(`{"goal":{"one_liner":"file goal","detailed":"file details"}}`), 0o644))

	got, err := loadFrozenSpecForSession(sessionDir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Goal)
	require.Equal(t, "db goal", got.Goal.OneLiner)
	require.Equal(t, "db details", got.Goal.Detailed)
	require.Equal(t, []string{"db ok"}, got.Goal.SuccessCriteriaNatural)
	require.Equal(t, []string{"db no"}, got.Goal.NonGoals)
	require.Equal(t, []string{"db task"}, got.Goal.Tasks)
}

func TestLoadFrozenSpecForSession_CachesBackendResult(t *testing.T) {
	clearSessionMetaCache()
	t.Cleanup(clearSessionMetaCache)
	clearFrozenSpecCache()
	t.Cleanup(clearFrozenSpecCache)

	oldDBLoader := loadFrozenSpecSummaryFromDB
	oldFileLoader := loadFrozenSpecSummary
	t.Cleanup(func() {
		loadFrozenSpecSummaryFromDB = oldDBLoader
		loadFrozenSpecSummary = oldFileLoader
	})

	dbCalls := 0
	loadFrozenSpecSummaryFromDB = func(sessionDir string) (*frozenSpecSummary, error) {
		dbCalls++
		return &frozenSpecSummary{Goal: frozenSpecGoalSummary{OneLiner: "cached db goal"}}, nil
	}
	loadFrozenSpecSummary = func(sessionDir string) (*frozenSpecSummary, error) {
		t.Fatal("file fallback should not run when DB returns summary")
		return nil, nil
	}

	dir := t.TempDir()
	first, err := loadFrozenSpecForSession(dir)
	require.NoError(t, err)
	second, err := loadFrozenSpecForSession(dir)
	require.NoError(t, err)

	require.Equal(t, "cached db goal", first.Goal.OneLiner)
	require.Equal(t, "cached db goal", second.Goal.OneLiner)
	require.Equal(t, 1, dbCalls)
}

func TestLoadFrozenSpecForSession_FallsBackToSummaryFileWhenDBMissing(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "01TESTSESSION")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spec.summary.json"), []byte(`{"goal":{"one_liner":"file goal","detailed":"file details","tasks":["file task"]}}`), 0o644))

	got, err := loadFrozenSpecForSession(sessionDir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Goal)
	require.Equal(t, "file goal", got.Goal.OneLiner)
	require.Equal(t, "file details", got.Goal.Detailed)
	require.Equal(t, []string{"file task"}, got.Goal.Tasks)
}

func TestLoadFrozenSpecForSession_MissingDBDoesNotCreateFile(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "01TESTSESSION")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	_, err := loadFrozenSpecForSession(sessionDir)
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(root, "sessions.db"))
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))
}

func BenchmarkLoadFrozenSpecForSessionDB(b *testing.B) {
	root := b.TempDir()
	sessionDir := filepath.Join(root, "sessions", "01TESTSESSION")
	require.NoError(b, os.MkdirAll(sessionDir, 0o755))

	dbPath := filepath.Join(root, "sessions.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(b, err)
	defer func() { require.NoError(b, db.Close()) }()

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			frozen_goal_one_liner TEXT,
			frozen_goal_detailed TEXT,
			frozen_goal_success_criteria_json TEXT,
			frozen_goal_non_goals_json TEXT,
			frozen_goal_tasks_json TEXT
		)
	`)
	require.NoError(b, err)
	successJSON, err := json.Marshal([]string{"db ok"})
	require.NoError(b, err)
	nonGoalsJSON, err := json.Marshal([]string{"db no"})
	require.NoError(b, err)
	tasksJSON, err := json.Marshal([]string{"db task"})
	require.NoError(b, err)
	_, err = db.Exec(`
		INSERT INTO sessions (
			id, frozen_goal_one_liner, frozen_goal_detailed,
			frozen_goal_success_criteria_json, frozen_goal_non_goals_json,
			frozen_goal_tasks_json
		) VALUES (?, ?, ?, ?, ?, ?)
	`, "01TESTSESSION", "db goal", "db details", string(successJSON), string(nonGoalsJSON), string(tasksJSON))
	require.NoError(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := loadFrozenSpecForSession(sessionDir)
		require.NoError(b, err)
	}
}

func BenchmarkSessionMetaForCacheHit(b *testing.B) {
	clearSessionMetaCache()
	defer clearSessionMetaCache()
	clearFrozenSpecCache()
	defer clearFrozenSpecCache()

	oldTTL := sessionMetaCacheTTL
	sessionMetaCacheTTL = time.Hour
	defer func() { sessionMetaCacheTTL = oldTTL }()

	oldLoadSpec := loadFrozenSpecForSession
	oldLoadLatest := loadLatestEventSummary
	defer func() {
		loadFrozenSpecForSession = oldLoadSpec
		loadLatestEventSummary = oldLoadLatest
	}()

	loadFrozenSpecForSession = func(sessionDir string) (*gilv1.FrozenSpec, error) {
		return &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "cached"}}, nil
	}
	loadLatestEventSummary = func(path string) (string, time.Time) {
		return "tool_call", time.Unix(123, 0)
	}

	sess := &sdk.Session{ID: "01BENCH", WorkingDir: "/tmp/work"}
	_ = sessionMetaFor(sess, "/tmp/sessions")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sessionMetaFor(sess, "/tmp/sessions")
	}
}

func BenchmarkBuildSummaryRows(b *testing.B) {
	clearSessionMetaCache()
	defer clearSessionMetaCache()
	clearFrozenSpecCache()
	defer clearFrozenSpecCache()

	oldLoadSpec := loadFrozenSpecForSession
	oldLoadLatest := loadLatestEventSummary
	defer func() {
		loadFrozenSpecForSession = oldLoadSpec
		loadLatestEventSummary = oldLoadLatest
	}()

	loadFrozenSpecForSession = func(sessionDir string) (*gilv1.FrozenSpec, error) {
		return &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "bench"}}, nil
	}
	loadLatestEventSummary = func(path string) (string, time.Time) {
		return "tool_call", time.Unix(123, 0)
	}

	sessions := make([]*sdk.Session, 100)
	for i := range sessions {
		sessions[i] = &sdk.Session{
			ID:         fmt.Sprintf("01BENCH%03d", i),
			WorkingDir: "/tmp/work",
			CreatedAt:  time.Unix(int64(i), 0),
			Status:     "RUNNING",
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clearSessionMetaCache()
		_ = buildSummaryRows(sessions, "/tmp/sessions")
	}
}
