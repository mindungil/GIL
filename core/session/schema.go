package session

import (
	"database/sql"
)

// currentSchemaVersion is the latest schema version. When new migrations
// are added, this constant must be incremented to match the new version.
const currentSchemaVersion = 1

// migrations is a slice of SQL migration strings, indexed by version-1.
// For example, migrations[0] is the SQL for version 1, migrations[1] is for
// version 2, and so on. Each migration may contain multiple SQL statements
// separated by semicolons and must be idempotent (safe to run multiple times).
var migrations = []string{
	// v1
	`
	CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

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
}

// Migrate applies all pending schema migrations to the database in a transactional manner.
// It creates the schema_version table if it doesn't exist, checks the current schema version,
// and then applies any migrations needed to reach currentSchemaVersion. Each migration is
// wrapped in a transaction and will be rolled back if any SQL statement fails.
// Migrate is idempotent: it can be safely called multiple times and will only apply
// migrations that haven't been applied yet.
func Migrate(db *sql.DB) error {
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
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", v); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
