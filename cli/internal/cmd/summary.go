package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/version"
	"github.com/mindungil/gil/sdk"
)

// runSummary is the no-arg entry-point. It is called from Root() when
// the user runs `gil` without any subcommand. The command intentionally
// does not return a *cobra.Command of its own — it lives behind
// Root().Run so that `gil --help` keeps cobra's standard behaviour and
// only the bare `gil` invocation routes here.
//
// The flow:
//  1. ensure the daemon is up (ensureDaemon spawns it if not)
//  2. list sessions
//  3. branch on the count: zero → onboarding hint; >0 → mission-control summary
//
// All visual output goes through uistyle so the renderer is testable
// against fixtures in cli/internal/cmd/testdata/.
func runSummary(out io.Writer, socket, base string, ascii bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ensureDaemon(socket, base); err != nil {
		return err
	}
	cli, err := sdk.Dial(socket)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer cli.Close()

	list, err := cli.ListSessions(ctx, 100)
	if err != nil {
		return wrapRPCError(err)
	}

	g := uistyle.NewGlyphs(ascii)
	p := uistyle.NewPalette(false)

	rows := make([]summaryRow, 0, len(list))
	for _, s := range list {
		if s == nil {
			continue
		}
		rows = append(rows, summaryRowFromSession(s))
	}

	total := len(rows)
	const maxRows = 10
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}

	renderSummary(out, summaryEnv{
		Version:       version.Short(),
		User:          currentUser(),
		Host:          currentHost(),
		Now:           time.Now(),
		Glyphs:        g,
		Palette:       p,
		Sessions:      rows,
		TotalSessions: total,
	})
	return nil
}

// summaryEnv collects everything renderSummary needs. Pulling it into
// a struct keeps the renderer pure (no env / clock access) so the
// snapshot tests can drive deterministic output.
type summaryEnv struct {
	Version  string
	User     string
	Host     string
	Now      time.Time
	Glyphs   uistyle.Glyphs
	Palette  uistyle.Palette
	Sessions      []summaryRow
	TotalSessions int // total count before truncation; renderer emits overflow hint
}

// summaryRow is the renderable view of one session. The row carries
// pre-computed display values so the renderer doesn't reach back into
// the SDK type — keeps the tests trivial.
//
// CostBudget / TokensBudget are zero when the spec didn't set a cap on
// that dimension; the cost cell renders as the bare value in that case.
// BudgetExceeded is the sticky bit from the runner's budget_exceeded
// event so a finished-budget session keeps the alert glyph after it
// has stopped.
type summaryRow struct {
	ID             string
	Status         string // "RUNNING" / "DONE" / "STUCK" / ...
	Iter           int32
	MaxIter        int32
	Tokens         int64
	TokensBudget   int64
	CostUSD        float64
	CostBudget     float64
	BudgetExceeded bool
	Goal           string
	StuckNote      string // "RepeatedAction (2/3)" or empty

	// PlanCompleted / PlanTotal: when PlanTotal > 0 the iter cell
	// is augmented with "plan C/T" so the user sees plan progress
	// alongside the iter count. PlanTotal == 0 means "no plan yet"
	// and the cell renders as before.
	PlanCompleted int
	PlanTotal     int
}

func summaryRowFromSession(s *sdk.Session) summaryRow {
	row := summaryRow{
		ID:             s.ID,
		Status:         s.Status,
		Iter:           s.CurrentIteration,
		MaxIter:        100, // server doesn't expose max yet; matches spec mockup
		Tokens:         s.CurrentTokens,
		TokensBudget:   s.BudgetMaxTokens,
		CostUSD:        s.TotalCostUSD,
		CostBudget:     s.BudgetMaxCostUSD,
		BudgetExceeded: s.BudgetExceeded,
		Goal:           s.GoalHint,
	}
	// Best-effort plan progress: read <SessionsDir>/<id>/plan.json
	// directly. Failure or missing file → leave PlanTotal=0 so the
	// renderer falls back to plain iter display.
	if comp, total, ok := loadSessionPlanCounts(s.ID); ok {
		row.PlanCompleted = comp
		row.PlanTotal = total
	}
	return row
}

