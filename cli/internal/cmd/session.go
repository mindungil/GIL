package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/sdk"
)

// sessionCmd is the parent of `gil session list / rm / show`.
//
// The three operate on the same store as `gil status` but each carries
// a slightly different intent — list mirrors status (with a
// `--all-statuses` shortcut once we surface archived rows), rm prunes
// (single id, batch by status, or by age), show is a one-screen
// per-session pretty-print. Each refuses to act on a RUNNING session
// when the action would be destructive — those checks live in the
// SessionService.Delete RPC, not here, so the same guard applies to
// every caller (CLI, TUI, future MCP tool).
func sessionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "session",
		Short: "Manage sessions (list, remove, show)",
		Long: `Inspect and prune sessions tracked by gild.

The list / show subcommands are read-only convenience views over the
SessionService.List / Get RPCs. The rm subcommand is destructive — it
unlinks the per-session workspace directory under SessionsDir/<id> in
addition to removing the SQL row. RUNNING sessions are refused; stop
them with the corresponding ` + "`gil`" + ` command first.`,
	}
	c.AddCommand(sessionListCmd())
	c.AddCommand(sessionRmCmd())
	c.AddCommand(sessionShowCmd())
	return c
}

// sessionListCmd is functionally a thin wrapper around `gil status`
// today — kept as a discoverable verb under the `session` group so a
// user who types `gil session <TAB>` finds it next to rm/show. We do
// NOT alias it to statusCmd() at the cobra level because the two
// surfaces may diverge later (e.g. session list could grow
// `--include-archived` filters that status does not need).
func sessionListCmd() *cobra.Command {
	var socket string
	var limit int
	var plain bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		Long: `List sessions tracked by gild, newest first.

The default rendering mirrors ` + "`gil status`" + ` — a "card" per session
with a sub-cell progress bar. --plain emits the legacy tab-separated
table for scripts; --output json (root flag) emits structured JSON.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return cliutil.New(
					fmt.Sprintf("--limit must be positive, got %d", limit),
					`try --limit 100 (or any positive integer)`)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			list, err := cli.ListSessions(ctx, limit)
			if err != nil {
				return wrapRPCError(err)
			}
			if outputJSON() {
				return writeStatusJSON(cmd.OutOrStdout(), list)
			}
			if plain {
				return writeStatusText(cmd.OutOrStdout(), list)
			}
			return writeStatusVisual(cmd.OutOrStdout(), list, asciiMode)
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().IntVar(&limit, "limit", 100, "max sessions to list")
	c.Flags().BoolVar(&plain, "plain", false, "use the legacy tab-separated table (script friendly)")
	return c
}

// sessionShowCmd is the per-session "card detail" view. It does NOT
// stream events — for that the user has `gil events <id> --tail` and
// `gil watch <id>`. Show is a one-shot, mostly metadata, designed for
// a quick "what was this session about?" recall.
func sessionShowCmd() *cobra.Command {
	var socket string
	c := &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show a single session's metadata, spec preview, and event count",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			sess, err := cli.GetSession(ctx, id)
			if err != nil {
				return wrapRPCError(err)
			}
			layout, _ := paths.FromEnv()
			eventsCount := countEvents(filepath.Join(layout.SessionsDir(), id, "events", "events.jsonl"))
			specPath := filepath.Join(layout.SessionsDir(), id, "spec.json")
			specPreview := readSpecPreview(specPath)
			if outputJSON() {
				return writeSessionShowJSON(cmd.OutOrStdout(), sess, eventsCount, specPreview)
			}
			writeSessionShow(cmd.OutOrStdout(), sess, eventsCount, specPreview, asciiMode)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	return c
}

// sessionRmCmd is the destructive entrypoint. The flag matrix is
// deliberately mutually exclusive at the surface — exactly one of
// (positional id | --status | --older-than | --all) must be supplied
// — to keep the user's intent unambiguous. We resolve the candidate
// list first, then prompt (or skip with --yes), then delete one by
// one and tally the freed bytes.
func sessionRmCmd() *cobra.Command {
	var socket string
	var statusFilter string
	var olderThan string
	var all bool
	var yes bool
	var limit int
	c := &cobra.Command{
		Use:   "rm [<session-id>]",
		Short: "Remove sessions (single id, by status, by age, or all)",
		Long: `Remove sessions and their per-session workspace directories.

Modes (exactly one):
  rm <id>             remove a single session by id
  rm --status DONE    remove every session matching the status
  rm --older-than 7d  remove sessions whose last event is older than N (units: h, d)
  rm --all            remove every session (requires explicit confirmation)

RUNNING sessions are refused — stop them first. By default the command
prompts for confirmation before deleting; pass --yes to skip.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modeCount := 0
			if len(args) == 1 {
				modeCount++
			}
			if statusFilter != "" {
				modeCount++
			}
			if olderThan != "" {
				modeCount++
			}
			if all {
				modeCount++
			}
			if modeCount == 0 {
				return cliutil.New(
					"no targets specified",
					`pass <session-id>, or one of --status, --older-than, --all`)
			}
			if modeCount > 1 {
				return cliutil.New(
					"choose exactly one of <id>, --status, --older-than, --all",
					`combining filters is intentionally rejected so the user's intent is unambiguous`)
			}
			if limit <= 0 {
				return cliutil.New(
					fmt.Sprintf("--limit must be positive, got %d", limit),
					`try --limit 1000`)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			// Resolve the candidate list.
			var targets []*sdk.Session
			if len(args) == 1 {
				sess, err := cli.GetSession(ctx, args[0])
				if err != nil {
					return wrapRPCError(err)
				}
				targets = []*sdk.Session{sess}
			} else {
				list, err := cli.ListSessions(ctx, limit)
				if err != nil {
					return wrapRPCError(err)
				}
				layout, _ := paths.FromEnv()
				targets = filterSessionsForRm(list, statusFilter, olderThan, all, layout.SessionsDir())
			}
			if len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "   no sessions match — nothing to remove")
				return nil
			}

			// Refuse if any target is running — surfacing the guard at
			// the CLI gives a single, batched error instead of N
			// individual FailedPrecondition errors mid-loop.
			for _, t := range targets {
				if strings.EqualFold(t.Status, "RUNNING") {
					return cliutil.New(
						fmt.Sprintf("session %s is currently RUNNING", shortID(t.ID)),
						`wait for the run to finish (or stop it via the TUI), then retry`)
				}
			}

			if !yes {
				if err := promptDeleteConfirm(cmd, targets); err != nil {
					return err
				}
			}

			out := cmd.OutOrStdout()
			g := uistyle.NewGlyphs(asciiMode)
			p := uistyle.NewPalette(false)
			batch := len(targets) > 1
			if batch {
				summary := summariseStatuses(targets)
				fmt.Fprintf(out, "   removing %d sessions%s...\n", len(targets), summary)
			}
			var totalFreed int64
			var removed int
			for _, t := range targets {
				freed, err := cli.DeleteSession(ctx, t.ID)
				if err != nil {
					if status.Code(err) == codes.NotFound {
						fmt.Fprintf(out, "   %s session %s no longer exists\n",
							p.Caution(g.Warn), shortID(t.ID))
						continue
					}
					return wrapRPCError(err)
				}
				totalFreed += freed
				removed++
				if !batch {
					// Single-id rm: emit the per-session "✓ removed" line.
					meta := []string{}
					if t.Status != "" {
						meta = append(meta, "was "+strings.ToUpper(t.Status))
					}
					if t.CurrentIteration > 0 {
						meta = append(meta, fmt.Sprintf("%d iters", t.CurrentIteration))
					}
					if t.TotalCostUSD > 0 {
						meta = append(meta, fmt.Sprintf("$%0.2f", t.TotalCostUSD))
					}
					if freed > 0 {
						meta = append(meta, humanBytes(freed))
					}
					detail := ""
					if len(meta) > 0 {
						detail = " (" + strings.Join(meta, ", ") + ")"
					}
					fmt.Fprintf(out, "   %s removed session %s%s\n",
						p.Success(g.Done), p.Primary(shortID(t.ID)), p.Dim(detail))
				}
			}
			if batch {
				fmt.Fprintf(out, "   %s removed %d sessions (freed %s)\n",
					p.Success(g.Done), removed, humanBytes(totalFreed))
			}
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&statusFilter, "status", "", "remove every session matching this status (e.g. DONE, STUCK)")
	c.Flags().StringVar(&olderThan, "older-than", "", "remove sessions older than the duration (e.g. 7d, 24h)")
	c.Flags().BoolVar(&all, "all", false, "remove ALL sessions (requires confirm or --yes)")
	c.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	c.Flags().IntVar(&limit, "limit", 1000, "max sessions to enumerate when filtering")
	return c
}

