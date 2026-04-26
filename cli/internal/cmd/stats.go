package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/core/cliutil"
	"github.com/jedutools/gil/core/cost"
	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/paths"
)

// statsCmd returns `gil stats [--days N] [--json]`. It walks the sessions
// dir, aggregates per-model token usage and cost across the window, and
// prints a per-model breakdown plus totals.
//
// gild does not need to be running — events are read straight off disk.
func statsCmd() *cobra.Command {
	var days int
	var asJSON bool
	c := &cobra.Command{
		Use:   "stats",
		Short: "Aggregate token usage and cost across sessions",
		Long: `Aggregate token usage and cost across sessions.

Walks the sessions data directory, reads each session's events.jsonl,
sums tokens per model, and applies the embedded cost catalog to produce
a USD estimate. The catalog can be overridden by writing your own JSON
to the gil cache directory's models.json.

Use --days to bound the window (default 30); a session is included when
its first event timestamp falls inside the window. --days 0 disables
the filter and aggregates over all-time.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if days < 0 {
				return cliutil.New(
					fmt.Sprintf("--days must be >= 0, got %d", days),
					`use --days 0 for all-time, --days 30 for the last 30 days, etc.`)
			}
			layout := defaultLayout()

			report, err := buildStats(layout, days, time.Now())
			if err != nil {
				return err
			}
			if asJSON {
				return writeStatsJSON(cmd.OutOrStdout(), report)
			}
			return writeStatsText(cmd.OutOrStdout(), report)
		},
	}
	c.Flags().IntVar(&days, "days", 30, "include sessions started within the last N days (0 = all)")
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of human-readable text")
	return c
}

// modelBreakdown is one row of the per-model aggregation.
type modelBreakdown struct {
	Model       string  `json:"model"`
	Sessions    int     `json:"sessions"`
	InputTokens int64   `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CachedRead  int64   `json:"cached_read_tokens"`
	CacheWrite  int64   `json:"cache_write_tokens"`
	CostUSD     float64 `json:"cost_usd"`
	Known       bool    `json:"model_known"`
}

// statsTotals are the aggregate-of-aggregates row.
type statsTotals struct {
	Sessions     int     `json:"sessions"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedRead   int64   `json:"cached_read_tokens"`
	CacheWrite   int64   `json:"cache_write_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type statsReport struct {
	Days     int              `json:"days"`
	Sessions int              `json:"session_count"`
	Totals   statsTotals      `json:"totals"`
	ByModel  []modelBreakdown `json:"by_model"`
}

// buildStats walks the sessions tree, applies the time-window filter, and
// aggregates per-model. Sessions whose events.jsonl is missing or whose
// first event is outside the window are skipped silently.
func buildStats(layout paths.Layout, days int, now time.Time) (statsReport, error) {
	sessionsDir := layout.SessionsDir()
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return statsReport{Days: days}, nil
		}
		return statsReport{}, fmt.Errorf("read sessions dir: %w", err)
	}

	cat, err := cost.LoadCatalog(layout.ModelCatalog())
	if err != nil {
		return statsReport{}, fmt.Errorf("load catalog: %w", err)
	}
	calc := &cost.Calculator{Catalog: cat}

	var cutoff time.Time
	if days > 0 {
		cutoff = now.Add(-time.Duration(days) * 24 * time.Hour)
	}

	byModel := map[string]*modelBreakdown{}
	totals := statsTotals{}

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		eventsPath := filepath.Join(sessionsDir, ent.Name(), "events", "events.jsonl")
		evs, err := event.LoadAll(eventsPath)
		if err != nil {
			// Missing or unreadable: treat as an empty session.
			continue
		}
		if len(evs) == 0 {
			continue
		}
		// Window filter: include if first event is after cutoff.
		if !cutoff.IsZero() && evs[0].Timestamp.Before(cutoff) {
			continue
		}

		usage, model := aggregateUsage(evs)
		if model == "" && usage.Input == 0 && usage.Output == 0 {
			// Nothing useful to count for this session.
			continue
		}
		if model == "" {
			model = "(unknown)"
		}
		row := byModel[model]
		if row == nil {
			row = &modelBreakdown{Model: model}
			byModel[model] = row
		}
		row.Sessions++
		row.InputTokens += usage.Input
		row.OutputTokens += usage.Output
		row.CachedRead += usage.CachedRead
		row.CacheWrite += usage.CacheWrite

		usd, ok := calc.Estimate(model, cost.Usage{
			InputTokens:      usage.Input,
			OutputTokens:     usage.Output,
			CachedReadTokens: usage.CachedRead,
			CacheWriteTokens: usage.CacheWrite,
		})
		if ok {
			row.Known = true
			row.CostUSD += usd
			totals.CostUSD += usd
		}

		totals.Sessions++
		totals.InputTokens += usage.Input
		totals.OutputTokens += usage.Output
		totals.CachedRead += usage.CachedRead
		totals.CacheWrite += usage.CacheWrite
	}

	rows := make([]modelBreakdown, 0, len(byModel))
	for _, r := range byModel {
		rows = append(rows, *r)
	}
	// Sort by total tokens desc so the biggest spend lands on top.
	sort.Slice(rows, func(i, j int) bool {
		ti := rows[i].InputTokens + rows[i].OutputTokens
		tj := rows[j].InputTokens + rows[j].OutputTokens
		if ti == tj {
			return rows[i].Model < rows[j].Model
		}
		return ti > tj
	})

	return statsReport{
		Days:     days,
		Sessions: totals.Sessions,
		Totals:   totals,
		ByModel:  rows,
	}, nil
}

func writeStatsText(w io.Writer, r statsReport) error {
	header := "All-time"
	if r.Days > 0 {
		header = fmt.Sprintf("Past %d days", r.Days)
	}
	fmt.Fprintf(w, "%s: %d sessions\n\n", header, r.Sessions)
	if r.Sessions == 0 {
		fmt.Fprintln(w, "no sessions to summarise")
		return nil
	}
	fmt.Fprintln(w, "By model:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range r.ByModel {
		tokens := row.InputTokens + row.OutputTokens + row.CachedRead + row.CacheWrite
		fmt.Fprintf(tw, "  %s\t%d sessions\t%s tokens\t%s\n",
			row.Model,
			row.Sessions,
			formatTokensCompact(tokens),
			formatCost(row.CostUSD, row.Known),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	totalTokens := r.Totals.InputTokens + r.Totals.OutputTokens + r.Totals.CachedRead + r.Totals.CacheWrite
	fmt.Fprintf(w, "\nTotal: %d sessions, %s tokens, $%.2f\n",
		r.Totals.Sessions,
		formatTokensCompact(totalTokens),
		r.Totals.CostUSD,
	)
	return nil
}

func writeStatsJSON(w io.Writer, r statsReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// formatTokensCompact renders a token count as e.g. "2.1M", "812K", "412".
// Mirrors opencode/stats.ts formatNumber so users can compare side by side.
func formatTokensCompact(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatCost renders cost or "n/a" when the model isn't in the catalog.
func formatCost(usd float64, known bool) string {
	if !known {
		return "n/a (model not in catalog)"
	}
	return fmt.Sprintf("$%.2f", usd)
}