// renderSummary is the full no-arg layout. Any change here must be
// kept in sync with docs/design/terminal-aesthetic.md §7 and the
// fixtures under testdata/summary_*.golden.
func renderSummary(out io.Writer, e summaryEnv) {
	g, p := e.Glyphs, e.Palette

	// Header — "G I L  v…  user  ●  host". Letterspaced display per
	// spec §2 typography.
	fmt.Fprintln(out)
	headLeft := p.Primary("G I L") + "  " + p.Dim(e.Version)
	headRight := e.User + "  " + p.Info(g.Running) + "  " + e.Host
	fmt.Fprintf(out, "%s%s%s\n", headLeft, padBetween(headLeft, headRight, 78), headRight)
	fmt.Fprintln(out)

	if len(e.Sessions) == 0 {
		fmt.Fprintf(out, "   %s\n\n", p.Surface("No sessions yet."))
		fmt.Fprintf(out, "   %s  %s            %s\n",
			p.Info(g.Arrow), p.Primary("gil interview"), p.Dim("start a new task"))
		fmt.Fprintf(out, "   %s  %s              %s\n",
			p.Info(g.Arrow), p.Primary("gil --help"), p.Dim("see commands"))
		fmt.Fprintf(out, "   %s  %s                %s\n",
			p.Info(g.Arrow), p.Primary("gil doctor"), p.Dim("check setup"))
		fmt.Fprintln(out)
		return
	}

	noun := "session"
	if len(e.Sessions) != 1 {
		noun = "sessions"
	}
	fmt.Fprintf(out, "   %s\n\n", p.Surface(fmt.Sprintf("%d %s", len(e.Sessions), noun)))

	for _, r := range e.Sessions {
		marker, mark := sessionStatusGlyph(g,r.Status)
		coloured := colourMarker(p, marker, mark)
		bar := uistyle.BarFixed(g, int(r.Iter), int(r.MaxIter))
		// "23/100" or "45" depending on whether status is RUNNING (need denominator)
		iterStr := iterDisplay(r)
		costStr := renderCostCell(g, p, r)
		// 14-char goal column — truncate with ellipsis if longer so the
		// row stays single-line under the spec's 80-col target.
		goal := truncRune(r.Goal, 48)
		fmt.Fprintf(out, "   %s  %s   %s   %-7s  %-18s %s\n",
			coloured, p.Dim(shortID(r.ID)), bar, iterStr, costStr, goal)
		if r.StuckNote != "" {
			indent := strings.Repeat(" ", 49) // hand-aligned with cost column
			fmt.Fprintf(out, "%s%s %s\n", indent, p.Caution(g.Warn),
				p.Caution("STUCK · "+r.StuckNote))
		}
	}

	if e.TotalSessions > len(e.Sessions) {
		extra := e.TotalSessions - len(e.Sessions)
		fmt.Fprintf(out, "   %s\n", p.Dim(fmt.Sprintf("›  + %d more", extra)))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out)
	// Next-step row — three suggestions, top line; bottom line has
	// secondary actions per spec §7.
	row1 := []string{
		fmt.Sprintf("%s  %s", p.Info(g.Arrow), p.Primary("gil status")),
	}
	if len(e.Sessions) > 0 {
		row1 = append(row1,
			fmt.Sprintf("%s  %s %s", p.Info(g.Arrow), p.Primary("gil watch"), p.Dim(shortID(e.Sessions[0].ID))),
			fmt.Sprintf("%s  %s %s --tail", p.Info(g.Arrow), p.Primary("gil events"), p.Dim(shortID(e.Sessions[len(e.Sessions)-1].ID))),
		)
	}
	fmt.Fprintf(out, "   %s\n", strings.Join(row1, "       "))
	fmt.Fprintf(out, "   %s  %s     %s  %s\n",
		p.Info(g.Arrow), p.Primary("gil interview"),
		p.Info(g.Arrow), p.Primary("gil --help"))
	fmt.Fprintln(out)
}

// renderCostCell returns the cost column for one summary/status row.
// When the session has a cost budget set the cell shows "used / total"
// with a warn or alert glyph at the threshold bands; otherwise it
// degrades to the bare "$X.YY" so legacy sessions render unchanged.
//
// Aesthetic per terminal-aesthetic.md §1: amber (Caution) at >= 75%,
// coral (Alert) at >= 100% or when the budget_exceeded sticky bit is
// set. Never both at once on the same row.
func renderCostCell(g uistyle.Glyphs, p uistyle.Palette, r summaryRow) string {
	bare := fmt.Sprintf("$%0.2f", r.CostUSD)
	if r.CostBudget <= 0 {
		return bare
	}
	frac := r.CostUSD / r.CostBudget
	body := fmt.Sprintf("$%0.2f / $%0.2f", r.CostUSD, r.CostBudget)
	switch {
	case r.BudgetExceeded || frac >= 1.0:
		return p.Alert(g.Failed + " " + body)
	case frac >= 0.75:
		return p.Caution(g.Warn + " " + body)
	default:
		return body
	}
}

// sessionStatusGlyph maps a session status string to (glyph, palette-role-name).
// The role name lets the caller pick the right Palette method without
// forcing this package to import the Palette receiver — which keeps the
// glyph→role mapping easy to unit-test.
//
// Named with the `session` prefix to avoid colliding with doctor.go's
// statusGlyph(Status) which serves the doctor health-check view.
func sessionStatusGlyph(g uistyle.Glyphs, status string) (glyph, role string) {
	switch strings.ToUpper(status) {
	case "RUNNING":
		return g.Running, "info"
	case "DONE", "COMPLETED":
		return g.Done, "success"
	case "STUCK", "ERROR", "FAILED":
		return g.Warn, "caution"
	case "PAUSED":
		return g.Paused, "caution"
	default:
		return g.Idle, "dim"
	}
}

func colourMarker(p uistyle.Palette, glyph, role string) string {
	switch role {
	case "info":
		return p.Info(glyph)
	case "success":
		return p.Success(glyph)
	case "caution":
		return p.Caution(glyph)
	case "alert":
		return p.Alert(glyph)
	case "dim":
		return p.Dim(glyph)
	default:
		return glyph
	}
}

