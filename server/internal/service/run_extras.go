package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// diffMaxBytes caps the unified-diff body the server returns to clients
// so a megabyte-scale checkpoint diff doesn't blow out the gRPC frame
// budget or the TUI's renderer. The truncated tail is reported via the
// truncated_bytes field.
const diffMaxBytes = 16 * 1024

// RequestCompact queues a compaction to run at the next iteration
// boundary for the in-flight run on req.SessionId. When no run is in
// flight, returns queued=false with reason="no run in flight" so the
// surface can render a friendly message.
//
// The implementation is non-preemptive: the runner observes the flag
// at the top of the next iteration, never mid-tool-call. This matches
// the slash-command ground rule that surfaces never interrupt a tool
// call.
func (s *RunService) RequestCompact(ctx context.Context, req *gilv1.RequestCompactRequest) (*gilv1.RequestCompactResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	s.mu.Lock()
	loop := s.runLoops[req.SessionId]
	stream := s.runStreams[req.SessionId]
	s.mu.Unlock()

	if loop == nil {
		return &gilv1.RequestCompactResponse{Queued: false, Reason: "no run in flight"}, nil
	}
	loop.RequestCompact()

	// Emit a system_note so observers (TUI tail, gil events) see the
	// surface-issued request landed. compact_start / compact_done events
	// will follow at the next iteration boundary, courtesy of the
	// runner's existing Compactor instrumentation.
	if stream != nil {
		_, _ = stream.Append(event.Event{
			Timestamp: time.Now().UTC(),
			Source:    event.SourceUser,
			Kind:      event.KindNote,
			Type:      "compact_requested",
			Data:      []byte(`{"source":"slash"}`),
		})
	}

	return &gilv1.RequestCompactResponse{Queued: true}, nil
}

// PostHint stages a single-shot hint as an extraSystemNote on the
// in-flight run's AgentLoop. The note is appended to the system prompt
// for the NEXT iteration only, then cleared. The agent decides whether
// to honor — that's the ground-rule contract.
//
// Returns posted=false with reason="no run in flight" when the session
// has no active run; same friendly-message pattern as RequestCompact.
func (s *RunService) PostHint(ctx context.Context, req *gilv1.PostHintRequest) (*gilv1.PostHintResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if len(req.Hint) == 0 {
		return nil, status.Error(codes.InvalidArgument, "hint must contain at least one key/value pair")
	}
	s.mu.Lock()
	loop := s.runLoops[req.SessionId]
	stream := s.runStreams[req.SessionId]
	s.mu.Unlock()

	if loop == nil {
		return &gilv1.PostHintResponse{Posted: false, Reason: "no run in flight"}, nil
	}

	loop.QueueSystemNote(formatHintNote(req.Hint))

	// Surface a parallel system event so the hint shows up in `gil
	// events` / TUI tail. The agent reads the hint via the system
	// prompt; the event is purely for observability.
	if stream != nil {
		data, _ := json.Marshal(map[string]any{"hint": req.Hint, "source": "slash"})
		_, _ = stream.Append(event.Event{
			Timestamp: time.Now().UTC(),
			Source:    event.SourceUser,
			Kind:      event.KindNote,
			Type:      "user_hint",
			Data:      data,
		})
	}
	return &gilv1.PostHintResponse{Posted: true}, nil
}

