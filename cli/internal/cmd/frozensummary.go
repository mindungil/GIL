package cmd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	_ "modernc.org/sqlite"
)

type frozenSpecSummary struct {
	Goal frozenSpecGoalSummary `json:"goal"`
}

type frozenSpecGoalSummary struct {
	OneLiner               string   `json:"one_liner"`
	Detailed               string   `json:"detailed,omitempty"`
	SuccessCriteriaNatural []string `json:"success_criteria_natural,omitempty"`
	NonGoals               []string `json:"non_goals,omitempty"`
	Tasks                  []string `json:"tasks,omitempty"`
}

var loadFrozenSpecForSession = loadFrozenSpecSessionSpec

var frozenSpecDBCache sync.Map
var frozenSpecSummaryCache sync.Map

func loadFrozenSpecSessionSpec(sessionDir string) (*gilv1.FrozenSpec, error) {
	if summary, ok := frozenSpecSummaryFromCache(sessionDir); ok {
		return summary.toFrozenSpec(), nil
	}
	if summary, err := loadFrozenSpecSummaryFromDB(sessionDir); err == nil && summary != nil {
		cacheFrozenSpecSummary(sessionDir, summary)
		return summary.toFrozenSpec(), nil
	}
	if summary, err := loadFrozenSpecSummary(sessionDir); err == nil && summary != nil {
		cacheFrozenSpecSummary(sessionDir, summary)
		return summary.toFrozenSpec(), nil
	}
	spec, err := specstore.NewStore(sessionDir).Load()
	if err != nil {
		return nil, err
	}
	if spec != nil {
		summary := summaryFromFrozenSpec(spec)
		cacheFrozenSpecSummary(sessionDir, &summary)
		_ = persistFrozenSpecSummary(sessionDir, summary)
	}
	return spec, nil
}

func frozenSpecSummaryFromCache(sessionDir string) (*frozenSpecSummary, bool) {
	if cached, ok := frozenSpecSummaryCache.Load(sessionDir); ok {
		entry := cached.(sessionCacheEntry)
		if time.Now().Before(entry.expires) {
			return &entry.summary, true
		}
	}
	return nil, false
}

type sessionCacheEntry struct {
	summary frozenSpecSummary
	expires time.Time
}

func cacheFrozenSpecSummary(sessionDir string, summary *frozenSpecSummary) {
	if summary == nil {
		return
	}
	frozenSpecSummaryCache.Store(sessionDir, sessionCacheEntry{
		summary: *summary,
		expires: time.Now().Add(sessionMetaCacheTTL),
	})
}

func clearFrozenSpecCache() {
	frozenSpecDBCache = sync.Map{}
	frozenSpecSummaryCache = sync.Map{}
}

var loadFrozenSpecSummaryFromDB = loadFrozenSpecSummaryFromDBImpl

func loadFrozenSpecSummaryFromDBImpl(sessionDir string) (*frozenSpecSummary, error) {
	sessionID := filepath.Base(strings.TrimRight(sessionDir, string(filepath.Separator)))
	if sessionID == "." || sessionID == string(filepath.Separator) || sessionID == "" {
		return nil, errors.New("invalid session dir")
	}
	dbPath := filepath.Join(filepath.Dir(filepath.Dir(sessionDir)), "sessions.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	db := frozenSpecDB(dbPath)
	if db == nil {
		return nil, os.ErrNotExist
	}
	row := db.QueryRow(`
		SELECT frozen_goal_one_liner, frozen_goal_detailed,
		       frozen_goal_success_criteria_json, frozen_goal_non_goals_json,
		       frozen_goal_tasks_json
		FROM sessions WHERE id = ?
	`, sessionID)
	var oneLiner, detailed, successJSON, nonGoalsJSON, tasksJSON string
	if err := row.Scan(&oneLiner, &detailed, &successJSON, &nonGoalsJSON, &tasksJSON); err != nil {
		return nil, err
	}
	if strings.TrimSpace(oneLiner) == "" {
		return nil, sql.ErrNoRows
	}
	summary := &frozenSpecSummary{
		Goal: frozenSpecGoalSummary{
			OneLiner: oneLiner,
			Detailed: detailed,
		},
	}
	if err := json.Unmarshal([]byte(successJSON), &summary.Goal.SuccessCriteriaNatural); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(nonGoalsJSON), &summary.Goal.NonGoals); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tasksJSON), &summary.Goal.Tasks); err != nil {
		return nil, err
	}
	return summary, nil
}

func frozenSpecDB(dbPath string) *sql.DB {
	if cached, ok := frozenSpecDBCache.Load(dbPath); ok {
		return cached.(*sql.DB)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil
	}
	actual, loaded := frozenSpecDBCache.LoadOrStore(dbPath, db)
	if loaded {
		_ = db.Close()
		return actual.(*sql.DB)
	}
	return db
}

var loadFrozenSpecSummary = loadFrozenSpecSummaryImpl

func loadFrozenSpecSummaryImpl(sessionDir string) (*frozenSpecSummary, error) {
	body, err := os.ReadFile(filepath.Join(sessionDir, "spec.summary.json"))
	if err != nil {
		return nil, err
	}
	var summary frozenSpecSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, err
	}
	if summary.Goal.OneLiner == "" {
		return nil, os.ErrNotExist
	}
	return &summary, nil
}

func persistFrozenSpecSummary(sessionDir string, summary frozenSpecSummary) error {
	body, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	dir := sessionDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "spec.summary.json.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, "spec.summary.json"))
}

func summaryFromFrozenSpec(spec *gilv1.FrozenSpec) frozenSpecSummary {
	if spec == nil || spec.Goal == nil {
		return frozenSpecSummary{}
	}
	return frozenSpecSummary{
		Goal: frozenSpecGoalSummary{
			OneLiner:               spec.Goal.OneLiner,
			Detailed:               spec.Goal.Detailed,
			SuccessCriteriaNatural: append([]string(nil), spec.Goal.SuccessCriteriaNatural...),
			NonGoals:               append([]string(nil), spec.Goal.NonGoals...),
			Tasks:                  append([]string(nil), spec.Goal.Tasks...),
		},
	}
}

func (s frozenSpecSummary) toFrozenSpec() *gilv1.FrozenSpec {
	return &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{
			OneLiner:               s.Goal.OneLiner,
			Detailed:               s.Goal.Detailed,
			SuccessCriteriaNatural: append([]string(nil), s.Goal.SuccessCriteriaNatural...),
			NonGoals:               append([]string(nil), s.Goal.NonGoals...),
			Tasks:                  append([]string(nil), s.Goal.Tasks...),
		},
	}
}
