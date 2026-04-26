// Package checkpoint provides per-step workspace snapshots backed by a
// shadow Git repository. The .git directory lives OUTSIDE the workspace
// (default ~/.gil/shadow/<hash>/.git) so user repos are untouched.
package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ShadowGit manages a shadow git repository for per-iteration checkpoints.
type ShadowGit struct {
	// GitDir is the .git directory used by gil for snapshots (separate
	// from any user repo at the workspace).
	GitDir string
	// WorkspaceDir is the directory whose contents are snapshotted.
	WorkspaceDir string
	// GitBin is the git executable; defaults to "git".
	GitBin string
}

// New returns a ShadowGit whose GitDir is computed under baseDir as
// "<baseDir>/<sha256(workspaceDir)[:16]>/.git". Caller is responsible
// for calling Init() before first Commit().
func New(workspaceDir, baseDir string) *ShadowGit {
	h := sha256.Sum256([]byte(workspaceDir))
	hashHex := hex.EncodeToString(h[:8]) // 16 hex chars
	return &ShadowGit{
		WorkspaceDir: workspaceDir,
		GitDir:       filepath.Join(baseDir, hashHex, ".git"),
	}
}

// Init creates the bare-ish git directory and configures it to use the
// workspace as worktree. Idempotent — Init on an already-initialized
// shadow is a no-op (returns nil).
func (s *ShadowGit) Init(ctx context.Context) error {
	// Idempotency check: if HEAD file exists, already initialized.
	if _, err := os.Stat(filepath.Join(s.GitDir, "HEAD")); err == nil {
		return nil
	}

	// Create the parent directory of GitDir (i.e. the hash bucket directory).
	if err := os.MkdirAll(filepath.Dir(s.GitDir), 0o700); err != nil {
		return fmt.Errorf("checkpoint: mkdir %s: %w", filepath.Dir(s.GitDir), err)
	}

	// Create the GitDir itself so git init can use it as --git-dir.
	if err := os.MkdirAll(s.GitDir, 0o700); err != nil {
		return fmt.Errorf("checkpoint: mkdir %s: %w", s.GitDir, err)
	}

	// git --git-dir=<GitDir> --work-tree=<WorkspaceDir> init
	if _, err := s.gitCmd(ctx, "init"); err != nil {
		return fmt.Errorf("checkpoint: init: %w", err)
	}

	// Configure identity so commits work without a global git config.
	configs := [][2]string{
		{"user.name", "gil-shadow"},
		{"user.email", "shadow@gil.local"},
		{"commit.gpgsign", "false"},
	}
	for _, kv := range configs {
		if _, err := s.gitCmd(ctx, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("checkpoint: config %s: %w", kv[0], err)
		}
	}

	return nil
}

// Commit stages all changes in WorkspaceDir (including deletions) and
// commits with the given message. Returns the resulting commit SHA.
// Always uses --allow-empty so consecutive commits with no changes still
// produce a checkpoint.
func (s *ShadowGit) Commit(ctx context.Context, msg string) (sha string, err error) {
	if _, err := s.gitCmd(ctx, "add", "-A"); err != nil {
		return "", fmt.Errorf("checkpoint: add: %w", err)
	}
	if _, err := s.gitCmd(ctx, "commit", "--allow-empty", "--no-verify", "-m", msg); err != nil {
		return "", fmt.Errorf("checkpoint: commit: %w", err)
	}
	out, err := s.gitCmd(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("checkpoint: rev-parse: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Restore checks out the given commit SHA into the workspace, reverting
// all tracked files to that snapshot. Untracked files in the workspace
// are left as-is (matches `git checkout <sha> -- .` behavior).
func (s *ShadowGit) Restore(ctx context.Context, commitSHA string) error {
	_, err := s.gitCmd(ctx, "checkout", commitSHA, "--", ".")
	return err
}

// Reset HARD-resets HEAD and the working tree to commitSHA — equivalent to
// `git reset --hard <sha>`. Use Reset for stuck-recovery rollbacks where the
// agent needs a clean slate at the target commit; use Restore for less
// destructive rollbacks (e.g., manual `gil restore`) where untracked
// workspace files should be preserved.
//
// Lifted from Cline's resetHead (cline/src/integrations/checkpoints/
// CheckpointTracker.ts:364).
func (s *ShadowGit) Reset(ctx context.Context, commitSHA string) error {
	_, err := s.gitCmd(ctx, "reset", "--hard", commitSHA)
	return err
}

// CommitInfo describes one snapshot.
type CommitInfo struct {
	SHA       string
	Message   string
	Timestamp time.Time
}

// ListCommits returns the commit log on HEAD, newest first. If no commits
// exist (fresh init), returns empty slice without error.
func (s *ShadowGit) ListCommits(ctx context.Context) ([]CommitInfo, error) {
	out, err := s.gitCmd(ctx, "log", "--pretty=format:%H%x09%ct%x09%s", "--no-merges")
	if err != nil {
		// Detect "does not have any commits yet" or "ambiguous argument 'HEAD'"
		if strings.Contains(err.Error(), "does not have any commits yet") ||
			strings.Contains(err.Error(), "ambiguous argument 'HEAD'") {
			return nil, nil
		}
		return nil, err
	}

	var commits []CommitInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		ts, _ := strconv.ParseInt(parts[1], 10, 64)
		commits = append(commits, CommitInfo{
			SHA:       parts[0],
			Timestamp: time.Unix(ts, 0).UTC(),
			Message:   parts[2],
		})
	}
	return commits, nil
}

// gitCmd runs git with --git-dir and --work-tree flags prepended to args.
// Returns trimmed stdout on success; on failure returns empty string and an
// error that embeds stderr for diagnosis.
func (s *ShadowGit) gitCmd(ctx context.Context, args ...string) (string, error) {
	bin := s.GitBin
	if bin == "" {
		bin = "git"
	}
	full := append([]string{
		"--git-dir=" + s.GitDir,
		"--work-tree=" + s.WorkspaceDir,
	}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
