package slash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/instructions"
	"github.com/mindungil/gil/core/paths"
)

// SessionInfo is a tiny sub-set of sdk.Session that the handlers need.
// We intentionally keep this decoupled from the SDK to avoid a core →
// sdk dependency (sdk already depends on proto, and we don't want to
// drag the gRPC client into core just for a struct).
type SessionInfo struct {
	ID               string
	Status           string
	WorkingDir       string
	GoalHint         string
	CurrentIteration int32
	CurrentTokens    int64
	TotalTokens      int64
	TotalCostUSD     float64
}

// SessionFetcher returns a SessionInfo for the given id. The TUI and CLI
// both pass an adapter around their sdk.Client so handlers can refresh
// the session view independently from whatever the surface already has
// cached.
type SessionFetcher func(ctx context.Context, sessionID string) (*SessionInfo, error)

// DiffResult mirrors sdk.DiffResult without the core→sdk dependency.
// The handler renders Note when non-empty (e.g. "no checkpoints yet")
// or the unified diff plus stats otherwise.
type DiffResult struct {
	UnifiedDiff    string
	FilesChanged   int32
	LinesAdded     int32
	LinesRemoved   int32
	Truncated      bool
	TruncatedBytes int32
	CheckpointSHA  string
	Note           string
}

// RunControl is the slim subset of sdk.Client the slash handlers need
// for the RPC-backed commands (/compact, /model, /diff). Surfaces wrap
// their gRPC client behind this so the slash package stays free of a
// gRPC import — same pattern as SessionFetcher.
//
// Implementations should apply their own short timeouts via the passed
// ctx; handlers do not deadline themselves so a surface that wants a
// pager-style /diff can still stream a long response.
type RunControl interface {
	RequestCompact(ctx context.Context, sessionID string) (queued bool, reason string, err error)
	PostHint(ctx context.Context, sessionID string, hint map[string]string) (posted bool, reason string, err error)
	Diff(ctx context.Context, sessionID string) (*DiffResult, error)
}

// LocalState is the surface-side mutable state slice that handlers may
// touch directly: today only the local event ring buffer (cleared by
// /clear). Its sole purpose is to keep "/clear is local-only" enforced
// at compile time — the field is the ONLY way a handler can mutate
// surface-side data.
type LocalState struct {
	// ClearEvents wipes the surface-side event display buffer. It MUST NOT
	// touch server-side history. The TUI passes a closure that resets the
	// Bubbletea events slice; the CLI passes a no-op (no buffer to clear).
	ClearEvents func()
}

// HandlerEnv carries the shared dependencies the registered handlers need.
//
// Surfaces (TUI / CLI) build this env once at startup and call
// RegisterDefaults(reg, env) to populate the registry with the canonical
// nine commands.
type HandlerEnv struct {
	// SessionID is the session the surface is currently observing. May be
	// empty when no session is attached — commands marked NoSession (help,
	// quit) still work.
	SessionID string

	// Layout exposes the XDG paths so handlers can locate the shadow git
	// (per-session) and the session directory directly without round-
	// tripping through the daemon. This keeps /diff fast even when gild
	// is mid-tool-call and would otherwise serialize the request.
	Layout paths.Layout

	// Fetcher fetches Session info on demand (used by /status, /cost).
	// May be nil in tests; commands that need it return a friendly error.
	Fetcher SessionFetcher

	// Run carries the RPC-backed handlers for /compact, /model, /diff.
	// Optional: tests and the headless CLI may leave it nil, in which
	// case the affected handlers return a "no client configured" message
	// rather than crashing. Production surfaces (TUI, gil
	// run --interactive) wire their sdk.Client adapter here.
	Run RunControl

	// Local groups surface-local mutators (currently just /clear). nil-safe.
	Local LocalState

	// Stdout is where /agents non-interactive output is written. Defaults
	// to os.Stdout when zero.
	Stdout io.Writer
}