// formatHintNote renders the opaque hint map as a human-readable block
// the agent can read inside its system prompt. Keys are sorted so the
// rendering is stable across calls (helps any provider-side prefix
// caching that compares note strings byte-for-byte).
func formatHintNote(hint map[string]string) string {
	keys := make([]string, 0, len(hint))
	for k := range hint {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("USER HINT (consider for next turn):\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "  %s: %s\n", k, hint[k])
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Diff returns the unified diff between the latest shadow-git checkpoint
// and the current workspace state. The diff is read-only — the workspace
// is unchanged. When the session has no checkpoints, returns an empty
// body with note="no checkpoints yet for this session" so the surface
// can show a friendly message.
//
// Large diffs are truncated to ~16 KB; the truncated flag and
// truncated_bytes count let the caller render a "... (N bytes truncated)"
// marker.
func (s *RunService) Diff(ctx context.Context, req *gilv1.DiffRequest) (*gilv1.DiffResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := s.repo.Get(ctx, req.SessionId)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return nil, status.Errorf(codes.Internal, "session lookup: %v", err)
	}

	// Resolve the workspace dir the same way executeRun does: prefer the
	// frozen spec's workspace.path when set, fall back to the session's
	// working_dir. This keeps the diff sourced from the directory the
	// runner actually mutated, even when the spec re-rooted the workspace.
	workspaceDir := sess.WorkingDir
	if store := specstore.NewStore(s.sessionDir(req.SessionId)); store != nil {
		if spec, lerr := store.Load(); lerr == nil && spec != nil && spec.Workspace != nil && spec.Workspace.Path != "" {
			workspaceDir = spec.Workspace.Path
		}
	}
	if workspaceDir == "" {
		return nil, status.Error(codes.FailedPrecondition, "session has no workspace path")
	}

	shadowBase := filepath.Join(s.sessionDir(req.SessionId), "shadow")
	sg := checkpoint.New(workspaceDir, shadowBase)
	if _, statErr := os.Stat(filepath.Join(sg.GitDir, "HEAD")); os.IsNotExist(statErr) {
		return &gilv1.DiffResponse{Note: "no checkpoints yet for this session"}, nil
	}

	commits, err := sg.ListCommits(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list checkpoints: %v", err)
	}
	if len(commits) == 0 {
		return &gilv1.DiffResponse{Note: "no checkpoints yet for this session"}, nil
	}
	head := commits[0].SHA

	// Stage untracked files as "intent to add" so they appear in the
	// diff body — otherwise `git diff HEAD` only reports modifications
	// to files that already existed at the checkpoint, missing entirely
	// new files the agent wrote since. The intent-to-add entry adds no
	// content to the index (just marks "this path will be added"), so
	// resetting after the diff leaves the shadow git state untouched
	// for the next checkpoint cycle.
	_ = runGitPlumbing(ctx, sg.GitDir, workspaceDir, "add", "-N", "--", ":/")
	defer func() {
		// Reset the index to HEAD so the intent-to-add markers don't
		// leak into the next Commit() (which uses `git add -A`). Best
		// effort: a failure here is harmless because the next Commit
		// stages the same files anyway.
		_ = runGitPlumbing(context.Background(), sg.GitDir, workspaceDir, "reset", "--mixed", head)
	}()

	// Two git invocations: --stat for the summary counts; the full
	// unified diff for the body. Both use the workspace as the work-tree
	// so untracked/modified files are picked up.
	statOut, statErr := runGitDiff(ctx, sg.GitDir, workspaceDir, head, true)
	if statErr != nil {
		return nil, status.Errorf(codes.Internal, "git diff --stat: %v", statErr)
	}
	body, bodyErr := runGitDiff(ctx, sg.GitDir, workspaceDir, head, false)
	if bodyErr != nil {
		return nil, status.Errorf(codes.Internal, "git diff: %v", bodyErr)
	}

	added, removed, files := parseDiffStat(statOut)

	resp := &gilv1.DiffResponse{
		FilesChanged:  int32(files),
		LinesAdded:    int32(added),
		LinesRemoved:  int32(removed),
		CheckpointSha: head,
	}
	if len(body) > diffMaxBytes {
		resp.Truncated = true
		resp.TruncatedBytes = int32(len(body) - diffMaxBytes)
		resp.UnifiedDiff = body[:diffMaxBytes] + fmt.Sprintf("\n... (%d bytes truncated)\n", resp.TruncatedBytes)
	} else {
		resp.UnifiedDiff = body
	}
	return resp, nil
}

// runGitDiff executes `git diff <head> -- .` against the shadow git for
// the given workspace. When stat=true it appends `--stat` so the caller
// can parse summary counts; otherwise the full unified body is returned.
// Both modes use --no-color so downstream parsers / renderers don't
// have to strip ANSI sequences.
func runGitDiff(ctx context.Context, gitDir, workTree, head string, stat bool) (string, error) {
	args := []string{
		"--git-dir=" + gitDir,
		"--work-tree=" + workTree,
		"diff", "--no-color",
	}
	if stat {
		args = append(args, "--stat")
	}
	args = append(args, head, "--")
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runGitPlumbing is a fire-and-forget wrapper for git index commands
// (add -N, reset) that the diff path uses to bring untracked files
// into the diff scope. The output is discarded — only the exit code
// matters, and even that is non-fatal: a failed `add -N` just means
// untracked files won't appear in the diff body. We keep stderr piped
// to /dev/null so a reset error doesn't leak into the gRPC response.
func runGitPlumbing(ctx context.Context, gitDir, workTree string, args ...string) error {
	full := append([]string{
		"--git-dir=" + gitDir,
		"--work-tree=" + workTree,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// parseDiffStat scans the trailing summary line of `git diff --stat`
// output and extracts (added, removed, files_changed). The stat output
// looks like:
//
//	 file1 | 5 +++--
//	 file2 | 2 +-
//	 2 files changed, 6 insertions(+), 3 deletions(-)
//
// Either insertion or deletion clauses may be absent (pure delete or
// pure add); zero is returned for whichever is missing. When the stat
// output is empty (no changes), all three counts are zero.
func parseDiffStat(out string) (added, removed, files int) {
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, 0, 0
	}
	lines := strings.Split(out, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	// last line: "N files changed, X insertions(+), Y deletions(-)"
	for _, part := range strings.Split(last, ",") {
		part = strings.TrimSpace(part)
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err != nil {
			continue
		}
		switch {
		case strings.Contains(part, "file"):
			files = n
		case strings.Contains(part, "insertion"):
			added = n
		case strings.Contains(part, "deletion"):
			removed = n
		}
	}
	return added, removed, files
}
