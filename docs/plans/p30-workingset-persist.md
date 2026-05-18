# P30 — Persistent WorkingSet Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `add_to_workingset` / `drop_from_workingset` / `list_workingset` survive a daemon restart, by mirroring the planStore persist pattern (write-through to a SQLite table, re-hydrate on first access). Closes the Severity-2 "Persistent working set" item from `docs/plans/roadmap-post-v0.2.0.md`.

**Architecture:** Add a `workingset_entries` table at schema v4. The
in-memory `workingSet` (in `server/internal/service/agent_tools_verbs.go`)
gains a `*sql.DB` field via `SetDB`, an `ensureLoadedLocked` hydrator,
and a single `persistAddLocked` / `persistDropLocked` pair. Wiring
flows through a small new `Repo.DB() *sql.DB` accessor consumed by the
existing lazy `chatWorkingSet()` accessor — when the SessionService
has a Repo, the workingSet automatically gets persistence; tests that
construct a SessionService without a Repo (none today) keep working.

**Tech Stack:** Go (server + core), `modernc.org/sqlite`, `testify/require`.

---

## §0. Spec

### 0.1 Storage shape

One table, schema v4:

```sql
CREATE TABLE IF NOT EXISTS workingset_entries (
    session_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    added_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, path)
);

CREATE INDEX IF NOT EXISTS idx_workingset_entries_session
    ON workingset_entries(session_id);
```

`PRIMARY KEY (session_id, path)` makes add idempotent at the DB layer
(matching the in-memory dedupe on the `bag` map). `added_at` is not
surfaced to callers today but is cheap to keep — useful for future
LRU/age-based eviction policies and matches the `applied_at` column
pattern used by `schema_version`.

### 0.2 In-memory store changes

`workingSet` (service package) gains, in this order:

```go
type workingSet struct {
    mu      sync.Mutex
    entries map[string]map[string]struct{}
    db      *sql.DB                        // P30: durable backing
    loaded  map[string]struct{}            // P30: hydrate-once tracker
}
```

Plus three new methods, modeled exactly on planStore:
- `SetDB(db *sql.DB)` — wires backing, resets `loaded`.
- `ensureLoadedLocked(sid string)` — on first access for `sid` after
  a SetDB call, SELECT all paths and seed `entries[sid]`.
- `persistAddLocked(sid string, paths []string)` and
  `persistDropLocked(sid string, paths []string)` — write-through on
  every mutation. Silent failure (durability is best-effort, same
  rationale as planStore).

`add`, `drop`, `list` all call `ensureLoadedLocked` first. `add` and
`drop` call their persist sibling at the end of the locked critical
section.

### 0.3 Wiring through SessionService

`Repo` gains a one-line `DB() *sql.DB` accessor. `chatWorkingSet()`
(the lazy allocator at `agent_tools_verbs.go:105`) calls
`s.workingSet.SetDB(s.repo.DB())` after the first allocation — but
only when `s.repo != nil` (existing nil-safety convention in this
package; some test paths construct a service without a repo).

### 0.4 What does NOT change

- The `add_to_workingset` / `drop_from_workingset` / `list_workingset`
  tool schemas. Same JSON, same outputs.
- The `agent.go` tool gating that includes these in the chat tool
  set. Untouched.
- The `workingSet` API surface (`add`, `drop`, `list`). Signatures
  identical; just gain durability behind the scenes.

### 0.5 Test strategy

Mirror `agent_tools_plan_verify_persist_test.go` exactly:
- `openTestWorkingSetDB(t)` — same shape as `openTestPlanDB`.
- `newWorkingSetWithDB(db)` — same shape as `newStoreWithDB`.
- One restart-roundtrip test (add paths, simulate restart with fresh
  workingSet pointing at same DB, list returns the same paths).
- One drop-roundtrip test (add+drop persists; restart confirms drop).
- One no-DB sanity test (construction without SetDB still works for
  in-memory mode).

---

## §1. File structure

**Modify:**
- `core/session/schema.go` — bump `currentSchemaVersion` to 4, append migration.
- `core/session/repo.go` — add `Repo.DB() *sql.DB` accessor.
- `server/internal/service/agent_tools_verbs.go:39-112` — add db/loaded fields, SetDB, hydrate, persist; wire from chatWorkingSet.

**Create:**
- `server/internal/service/agent_tools_verbs_persist_test.go` — restart-roundtrip tests.

