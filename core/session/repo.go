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
}

// CreateInput captures the fields the caller supplies for a new session.
type CreateInput struct {
	WorkingDir string
	GoalHint   string
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
		INSERT INTO sessions (id, status, created_at, updated_at, working_dir, goal_hint)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, statusCreated, now, now, in.WorkingDir, in.GoalHint)
	if err != nil {
		return Session{}, fmt.Errorf("session.Create: %w", err)
	}
	return Session{
		ID:         id,
		Status:     statusCreated,
		CreatedAt:  now,
		UpdatedAt:  now,
		WorkingDir: in.WorkingDir,
		GoalHint:   in.GoalHint,
	}, nil
}

// Get returns the session by id, or ErrNotFound.
func (r *Repo) Get(ctx context.Context, id string) (Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint, total_tokens, total_cost_usd
		FROM sessions WHERE id = ?
	`, id)
	var s Session
	err := row.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint, &s.TotalTokens, &s.TotalCostUSD)
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
	q := `SELECT id, status, created_at, updated_at, spec_id, working_dir, goal_hint, total_tokens, total_cost_usd
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
		if err := rows.Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.SpecID, &s.WorkingDir, &s.GoalHint, &s.TotalTokens, &s.TotalCostUSD); err != nil {
			return nil, fmt.Errorf("session.List scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