// filterSessionsForRm narrows list down to the rows that match the
// provided filter. statusFilter is matched case-insensitively. olderThan
// is parsed via parseAge and compared against the more-recent of (last
// event mtime, session UpdatedAt). all=true returns the entire list
// without filtering.
//
// sessionsDir is the on-disk root we consult for the events.jsonl mtime
// — passed explicitly so tests can isolate it via t.TempDir without
// touching the real XDG layout.
func filterSessionsForRm(list []*sdk.Session, statusFilter, olderThan string, all bool, sessionsDir string) []*sdk.Session {
	var dur time.Duration
	if olderThan != "" {
		dur, _ = parseAge(olderThan) // invalid → zero; matches "no filter"
	}
	now := time.Now()
	out := make([]*sdk.Session, 0, len(list))
	for _, s := range list {
		if s == nil {
			continue
		}
		if all {
			out = append(out, s)
			continue
		}
		if statusFilter != "" && !strings.EqualFold(s.Status, statusFilter) {
			continue
		}
		if dur > 0 {
			ref := s.UpdatedAt
			if ev := lastEventMtime(filepath.Join(sessionsDir, s.ID, "events", "events.jsonl")); !ev.IsZero() {
				if ev.After(ref) {
					ref = ev
				}
			}
			if ref.IsZero() {
				ref = s.CreatedAt // best effort
			}
			if ref.IsZero() || now.Sub(ref) < dur {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// parseAge accepts forms like "24h", "7d", "1h30m". Unsuffixed numbers
// are rejected so a typo ("7" vs "7d") does not silently mean
// "7 nanoseconds". The "d" unit is our addition on top of stdlib
// time.ParseDuration which only understands h/m/s.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Days: replace trailing "d" before ParseDuration sees it.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid days component %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}

// lastEventMtime returns the modification time of the per-session
// events.jsonl. Zero on missing file or read error — callers treat
// that as "no recorded activity".
func lastEventMtime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// countEvents returns the number of newline-terminated records in the
// session's events.jsonl. Reading the whole file is fine for the
// foreseeable future — even a long-running session caps out at a few
// thousand events.
func countEvents(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	// Allow long lines (single agent_turn payloads can be wide).
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) > 0 {
			n++
		}
	}
	return n
}

// readSpecPreview returns the first ~200 bytes of the spec file as a
// safe single-line string (newlines collapsed). Empty when the file
// is missing.
func readSpecPreview(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(data)
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " ")
}

// summariseStatuses produces "(45 DONE, 2 STUCK)" for the batch
// "removing N sessions" line. Returns "" when targets is empty so the
// caller can concatenate without conditionals.
func summariseStatuses(targets []*sdk.Session) string {
	if len(targets) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, t := range targets {
		counts[strings.ToUpper(t.Status)]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// promptDeleteConfirm writes the y/N prompt to stderr and reads a
// single line from stdin. Default-deny: only "y" / "yes" (case
// insensitive) accepts; everything else cancels. Returns a typed
// "operation cancelled" error so the caller exits non-zero.
//
// Reading from os.Stdin (not cmd.InOrStdin) is intentional: cobra's
// in-memory stdin during tests bypasses the prompt by feeding empty
// input → cancellation, which is exactly the safe default we want.
func promptDeleteConfirm(cmd *cobra.Command, targets []*sdk.Session) error {
	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)
	out := cmd.ErrOrStderr()
	noun := "session"
	if len(targets) != 1 {
		noun = "sessions"
	}
	summary := summariseStatuses(targets)
	fmt.Fprintf(out, "   %s This will remove %d %s%s.\n",
		p.Caution(g.Warn), len(targets), noun, summary)
	fmt.Fprintf(out, "   Continue? %s ", p.Dim("[y/N]"))

	r := bufio.NewReader(cmd.InOrStdin())
	line, _ := r.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return cliutil.New(
		"cancelled",
		`pass --yes to skip the prompt`)
}

// humanBytes renders n as "12.3 MB", "456 KB", "789 B". Uses 1024
// bases (KiB-style) but writes the SI labels for legibility — same
// trick docker / kubectl use for `--all` cleanup feedback.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024.0)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024.0*1024.0))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024.0*1024.0*1024.0))
	}
}