**No changes to:**
- `server/cmd/gild/main.go` — chatWorkingSet auto-wires from existing repo handle; no startup change needed.
- Tool definitions, agent.go gating, schema for sessions/plan_steps.

---

## §2. Tasks

### Task 1: Schema v4 migration + Repo.DB accessor (TDD)

**Files:**
- Modify: `core/session/schema.go`
- Modify: `core/session/repo.go`
- Create: `core/session/schema_workingset_test.go`

- [ ] **Step 1: Write the failing migration test**

Create `core/session/schema_workingset_test.go`:

```go
package session

import (
    "database/sql"
    "path/filepath"
    "testing"

    _ "modernc.org/sqlite"

    "github.com/stretchr/testify/require"
)

func TestMigrateV4_CreatesWorkingsetEntries(t *testing.T) {
    db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ws.db"))
    require.NoError(t, err)
    t.Cleanup(func() { _ = db.Close() })
    require.NoError(t, Migrate(db))

    // INSERT round-trip proves table exists with expected columns.
    _, err = db.Exec(`INSERT INTO workingset_entries (session_id, path)
        VALUES (?, ?)`, "s1", "main.go")
    require.NoError(t, err)
    _, err = db.Exec(`INSERT INTO workingset_entries (session_id, path)
        VALUES (?, ?)`, "s1", "util.go")
    require.NoError(t, err)

    // PK enforces dedupe — second insert of same (sid, path) is an error.
    _, err = db.Exec(`INSERT INTO workingset_entries (session_id, path)
        VALUES (?, ?)`, "s1", "main.go")
    require.Error(t, err, "primary key (session_id, path) should reject duplicate")

    // Index is queryable.
    rows, err := db.Query(`SELECT path FROM workingset_entries
        WHERE session_id = ? ORDER BY path ASC`, "s1")
    require.NoError(t, err)
    defer rows.Close()
    var got []string
    for rows.Next() {
        var p string
        require.NoError(t, rows.Scan(&p))
        got = append(got, p)
    }
    require.Equal(t, []string{"main.go", "util.go"}, got)
}

func TestRepoDB_ReturnsHandle(t *testing.T) {
    db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "r.db"))
    require.NoError(t, err)
    t.Cleanup(func() { _ = db.Close() })
    require.NoError(t, Migrate(db))

    repo := NewRepo(db)
    require.Same(t, db, repo.DB(), "Repo.DB returns the wrapped *sql.DB")
}
```

- [ ] **Step 2: Run the test — confirm both fail**

Run: `cd core && go test ./session/ -run "TestMigrateV4_CreatesWorkingsetEntries|TestRepoDB_ReturnsHandle" -v`
Expected: FAIL — `TestMigrateV4` fails on the INSERT (no such table); `TestRepoDB` fails to compile (Repo.DB undefined).

- [ ] **Step 3: Bump schema and append migration**

In `core/session/schema.go`:

Change line 11:
```go
const currentSchemaVersion = 4
```

Append to the `migrations` slice (before the closing `}`):

```go
    // v4 — workingset_entries persistence (P30). add_to_workingset /
    // drop_from_workingset previously held in-memory only on the daemon;
    // restart silently emptied the user's curated context. The table
    // backs the per-session set with write-through inserts/deletes and
    // hydrate-on-first-access. PK (session_id, path) gives idempotent
    // adds that match the in-memory dedupe; added_at is unused today
    // but cheap and useful for future LRU policies.
    `
    CREATE TABLE IF NOT EXISTS workingset_entries (
        session_id TEXT NOT NULL,
        path       TEXT NOT NULL,
        added_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY (session_id, path)
    );

    CREATE INDEX IF NOT EXISTS idx_workingset_entries_session
        ON workingset_entries(session_id);
    `,
```

- [ ] **Step 4: Add Repo.DB accessor**

Append to `core/session/repo.go` (after `ListChildren`):

```go
// DB returns the underlying *sql.DB. Used by service-layer stores
// (planStore, workingSet) that need write-through persistence
// without forcing every Repo caller through the Repo abstraction
// for what are essentially per-session caches.
func (r *Repo) DB() *sql.DB { return r.db }
```

- [ ] **Step 5: Run the test — confirm both pass**

Run: `cd core && go test ./session/ -run "TestMigrateV4_CreatesWorkingsetEntries|TestRepoDB_ReturnsHandle" -v`
Expected: PASS — both.

- [ ] **Step 6: Run the full session package**