// RegisterDefaults installs the canonical nine slash commands on reg.
// Both TUI and CLI call this so behaviour matches between surfaces.
//
// The handlers are intentionally read-only with respect to the server:
//
//   - /help, /clear, /quit, /agents — purely local
//   - /status, /cost, /diff — observation only (Get + filesystem)
//   - /compact, /model — return "not yet wired" until Phase 12 Track F /
//     a future RPC adds RunService.RequestCompact + a hint event channel
//
// That deliberate restraint matches the project rule "agent decides,
// system safety net" — we never let a slash command redirect the agent
// mid-tool-call. /compact is conceptually safe (the agent already
// initiates it autonomously) but we still gate the surface-driven
// version behind an explicit RPC, deferred to Track F.
func RegisterDefaults(reg *Registry, env *HandlerEnv) {
	if reg == nil || env == nil {
		return
	}
	reg.Register(Spec{
		Name:      "help",
		Summary:   "list available slash commands",
		NoSession: true,
		Handler:   handleHelp(reg),
	})
	reg.Register(Spec{
		Name:    "status",
		Summary: "show current session id, status, iter, tokens",
		Handler: handleStatus(env),
	})
	reg.Register(Spec{
		Name:    "cost",
		Summary: "show estimated USD cost (Track F catalog wired in Phase 12 Track F)",
		Handler: handleCost(env),
	})
	reg.Register(Spec{
		Name:    "clear",
		Summary: "clear the local event display (no server-side effect)",
		Handler: handleClear(env),
	})
	reg.Register(Spec{
		Name:    "compact",
		Summary: "request the agent to compact context next turn",
		Handler: handleCompact(env),
	})
	reg.Register(Spec{
		Name:    "model",
		Summary: "hint a model preference for the agent's next turn",
		Handler: handleModel(env),
	})
	reg.Register(Spec{
		Name:    "agents",
		Summary: "open AGENTS.md in $EDITOR (or print path + first 20 lines)",
		Handler: handleAgents(env),
	})
	reg.Register(Spec{
		Name:    "diff",
		Summary: "show shadow-git diff since last checkpoint",
		Handler: handleDiff(env),
	})
	reg.Register(Spec{
		Name:      "quit",
		Aliases:   []string{"exit", "q"},
		Summary:   "exit the surface (does not stop the run)",
		NoSession: true,
		Handler:   handleQuit(),
	})
}

// QuitSignal is returned by /quit. Surfaces inspect for it via
// errors.Is(err, ErrQuit) and translate to tea.Quit / loop break.
var ErrQuit = errors.New("slash: quit requested")

func handleHelp(reg *Registry) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		var sb strings.Builder
		sb.WriteString("Slash commands:\n")
		// Compute longest name for alignment so the table reads well at any
		// terminal width.
		specs := reg.List()
		maxName := 0
		for _, s := range specs {
			if l := len(s.Name); l > maxName {
				maxName = l
			}
		}
		for _, s := range specs {
			pad := strings.Repeat(" ", maxName-len(s.Name)+2)
			fmt.Fprintf(&sb, "  /%s%s%s", s.Name, pad, s.Summary)
			if len(s.Aliases) > 0 {
				sort.Strings(s.Aliases)
				fmt.Fprintf(&sb, " (aliases: /%s)", strings.Join(s.Aliases, ", /"))
			}
			sb.WriteString("\n")
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	}
}

func handleStatus(env *HandlerEnv) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		if env.SessionID == "" {
			return "", fmt.Errorf("no session attached")
		}
		if env.Fetcher == nil {
			return "", fmt.Errorf("status: no session fetcher configured")
		}
		info, err := env.Fetcher(ctx, env.SessionID)
		if err != nil {
			return "", fmt.Errorf("status: %w", err)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Session:    %s\n", info.ID)
		fmt.Fprintf(&sb, "Status:     %s\n", info.Status)
		fmt.Fprintf(&sb, "Workspace:  %s\n", info.WorkingDir)
		fmt.Fprintf(&sb, "Goal:       %s\n", info.GoalHint)
		if info.CurrentIteration > 0 {
			fmt.Fprintf(&sb, "Iteration:  %d\n", info.CurrentIteration)
		}
		if info.CurrentTokens > 0 {
			fmt.Fprintf(&sb, "Tokens:     %d (live), %d (total)\n", info.CurrentTokens, info.TotalTokens)
		} else {
			fmt.Fprintf(&sb, "Tokens:     %d (total)\n", info.TotalTokens)
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	}
}