// writeSessionShow renders the human-readable per-session card. The
// layout mirrors the spec mockup for `gil watch` — a bordered header
// line then a metadata key/value column. We reuse uistyle so colour /
// glyph swaps follow the global flags.
func writeSessionShow(w io.Writer, s *sdk.Session, events int, specPreview string, ascii bool) {
	g := uistyle.NewGlyphs(ascii)
	p := uistyle.NewPalette(false)

	marker, role := sessionStatusGlyph(g, s.Status)
	col := colourMarker(p, marker, role)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "   %s  %s  %s\n",
		col, p.Primary(shortID(s.ID)), p.Surface(truncRune(s.GoalHint, 60)))
	fmt.Fprintln(w)

	row := func(k, v string) {
		fmt.Fprintf(w, "   %-12s %s\n", p.Dim(k), v)
	}
	row("Status", s.Status)
	row("Working dir", s.WorkingDir)
	if !s.CreatedAt.IsZero() {
		row("Created", s.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if !s.UpdatedAt.IsZero() {
		row("Updated", s.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if s.SpecID != "" {
		row("Spec", s.SpecID)
	}
	row("Events", fmt.Sprintf("%d", events))
	if s.CurrentIteration > 0 {
		row("Iteration", fmt.Sprintf("%d", s.CurrentIteration))
	}
	if s.TotalTokens > 0 {
		row("Tokens", fmt.Sprintf("%d", s.TotalTokens))
	}
	if s.TotalCostUSD > 0 {
		row("Cost", fmt.Sprintf("$%0.2f", s.TotalCostUSD))
	}
	if specPreview != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "   %s\n", p.Dim("spec preview"))
		fmt.Fprintf(w, "   %s %s\n", p.Dim(g.QuoteBar), truncRune(specPreview, 240))
	}
	fmt.Fprintln(w)
}

// sessionShowJSON is the structured shape emitted under --output json.
// We carry the same values the human renderer prints (rather than the
// full proto) so consumers do not need to re-parse the SDK type to
// pick out the show-relevant fields.
type sessionShowJSON struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	WorkingDir   string  `json:"working_dir"`
	GoalHint     string  `json:"goal_hint"`
	SpecID       string  `json:"spec_id"`
	CreatedAt    string  `json:"created_at,omitempty"`
	UpdatedAt    string  `json:"updated_at,omitempty"`
	Events       int     `json:"events"`
	Iteration    int32   `json:"iteration"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	SpecPreview  string  `json:"spec_preview,omitempty"`
}

func writeSessionShowJSON(w io.Writer, s *sdk.Session, events int, specPreview string) error {
	out := sessionShowJSON{
		ID:           s.ID,
		Status:       s.Status,
		WorkingDir:   s.WorkingDir,
		GoalHint:     s.GoalHint,
		SpecID:       s.SpecID,
		Events:       events,
		Iteration:    s.CurrentIteration,
		TotalTokens:  s.TotalTokens,
		TotalCostUSD: s.TotalCostUSD,
		SpecPreview:  specPreview,
	}
	if !s.CreatedAt.IsZero() {
		out.CreatedAt = s.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = s.UpdatedAt.UTC().Format(time.RFC3339)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
