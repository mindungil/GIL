package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

const statusCreated = "created"

// ErrNotFound is returned when a session lookup misses.
var ErrNotFound = errors.New("session not found")

// Session is the in-memory representation of a session row.
type Session struct {
	ID           string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	SpecID       string
	WorkingDir   string
	GoalHint     string
	TotalTokens  int64
	TotalCostUSD float64
	// Subagent linkage (schema v3, G5).
	// ParentSessionID = "" → root session (the user-initiated one).
	// SubagentDepth   = 0 for root, parent.depth+1 for spawned children.
	// SubagentLabel   = parent-chosen nickname used by wait_agent. "" for root.
	ParentSessionID string
	SubagentDepth   int32
	SubagentLabel   string
}

// CreateInput captures the fields the caller supplies for a new session.
type CreateInput struct {
	WorkingDir string
	GoalHint   string
	// Subagent fields — set only when spawn_agent creates a child. Empty
	// values keep the session as a root (parent="" depth=0 label="").
	ParentSessionID string
	SubagentDepth   int32
	SubagentLabel   string
}

// ListOptions controls pagination and filtering for List.
type ListOptions struct {
	Limit        int
	StatusFilter string
}

// Repo wraps a *sql.DB and provides session CRUD.
type Repo struct {
	db *sql.DB
}

// NewRepo returns a Repo backed by db.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// Create inserts a new session with a fresh ULID and the supplied fields.
func (r *Repo) Create(ctx context.Context, in CreateInput) (Session, error) {
	id := ulid.Make().String()
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sessions
		(id, status, created_at, updated_at, working_dir, goal_hint,
		 parent_session_id, subagent_depth, subagent_label)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, statusCreated, now, now, in.WorkingDir, in.GoalHint,
		in.ParentSessionID, in.SubagentDepth, in.SubagentLabel)
	if err != nil {
		return Session{}, fmt.Errorf("session.Create: %w", err)
	}
	return Session{
		ID:              id,
		Status:          statusCreated,
		CreatedAt:       now,
		UpdatedAt:       now,
		WorkingDir:      in.WorkingDir,
		GoalHint:        in.GoalHint,
		ParentSessionID: in.ParentSessionID,
		SubagentDepth:   in.SubagentDepth,
		SubagentLabel:   in.SubagentLabel,
	}, nil
}

// Get returns the session by id, or ErrNotFound.
func (r *Repo) Get(ctx context.Context, id string) (Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint,
		       total_tokens, total_cost_usd,
		       parent_session_id, subagent_depth, subagent_label
		FROM sessions WHERE id = ?
	`, id)
	var s Session
	err := row.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint,
		&s.TotalTokens, &s.TotalCostUSD,
		&s.ParentSessionID, &s.SubagentDepth, &s.SubagentLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("session.Get: %w", err)
	}
	return s, nil
}

// List returns sessions ordered by created_at desc, optionally filtered by status.
func (r *Repo) List(ctx context.Context, opts ListOptions) ([]Session, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint,
	             total_tokens, total_cost_usd,
	             parent_session_id, subagent_depth, subagent_label
	      FROM sessions`
	args := []any{}
	if opts.StatusFilter != "" {
		q += ` WHERE status = ?`
		args = append(args, opts.StatusFilter)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("session.List query: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint,
			&s.TotalTokens, &s.TotalCostUSD,
			&s.ParentSessionID, &s.SubagentDepth, &s.SubagentLabel); err != nil {
			return nil, fmt.Errorf("session.List scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Delete removes the session row by id. Returns ErrNotFound when no
// row matches; the caller can treat that as an idempotent no-op or
// surface it depending on context (the SessionService.Delete RPC
// surfaces it so a CLI batch delete can render an accurate count).
//
// This is a row-only delete; the per-session workspace directory under
// SessionsDir/<id> is owned by the gild process and is unlinked
// separately by SessionService.Delete (the Repo has no path knowledge).
func (r *Repo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("session.Delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("session.Delete rowsAffected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateStatus changes the session's status string and bumps updated_at.
// Returns ErrNotFound if the session does not exist.
func (r *Repo) UpdateStatus(ctx context.Context, id, status string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("session.UpdateStatus: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("session.UpdateStatus rowsAffected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListChildren returns sessions whose parent_session_id matches parentID.
// Order is created_at ascending — earliest spawned first, which matches
// how a parent agent reasons about its child fleet ("first the explorer,
// then the awaiter"). Empty parentID lists root-only sessions; pass a
// real id to get only spawned subagents.
//
// Returned slice is empty (not nil-pointing-to-err) on zero matches.
// Used by agent_status, wait_agent (label lookup), and the subagent
// registry's depth/count checks.
func (r *Repo) ListChildren(ctx context.Context, parentID string) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint,
		       total_tokens, total_cost_usd,
		       parent_session_id, subagent_depth, subagent_label
		FROM sessions
		WHERE parent_session_id = ?
		ORDER BY created_at ASC
	`, parentID)
	if err != nil {
		return nil, fmt.Errorf("session.ListChildren query: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint,
			&s.TotalTokens, &s.TotalCostUSD,
			&s.ParentSessionID, &s.SubagentDepth, &s.SubagentLabel); err != nil {
			return nil, fmt.Errorf("session.ListChildren scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DB returns the underlying *sql.DB. Used by service-layer stores
// (planStore, workingSet) that need write-through persistence
// without forcing every Repo caller through the Repo abstraction
// for what are essentially per-session caches.
func (r *Repo) DB() *sql.DB { return r.db }