func handleCost(env *HandlerEnv) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		if env.SessionID == "" {
			return "", fmt.Errorf("no session attached")
		}
		if env.Fetcher == nil {
			return "Phase 12 Track F (cost) not yet wired — coming soon", nil
		}
		info, err := env.Fetcher(ctx, env.SessionID)
		if err != nil {
			return "", fmt.Errorf("cost: %w", err)
		}
		// Server-side total_cost_usd is populated when a provider reports
		// usage; mock provider leaves it 0 and that's fine to show.
		out := fmt.Sprintf("Estimated cost: $%.4f USD (tokens: %d)", info.TotalCostUSD, info.TotalTokens)
		if info.TotalCostUSD == 0 {
			out += "\n(note: per-model price catalog lands in Phase 12 Track F)"
		}
		return out, nil
	}
}

func handleClear(env *HandlerEnv) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		if env.Local.ClearEvents != nil {
			env.Local.ClearEvents()
		}
		return "(local event display cleared — server history untouched)", nil
	}
}

func handleCompact(env *HandlerEnv) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		if env.SessionID == "" {
			return "", fmt.Errorf("no session attached")
		}
		if env.Run == nil {
			return "/compact: no run-control client configured", nil
		}
		queued, reason, err := env.Run.RequestCompact(ctx, env.SessionID)
		if err != nil {
			return "", fmt.Errorf("compact: %w", err)
		}
		if !queued {
			if reason == "" {
				reason = "not queued"
			}
			return fmt.Sprintf("/compact: %s", reason), nil
		}
		return "compact requested for next turn boundary", nil
	}
}

func handleModel(env *HandlerEnv) Handler {
	return func(ctx context.Context, cmd Command) (string, error) {
		if len(cmd.Args) == 0 {
			return "", fmt.Errorf("usage: /model <name>")
		}
		name := strings.TrimSpace(cmd.Args[0])
		if name == "" {
			return "", fmt.Errorf("usage: /model <name>")
		}
		if env.SessionID == "" {
			return "", fmt.Errorf("no session attached")
		}
		if env.Run == nil {
			return fmt.Sprintf("/model %s: no run-control client configured", name), nil
		}
		posted, reason, err := env.Run.PostHint(ctx, env.SessionID, map[string]string{"model": name})
		if err != nil {
			return "", fmt.Errorf("model: %w", err)
		}
		if !posted {
			if reason == "" {
				reason = "hint not posted"
			}
			return fmt.Sprintf("/model %s: %s", name, reason), nil
		}
		return fmt.Sprintf("model hint posted: %s (agent will consider it next turn)", name), nil
	}
}

func handleAgents(env *HandlerEnv) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		// Try the workspace AGENTS.md first; fall back to the global one.
		var path string
		if env.Fetcher != nil && env.SessionID != "" {
			info, err := env.Fetcher(ctx, env.SessionID)
			if err == nil && info.WorkingDir != "" {
				p := filepath.Join(info.WorkingDir, "AGENTS.md")
				if _, err := os.Stat(p); err == nil {
					path = p
				}
			}
		}
		if path == "" {
			global := env.Layout.AgentsFile()
			if _, err := os.Stat(global); err == nil {
				path = global
			}
		}
		if path == "" {
			// Run the discovery walk so the user sees what gil would inject
			// even when no canonical AGENTS.md exists at the obvious spots.
			if env.Fetcher != nil && env.SessionID != "" {
				info, err := env.Fetcher(ctx, env.SessionID)
				if err == nil && info.WorkingDir != "" {
					srcs, _ := instructions.Discover(instructions.DiscoverOptions{
						Workspace:       info.WorkingDir,
						StopAtGitRoot:   true,
						GlobalConfigDir: env.Layout.Config,
					})
					if len(srcs) > 0 {
						path = srcs[len(srcs)-1].Path
					}
				}
			}
		}
		if path == "" {
			return "no AGENTS.md found in workspace, global config, or ancestor chain", nil
		}

		// Open in $EDITOR only when stdout is a terminal AND we have an
		// editor configured. Otherwise just print the path + first 20
		// lines so the user can still see the contents from a piped CLI.
		editor := os.Getenv("EDITOR")
		if editor != "" && isTerminal(env.Stdout) {
			c := exec.CommandContext(ctx, editor, path)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return "", fmt.Errorf("agents: $EDITOR (%s) failed: %w", editor, err)
			}
			return fmt.Sprintf("opened %s in %s", path, editor), nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("agents: read %s: %w", path, err)
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > 20 {
			lines = lines[:20]
			lines = append(lines, "... [truncated; pass $EDITOR to open the full file]")
		}
		return fmt.Sprintf("%s:\n%s", path, strings.Join(lines, "\n")), nil
	}
}