Run: `cd core && go test ./session/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add core/session/schema.go core/session/repo.go core/session/schema_workingset_test.go
git commit -m "feat(session): P30 schema v4 — workingset_entries + Repo.DB accessor"
```

### Task 2: workingSet persist methods (TDD)

**Files:**
- Modify: `server/internal/service/agent_tools_verbs.go:39-112`
- Create: `server/internal/service/agent_tools_verbs_persist_test.go`

- [ ] **Step 1: Write the failing persist tests**

Create `server/internal/service/agent_tools_verbs_persist_test.go`:

```go
package service

import (
    "database/sql"
    "path/filepath"
    "testing"

    _ "modernc.org/sqlite"

    "github.com/stretchr/testify/require"

    "github.com/mindungil/gil/core/session"
)

// P30 — verifies workingset survives a "daemon restart" by writing
// through a *sql.DB, dropping the in-memory cache, and rehydrating
// from the same DB on the next access. Mirrors
// agent_tools_plan_verify_persist_test.go.

func openTestWorkingSetDB(t *testing.T) *sql.DB {
    t.Helper()
    db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ws.db"))
    require.NoError(t, err)
    require.NoError(t, session.Migrate(db))
    t.Cleanup(func() { _ = db.Close() })
    return db
}

func newWorkingSetWithDB(db *sql.DB) *workingSet {
    s := newWorkingSet()
    s.SetDB(db)
    return s
}

func TestWorkingSet_AddPersistsThroughDB(t *testing.T) {
    db := openTestWorkingSetDB(t)
    s := newWorkingSetWithDB(db)

    added, dup := s.add("sess-a", []string{"main.go", "util.go"})
    require.ElementsMatch(t, []string{"main.go", "util.go"}, added)
    require.Empty(t, dup)

    // Simulate daemon restart: fresh store pointing at the same DB.
    revived := newWorkingSetWithDB(db)
    require.Equal(t, []string{"main.go", "util.go"}, revived.list("sess-a"))
}

func TestWorkingSet_DropPersistsThroughDB(t *testing.T) {
    db := openTestWorkingSetDB(t)
    s := newWorkingSetWithDB(db)

    s.add("sess-b", []string{"a.go", "b.go", "c.go"})
    dropped, missing := s.drop("sess-b", []string{"b.go"})
    require.Equal(t, []string{"b.go"}, dropped)
    require.Empty(t, missing)

    revived := newWorkingSetWithDB(db)
    require.Equal(t, []string{"a.go", "c.go"}, revived.list("sess-b"))
}

func TestWorkingSet_AddIsIdempotentAcrossRestart(t *testing.T) {
    db := openTestWorkingSetDB(t)
    s := newWorkingSetWithDB(db)

    s.add("sess-c", []string{"main.go"})
    revived := newWorkingSetWithDB(db)

    // Re-adding existing path on revived store reports it as duplicate,
    // not added — hydration populated the bag from DB.
    added, dup := revived.add("sess-c", []string{"main.go", "extra.go"})
    require.Equal(t, []string{"extra.go"}, added)
    require.Equal(t, []string{"main.go"}, dup)
}

func TestWorkingSet_NoDBStillWorks(t *testing.T) {
    s := newWorkingSet()
    added, _ := s.add("sess-d", []string{"x.go"})
    require.Equal(t, []string{"x.go"}, added)
    require.Equal(t, []string{"x.go"}, s.list("sess-d"))
}

func TestWorkingSet_PerSessionIsolation(t *testing.T) {
    db := openTestWorkingSetDB(t)
    s := newWorkingSetWithDB(db)

    s.add("sess-e", []string{"e.go"})
    s.add("sess-f", []string{"f.go"})

    revived := newWorkingSetWithDB(db)
    require.Equal(t, []string{"e.go"}, revived.list("sess-e"))
    require.Equal(t, []string{"f.go"}, revived.list("sess-f"))
}
```

- [ ] **Step 2: Run the tests — confirm they fail**

Run: `cd server && go test ./internal/service/ -run TestWorkingSet -v`
Expected: FAIL — `SetDB` undefined; restart roundtrip fails because the second `workingSet` instance has empty `entries`.

- [ ] **Step 3: Add db/loaded fields and persist methods**

In `server/internal/service/agent_tools_verbs.go`, replace the
existing `workingSet` struct (lines 39-46) and `add`/`drop`/`list`
methods (lines 48-100) with:

```go
type workingSet struct {
    mu      sync.Mutex
    entries map[string]map[string]struct{}
    // db is the optional durable backing. When nil the store behaves
    // as the pre-P30 in-memory version — tests that don't care about
    // persistence keep working untouched.
    db *sql.DB
    // loaded tracks which session IDs have been hydrated from DB so
    // add/drop/list skip the SELECT after the first hit. Presence-only.
    loaded map[string]struct{}
}

func newWorkingSet() *workingSet {
    return &workingSet{
        entries: map[string]map[string]struct{}{},
        loaded:  map[string]struct{}{},
    }
}

// SetDB attaches a *sql.DB to the store. Pass nil to detach (tests).
// Safe to call multiple times.
func (w *workingSet) SetDB(db *sql.DB) {
    w.mu.Lock()
    defer w.mu.Unlock()
    w.db = db
    // Reset loaded set so the next access rehydrates against the
    // new backing.
    w.loaded = map[string]struct{}{}
}

func (w *workingSet) add(sid string, paths []string) (added, alreadyPresent []string) {
    w.mu.Lock()
    defer w.mu.Unlock()
    w.ensureLoadedLocked(sid)
    bag, ok := w.entries[sid]
    if !ok {
        bag = map[string]struct{}{}
        w.entries[sid] = bag
    }
    for _, p := range paths {
        p = strings.TrimSpace(p)
        if p == "" {
            continue
        }
        if _, exists := bag[p]; exists {
            alreadyPresent = append(alreadyPresent, p)
            continue
        }
        bag[p] = struct{}{}
        added = append(added, p)
    }
    if len(added) > 0 {
        w.persistAddLocked(sid, added)
    }
    return added, alreadyPresent
}

func (w *workingSet) drop(sid string, paths []string) (dropped, notPresent []string) {
    w.mu.Lock()
    defer w.mu.Unlock()
    w.ensureLoadedLocked(sid)
    bag := w.entries[sid]
    for _, p := range paths {
        p = strings.TrimSpace(p)
        if p == "" {
            continue
        }
        if _, ok := bag[p]; ok {
            delete(bag, p)
            dropped = append(dropped, p)
        } else {
            notPresent = append(notPresent, p)
        }
    }
    if len(dropped) > 0 {
        w.persistDropLocked(sid, dropped)
    }
    return dropped, notPresent
}

func (w *workingSet) list(sid string) []string {
    w.mu.Lock()
    defer w.mu.Unlock()
    w.ensureLoadedLocked(sid)
    bag := w.entries[sid]
    out := make([]string, 0, len(bag))
    for p := range bag {
        out = append(out, p)
    }
    sort.Strings(out)
    return out
}

// ensureLoadedLocked hydrates w.entries[sid] from DB on first hit
// after a SetDB call. Caller holds w.mu. When db is nil this is a
// no-op.
func (w *workingSet) ensureLoadedLocked(sid string) {
    if w.loaded == nil {
        w.loaded = map[string]struct{}{}
    }
    if w.db == nil {
        return
    }
    if _, done := w.loaded[sid]; done {
        return
    }
    rows, err := w.db.Query(`SELECT path FROM workingset_entries
        WHERE session_id = ? ORDER BY path ASC`, sid)
    if err != nil {
        // Silent failure — pre-restart state is unrecoverable for
        // this session, but the in-memory store stays consistent.
        return
    }
    defer rows.Close()
    bag, ok := w.entries[sid]
    if !ok {
        bag = map[string]struct{}{}
        w.entries[sid] = bag
    }
    for rows.Next() {
        var p string
        if err := rows.Scan(&p); err != nil {
            return
        }
        bag[p] = struct{}{}
    }
    w.loaded[sid] = struct{}{}
}

// persistAddLocked inserts the new paths. Caller holds w.mu. Failures
// are silent — durability is best-effort and the in-memory store
// remains authoritative within the daemon's lifetime. Uses INSERT OR
// IGNORE so a stale duplicate row can't fail the whole batch.
func (w *workingSet) persistAddLocked(sid string, paths []string) {
    if w.db == nil {
        return
    }
    tx, err := w.db.Begin()
    if err != nil {
        return
    }
    defer func() { _ = tx.Rollback() }()
    for _, p := range paths {
        if _, err := tx.Exec(`INSERT OR IGNORE INTO workingset_entries
            (session_id, path) VALUES (?, ?)`, sid, p); err != nil {
            return
        }
    }
    _ = tx.Commit()
}

// persistDropLocked deletes the given paths for sid. Caller holds w.mu.
// Silent failure (same rationale as persistAddLocked).
func (w *workingSet) persistDropLocked(sid string, paths []string) {
    if w.db == nil {
        return
    }
    tx, err := w.db.Begin()
    if err != nil {
        return
    }
    defer func() { _ = tx.Rollback() }()
    for _, p := range paths {
        if _, err := tx.Exec(`DELETE FROM workingset_entries
            WHERE session_id = ? AND path = ?`, sid, p); err != nil {
            return
        }
    }
    _ = tx.Commit()
}
```

