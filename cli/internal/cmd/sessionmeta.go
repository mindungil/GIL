package cmd

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mindungil/gil/sdk"
)

var sessionMetaCacheTTL = 2 * time.Second

type sessionRowMeta struct {
	displayName   string
	frozenGoal    string
	gitSummary    string
	latestType    string
	latestAt      time.Time
	eventPath     string
	planCompleted int
	planTotal     int
	planNext      string
}

type sessionSummaryMeta struct {
	displayName   string
	frozenGoal    string
	gitSummary    string
	planCompleted int
	planTotal     int
	planNext      string
}

type sessionRowMetaCacheEntry struct {
	meta    sessionRowMeta
	expires time.Time
}

type sessionSummaryMetaCacheEntry struct {
	meta    sessionSummaryMeta
	expires time.Time
}

var sessionMetaCache atomic.Pointer[sync.Map]
var sessionSummaryMetaCache atomic.Pointer[sync.Map]

var loadLatestEventSummary = lastEventSummary

func sessionMetaFor(s *sdk.Session, sessionsDir string) sessionRowMeta {
	return sessionMetaForAt(s, sessionsDir, time.Now())
}

func sessionMetaForAt(s *sdk.Session, sessionsDir string, now time.Time) sessionRowMeta {
	if s == nil {
		return sessionRowMeta{}
	}
	if strings.TrimSpace(sessionsDir) == "" {
		return sessionRowMeta{
			gitSummary: gitWorkspaceSummaryAt(context.Background(), s.WorkingDir, now),
		}
	}
	key := sessionsDir + "\x00" + s.ID + "\x00" + s.WorkingDir

	cache := sessionMetaCache.Load()
	if cache == nil {
		cache = &sync.Map{}
		if sessionMetaCache.CompareAndSwap(nil, cache) {
			// cache installed below
		} else {
			cache = sessionMetaCache.Load()
		}
	}
	if cached, ok := cache.Load(key); ok {
		entry := cached.(sessionRowMetaCacheEntry)
		if now.Before(entry.expires) {
			return entry.meta
		}
	}

	sessionDir := joinPath(sessionsDir, s.ID)
	meta := sessionRowMeta{}
	meta.displayName = displayName(s)
	if spec, err := loadFrozenSpecForSession(sessionDir); err == nil && spec != nil && spec.Goal != nil {
		meta.frozenGoal = spec.Goal.OneLiner
	}
	eventPath := sessionEventLogPathAt(sessionsDir, s.ID, now)
	meta.eventPath = eventPath
	if latestType, latestAt := loadLatestEventSummary(eventPath); latestType != "" {
		meta.latestType = latestType
		meta.latestAt = latestAt
	}
	if completed, total, next, ok := loadSessionPlanMetaAt(joinPath(sessionDir, "plan.json"), now); ok {
		meta.planCompleted = completed
		meta.planTotal = total
		meta.planNext = next
	}
	meta.gitSummary = gitWorkspaceSummaryAt(context.Background(), s.WorkingDir, now)

	cache.Store(key, sessionRowMetaCacheEntry{
		meta:    meta,
		expires: now.Add(sessionMetaCacheTTL),
	})
	return meta
}

func sessionSummaryMetaForAt(s *sdk.Session, sessionsDir string, now time.Time) sessionSummaryMeta {
	if s == nil {
		return sessionSummaryMeta{}
	}
	if strings.TrimSpace(sessionsDir) == "" {
		return sessionSummaryMeta{
			gitSummary: gitWorkspaceSummaryAt(context.Background(), s.WorkingDir, now),
		}
	}
	key := sessionsDir + "\x00" + s.ID + "\x00" + s.WorkingDir

	cache := sessionSummaryMetaCache.Load()
	if cache == nil {
		cache = &sync.Map{}
		if sessionSummaryMetaCache.CompareAndSwap(nil, cache) {
			// cache installed below
		} else {
			cache = sessionSummaryMetaCache.Load()
		}
	}
	if cached, ok := cache.Load(key); ok {
		entry := cached.(sessionSummaryMetaCacheEntry)
		if now.Before(entry.expires) {
			return entry.meta
		}
	}

	sessionDir := joinPath(sessionsDir, s.ID)
	meta := sessionSummaryMeta{
		displayName: displayName(s),
	}
	if spec, err := loadFrozenSpecForSession(sessionDir); err == nil && spec != nil && spec.Goal != nil {
		meta.frozenGoal = spec.Goal.OneLiner
	}
	if completed, total, next, ok := loadSessionPlanMetaAt(joinPath(sessionDir, "plan.json"), now); ok {
		meta.planCompleted = completed
		meta.planTotal = total
		meta.planNext = next
	}
	meta.gitSummary = gitWorkspaceSummaryAt(context.Background(), s.WorkingDir, now)

	cache.Store(key, sessionSummaryMetaCacheEntry{
		meta:    meta,
		expires: now.Add(sessionMetaCacheTTL),
	})
	return meta
}

func clearSessionMetaCache() {
	sessionMetaCache.Store(&sync.Map{})
	sessionSummaryMetaCache.Store(&sync.Map{})
}