func handleDiff(env *HandlerEnv) Handler {
	return func(ctx context.Context, _ Command) (string, error) {
		if env.SessionID == "" {
			return "", fmt.Errorf("no session attached")
		}
		// Preferred path: ask the daemon over the Diff RPC. The daemon
		// resolves workspace + spec + shadow-git in one place, applies
		// the truncation cap, and reports stable checkpoint SHAs even
		// when the surface is mid-render.
		if env.Run != nil {
			res, err := env.Run.Diff(ctx, env.SessionID)
			if err != nil {
				return "", fmt.Errorf("diff: %w", err)
			}
			return formatDiffResult(res), nil
		}
		// Legacy path: when no RunControl is wired (e.g. tests, or the
		// CLI before its main wires the SDK), fall back to reading the
		// shadow-git directly from the surface side. Behaviour matches
		// the Phase 12 implementation exactly so downstream tests keep
		// passing.
		if env.Fetcher == nil {
			return "", fmt.Errorf("diff: no session fetcher configured")
		}
		info, err := env.Fetcher(ctx, env.SessionID)
		if err != nil {
			return "", fmt.Errorf("diff: %w", err)
		}
		if info.WorkingDir == "" {
			return "diff: session has no workspace path", nil
		}
		shadowBase := filepath.Join(env.Layout.SessionsDir(), env.SessionID, "shadow")
		sg := checkpoint.New(info.WorkingDir, shadowBase)
		if _, statErr := os.Stat(filepath.Join(sg.GitDir, "HEAD")); os.IsNotExist(statErr) {
			return "diff: no checkpoints yet for this session", nil
		}
		commits, err := sg.ListCommits(ctx)
		if err != nil {
			return "", fmt.Errorf("diff: list checkpoints: %w", err)
		}
		if len(commits) == 0 {
			return "diff: no checkpoints yet for this session", nil
		}
		head := commits[0].SHA
		gitDir := sg.GitDir
		args := []string{
			"--git-dir=" + gitDir,
			"--work-tree=" + info.WorkingDir,
			"diff", head, "--",
		}
		var stdout, stderr bytes.Buffer
		c := exec.CommandContext(ctx, "git", args...)
		c.Stdout = &stdout
		c.Stderr = &stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("diff: git diff: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		out := stdout.String()
		if strings.TrimSpace(out) == "" {
			return fmt.Sprintf("diff: workspace matches checkpoint %s (no changes)", head[:8]), nil
		}
		return fmt.Sprintf("diff vs checkpoint %s:\n%s", head[:8], out), nil
	}
}

// formatDiffResult renders the SDK DiffResult into the human-readable
// blob the surfaces (TUI panel, CLI run --interactive line) display.
// Note short-circuits the body when the session has no checkpoints; an
// empty body with no note means "workspace matches the checkpoint".
func formatDiffResult(r *DiffResult) string {
	if r == nil {
		return "diff: empty response"
	}
	if r.Note != "" {
		return "diff: " + r.Note
	}
	if r.UnifiedDiff == "" {
		short := r.CheckpointSHA
		if len(short) > 8 {
			short = short[:8]
		}
		return fmt.Sprintf("diff: workspace matches checkpoint %s (no changes)", short)
	}
	short := r.CheckpointSHA
	if len(short) > 8 {
		short = short[:8]
	}
	header := fmt.Sprintf("diff vs checkpoint %s — %d files, +%d/-%d",
		short, r.FilesChanged, r.LinesAdded, r.LinesRemoved)
	if r.Truncated {
		header += fmt.Sprintf(" (truncated %d bytes)", r.TruncatedBytes)
	}
	return header + ":\n" + r.UnifiedDiff
}

func handleQuit() Handler {
	return func(context.Context, Command) (string, error) {
		// Returning a sentinel error lets surfaces dispatch tea.Quit / break
		// the read loop without inventing a magic output string.
		return "exiting…", ErrQuit
	}
}

// isTerminal returns true when w is *os.File pointing at a terminal.
// Bubbletea's surface passes a non-File writer (its renderer); the CLI
// passes os.Stdout. We use this to gate the $EDITOR fork: spawning an
// interactive editor while bubbletea owns the TTY would garble the
// screen.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