You'll need to add `"database/sql"` to the imports if not already present in the file.

- [ ] **Step 4: Run the persist tests — confirm pass**

Run: `cd server && go test ./internal/service/ -run TestWorkingSet -v`
Expected: PASS — all 5 cases.

- [ ] **Step 5: Run the full service package**

Run: `cd server && go test ./internal/service/...`
Expected: PASS — no regressions in the existing add/drop/list tests.

- [ ] **Step 6: Commit**

```bash
git add server/internal/service/agent_tools_verbs.go server/internal/service/agent_tools_verbs_persist_test.go
git commit -m "feat(workingset): P30 — persist add/drop through workingset_entries"
```

### Task 3: SessionService wiring (auto-attach DB on first chatWorkingSet)

**Files:**
- Modify: `server/internal/service/agent_tools_verbs.go:105-112`

- [ ] **Step 1: Update chatWorkingSet to auto-wire DB on first allocation**

Replace the existing `chatWorkingSet` (lines 105-112) with:

```go
// chatWorkingSet returns the per-service working-set store, allocating
// on first access so existing constructors (NewSessionService) keep
// compiling without churn. When the SessionService has a Repo, the
// store is auto-wired with the underlying *sql.DB on first allocation
// so add/drop/list survive a daemon restart (P30).
func (s *SessionService) chatWorkingSet() *workingSet {
    s.workingSetMu.Lock()
    defer s.workingSetMu.Unlock()
    if s.workingSet == nil {
        s.workingSet = newWorkingSet()
        if s.repo != nil {
            s.workingSet.SetDB(s.repo.DB())
        }
    }
    return s.workingSet
}
```

- [ ] **Step 2: Run all service tests for regression**

Run: `cd server && go test ./internal/service/...`
Expected: PASS — including any pre-existing tests that go through
`chatWorkingSet` (verbs tests, session tests, etc.).

- [ ] **Step 3: Commit**

```bash
git add server/internal/service/agent_tools_verbs.go
git commit -m "feat(workingset): P30 — auto-wire DB through chatWorkingSet"
```

### Task 4: Final cross-module sanity build

**Files:** none modified.

- [ ] **Step 1: Build all modules in the workspace**

Run (from `/home/ubuntu/gil`):

```bash
for m in cli core server mcp proto runtime sdk tui; do
  if [ -f "$m/go.mod" ]; then
    echo "=== $m ==="
    (cd "$m" && go build ./... && go vet ./...) || exit 1
  fi
done
```

Expected: every module builds clean. (`go vet` may surface the pre-existing `subagent_slice.go` proto copy-lock noise — that's untouched by P30 and not a regression.)

- [ ] **Step 2: Run all tests in the changed modules**

Run:

```bash
(cd core && go test ./...) && (cd server && go test ./...)
```

Expected: PASS.

- [ ] **Step 3: No commit** — sanity-only.

---

## §3. Self-review (per writing-plans skill)

**Spec coverage:**
- §0.1 storage shape → Task 1 Step 3 (migration text matches verbatim).
- §0.2 in-memory store changes → Task 2 Step 3 (struct + 5 methods).
- §0.3 wiring → Task 3 Step 1.
- §0.4 unchanged surface → no task touches schemas, agent.go gating, or tool definitions.
- §0.5 test strategy → Task 2 Step 1 covers all 5 cases listed.

**Placeholders:** None — every step has the actual code or command.

**Type consistency:**
- `workingSet` keeps existing public methods (`add`, `drop`, `list`) with identical signatures.
- New methods all match planStore's naming exactly (`SetDB`, `ensureLoadedLocked`, `persistAddLocked` / `persistDropLocked`).
- `Repo.DB() *sql.DB` matches the existing accessor convention.
