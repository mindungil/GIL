// Package session provides SQLite schema management and session data access for gil.
package session

import (
	"database/sql"
	"fmt"
)

// currentSchemaVersion is the latest schema version. When new migrations
// are added, this constant must be incremented to match the new version.
const currentSchemaVersion = 8

// migrations is a slice of SQL migration strings, indexed by version-1.
// For example, migrations[0] is the SQL for version 1, migrations[1] is for
// version 2, and so on. Each migration may contain multiple SQL statements
// separated by semicolons and must be idempotent (safe to run multiple times).
var migrations = []string{
	// v1
	`
	CREATE TABLE IF NOT EXISTS sessions (
		id            TEXT PRIMARY KEY,
		status        TEXT NOT NULL DEFAULT 'created',
		created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		spec_id       TEXT NOT NULL DEFAULT '',
		working_dir   TEXT NOT NULL DEFAULT '',
		goal_hint     TEXT NOT NULL DEFAULT '',
		total_tokens  INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
	CREATE INDEX IF NOT EXISTS idx_sessions_created_at ON sessions(created_at);
	`,
	// v2 — plan_steps persistence (G3). M5.3 introduced plan_steps + verify
	// as a state machine; M5.4 honest section flagged that the store was
	// in-memory only and lost on daemon restart. This table is the durable
	// backing — the in-memory planStore writes through on every mutation
	// and re-hydrates on first access per session after a restart.
	`
	CREATE TABLE IF NOT EXISTS plan_steps (
		session_id        TEXT NOT NULL,
		step_id           INTEGER NOT NULL,
		description       TEXT NOT NULL,
		acceptance_check  TEXT NOT NULL,
		status            TEXT NOT NULL DEFAULT 'pending',
		last_failure      TEXT NOT NULL DEFAULT '',
		updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, step_id)
	);

	CREATE INDEX IF NOT EXISTS idx_plan_steps_session ON plan_steps(session_id);
	`,
	// v3 — subagent parent-child columns (G5). Flat sessions become a
	// tree via parent_session_id; subagent_depth caps recursion;
	// subagent_label is the parent-chosen nickname used in wait_agent.
	// SQLite tolerates ALTER TABLE ADD COLUMN with a default, which is
	// what we need for backfill — existing rows get parent_session_id=""
	// (root), subagent_depth=0, subagent_label="".
	`
	ALTER TABLE sessions ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN subagent_depth INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN subagent_label TEXT NOT NULL DEFAULT '';

	CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
	`,
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
	// v5 — chat_messages persistence (P34). chatHistory previously held
	// the per-session message log in a sync.Map on SessionService; daemon
	// restart silently wiped every in-progress conversation. The table
	// backs the per-session list with write-through inserts and
	// hydrate-on-first-access, identical pattern to workingset_entries.
	// PK (session_id, seq) makes ordering explicit (seq is a 0-based
	// monotonic counter per session, set by chatHistory.append). tool_calls
	// and tool_results are JSON-encoded slices; empty string means no
	// entries. CacheControl is intentionally NOT persisted — it's per-turn
	// state recomputed by core/compact.MarkCacheBreakpoints every Prompt.
	`
	CREATE TABLE IF NOT EXISTS chat_messages (
		session_id   TEXT NOT NULL,
		seq          INTEGER NOT NULL,
		role         TEXT NOT NULL,
		content      TEXT NOT NULL DEFAULT '',
		tool_calls   TEXT NOT NULL DEFAULT '',
		tool_results TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, seq)
	);

	CREATE INDEX IF NOT EXISTS idx_chat_messages_session
		ON chat_messages(session_id);
	`,
	// v6 — session_memories: cross-session memory bank (P55). Lets the
	// chat agent persist short notes ("user prefers tabs", "auth module
	// uses session_token") that get auto-surfaced into the next chat
	// session's system prompt. Per design: id PRIMARY KEY (not
	// session_id+seq) because per-prompt rendering takes recent-N across
	// all sessions, not per-session ordering. content capped at 500 chars
	// by the toolRemember enforcement; the column is TEXT to keep the
	// schema simple.
	`
	CREATE TABLE IF NOT EXISTS session_memories (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		content     TEXT NOT NULL,
		created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_session_memories_created
		ON session_memories(created_at);
	CREATE INDEX IF NOT EXISTS idx_session_memories_session
		ON session_memories(session_id);
	`,
	// v7 — persistent system prompt cache (P68b). Per Hermes pattern
	// (docs/research/2026-05-20-harness-comparison/hermes.md §7), the
	// assembled chat system prompt is byte-stable for the lifetime of
	// a session unless the model/agent profile changes. Caching it on
	// the session row saves the per-turn fmt.Sprintf cost AND — more
	// importantly — sets up Anthropic prefix-cache hits since the
	// cached bytes are identical across turns. cached_prompt_key is a
	// short cache invalidation signature (provider + model + agent);
	// when it changes we drop the cache and rebuild.
	`
	ALTER TABLE sessions ADD COLUMN cached_system_prompt TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN cached_prompt_key    TEXT NOT NULL DEFAULT '';
	`,
	// v8 — frozen spec summary cache (P69). The goal-oriented CLI
	// surfaces only need a small, stable subset of spec.yaml (goal,
	// tasks, success criteria, non-goals). Storing the summary on the
	// session row lets list/goal/resume/watch avoid loading spec.yaml
	// entirely on the hot path; the full spec remains on disk for the
	// runner and exact-detail views.
	`
	ALTER TABLE sessions ADD COLUMN frozen_goal_one_liner TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN frozen_goal_detailed TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN frozen_goal_success_criteria_json TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN frozen_goal_non_goals_json TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN frozen_goal_tasks_json TEXT NOT NULL DEFAULT '';
	`,
}

// Migrate applies all pending schema migrations to the database in a transactional manner.
// It creates the schema_version table if it doesn't exist, checks the current schema version,
// and then applies any migrations needed to reach currentSchemaVersion. Each migration is
// wrapped in a transaction and will be rolled back if any SQL statement fails.
// Migrate is idempotent: it can be safely called multiple times and will only apply
// migrations that haven't been applied yet.
func Migrate(db *sql.DB) error {
	if currentSchemaVersion != len(migrations) {
		return fmt.Errorf("schema version mismatch: currentSchemaVersion=%d but migrations slice has %d entries",
			currentSchemaVersion, len(migrations))
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}

	var current int
	row := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
	if err := row.Scan(&current); err != nil {
		return err
	}

	for v := current + 1; v <= currentSchemaVersion; v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v-1]); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("migration v%d failed: %w; rollback also failed: %v", v, err, rbErr)
			}
			return fmt.Errorf("migration v%d failed: %w", v, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", v); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("schema_version insert v%d failed: %w; rollback also failed: %v", v, err, rbErr)
			}
			return fmt.Errorf("schema_version insert v%d failed: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