// iterDisplay formats the iter column. RUNNING shows "iter/max"; DONE
// shows just the final iter (max not meaningful post-finish). Matches
// the spec mockup column alignment.
//
// Phase 18: when the row has a plan (PlanTotal > 0), append "plan C/T"
// after the iter so a glance picks up "iter 23/100  plan 1/3". The
// suffix is omitted when there's no plan, keeping the legacy column
// width for plan-less sessions.
func iterDisplay(r summaryRow) string {
	base := fmt.Sprintf("%d", r.Iter)
	if strings.EqualFold(r.Status, "RUNNING") {
		base = fmt.Sprintf("%d/%d", r.Iter, r.MaxIter)
	}
	if r.PlanTotal > 0 {
		return fmt.Sprintf("%s  plan %d/%d", base, r.PlanCompleted, r.PlanTotal)
	}
	return base
}

// loadSessionPlanCounts reads <SessionsDir>/<sessionID>/plan.json and
// returns (completed, total, ok). ok=false on any failure (missing
// file, malformed JSON, no items) so callers can fall back to the
// no-plan rendering. Sub-items count toward the totals — they're
// independently checkable steps from the agent's perspective.
func loadSessionPlanCounts(sessionID string) (completed, total int, ok bool) {
	if sessionID == "" {
		return 0, 0, false
	}
	layout, err := paths.FromEnv()
	if err != nil {
		return 0, 0, false
	}
	body, err := os.ReadFile(filepath.Join(layout.SessionsDir(), sessionID, "plan.json"))
	if err != nil {
		return 0, 0, false
	}
	var d struct {
		Items []struct {
			Status string `json:"status"`
			Sub    []struct {
				Status string `json:"status"`
			} `json:"sub"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return 0, 0, false
	}
	if len(d.Items) == 0 {
		return 0, 0, false
	}
	for _, it := range d.Items {
		total++
		if it.Status == "completed" {
			completed++
		}
		for _, sub := range it.Sub {
			total++
			if sub.Status == "completed" {
				completed++
			}
		}
	}
	return completed, total, true
}

// loadSessionPlanNext returns the text of the first non-completed
// item in the plan (in_progress preferred over pending). Returns ""
// when the plan is missing, malformed, or fully completed. Used by
// `gil watch` to show "next:" alongside the plan progress ratio.
func loadSessionPlanNext(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	layout, err := paths.FromEnv()
	if err != nil {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(layout.SessionsDir(), sessionID, "plan.json"))
	if err != nil {
		return ""
	}
	var d struct {
		Items []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return ""
	}
	// Prefer in_progress; fall back to first pending.
	for _, it := range d.Items {
		if it.Status == "in_progress" {
			return it.Text
		}
	}
	for _, it := range d.Items {
		if it.Status != "completed" {
			return it.Text
		}
	}
	return ""
}

// shortID returns the first 6 chars of the ULID lowercased — enough
// to disambiguate within a working set, narrow enough for the table.
// (Same convention the spec mockups use.) Lowercasing keeps the
// rendering uniform whether the ID came in as upper or mixed case.
func shortID(id string) string {
	if len(id) <= 6 {
		return strings.ToLower(id)
	}
	return strings.ToLower(id[:6])
}

// truncRune cuts s to width with a trailing single-char ellipsis when
// it had to. Width is in runes — important for any goal strings that
// happen to contain non-ASCII text.
//
// Named distinctly from export.go's truncate (which is byte-based and
// used for log line shortening) so the two intents stay separable.
func truncRune(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// padBetween computes the spaces needed to right-justify `right`
// after `left` within `total` cells. Falls back to two spaces when
// the inputs already overflow — never returns a negative count.
//
// We treat ANSI SGR sequences as zero-width: the strings.Builder
// loops below count rune width by ignoring everything between ESC
// and 'm'. This keeps the header alignment correct under both
// colorised and NO_COLOR modes.
func padBetween(left, right string, total int) string {
	used := visibleWidth(left) + visibleWidth(right)
	if used >= total {
		return "  "
	}
	return strings.Repeat(" ", total-used)
}

// visibleWidth strips ANSI SGR escapes and returns the rune count.
// Good enough for our header/footer alignment — we never embed
// double-width East Asian glyphs in those rows so 1 rune == 1 column.
func visibleWidth(s string) int {
	w := 0
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		w++
	}
	return w
}

// currentUser returns a best-effort short username. Used in the
// header — when it cannot be resolved (containers without /etc/passwd
// entries) we fall back to "user" so the line still renders.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "user"
}

// currentHost returns the short hostname (or "host" when even the
// system call fails).
func currentHost() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		// Strip any trailing .local / .lan suffix to keep the header
		// short. We split on the first dot rather than walking — the
		// spec wants a glanceable identifier, not a fully-qualified
		// name.
		if i := strings.IndexByte(h, '.'); i > 0 {
			return h[:i]
		}
		return h
	}
	return "host"
}
