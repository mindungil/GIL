package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/core/cliutil"
	"github.com/jedutools/gil/core/cost"
	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/paths"
)

// costCmd returns the `gil cost [<session-id>]` command. It reads the
// per-session events JSONL directly (no daemon required), aggregates token
// usage, and prints a USD estimate using the embedded cost catalog
// (overridable via Cache/models.json).
func costCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "cost [session-id]",
		Short: "Show token usage and USD cost estimate for a session",
		Long: `Show token usage and a USD cost estimate for a single session.

Reads the session's events.jsonl directly — gild does not need to be
running. Costs are computed from the embedded model price catalog (see
core/cost/default_catalog.json); override it by writing your own JSON to
the gil cache directory's models.json.

When no session-id is given, the most recent session under the data dir
is used (lexicographic ULID ordering).

Prices are best-effort public list prices and may be stale.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			layout := defaultLayout()

			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			if id == "" {
				latest, err := latestSessionID(layout.SessionsDir())
				if err != nil {
					return err
				}
				id = latest
			}

			report, err := buildSessionCost(layout, id)
			if err != nil {
				return err
			}
			// Back-compat: the older per-command --json flag wins when set,
			// then we fall through to the persistent --output flag. This
			// keeps existing scripts (`gil cost --json`) byte-identical
			// while letting new callers reach for `--output json` uniformly.
			if asJSON || outputJSON() {
				return writeCostJSON(cmd.OutOrStdout(), report)
			}
			return writeCostText(cmd.OutOrStdout(), report)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of human-readable text (alias for --output json)")
	return c
}

// sessionCostReport is the structured result returned by buildSessionCost
// and rendered in either text or JSON form.
type sessionCostReport struct {
	Session  string       `json:"session"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Tokens   tokenSummary `json:"tokens"`
	CostUSD  float64      `json:"cost_usd"`
	Estimate bool         `json:"estimate"` // true when prices come from catalog (always true today)
	Known    bool         `json:"model_known"`
}

type tokenSummary struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CachedRead int64 `json:"cached_read"`
	CacheWrite int64 `json:"cache_write"`
}

// latestSessionID returns the lexicographically largest entry name under
// sessionsDir. ULIDs sort lexicographically by creation time, so this is
// the most recent session.
func latestSessionID(sessionsDir string) (string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", cliutil.New(
				"no sessions found",
				`run "gil new" or "gil interview" to start a session`)
		}
		return "", fmt.Errorf("read sessions dir: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return "", cliutil.New(
			"no sessions found",
			`run "gil new" or "gil interview" to start a session`)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids[0], nil
}

// buildSessionCost loads the session's events.jsonl, aggregates token
// counters, and computes a USD estimate. Returns a friendly error when
// the events file is missing.
func buildSessionCost(layout paths.Layout, sessionID string) (sessionCostReport, error) {
	if sessionID == "" {
		return sessionCostReport{}, cliutil.New(
			"session id is required",
			`pass a session id positionally or omit it to use the latest`)
	}
	eventsPath := filepath.Join(layout.SessionsDir(), sessionID, "events", "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		if os.IsNotExist(err) {
			return sessionCostReport{}, cliutil.New(
				fmt.Sprintf("no events recorded for session %s", sessionID),
				`the session may not have started a run yet — try "gil run" first`)
		}
		return sessionCostReport{}, fmt.Errorf("stat events: %w", err)
	}
	events, err := event.LoadAll(eventsPath)
	if err != nil {
		return sessionCostReport{}, fmt.Errorf("load events: %w", err)
	}

	totals, model := aggregateUsage(events)

	cat, err := cost.LoadCatalog(layout.ModelCatalog())
	if err != nil {
		return sessionCostReport{}, fmt.Errorf("load catalog: %w", err)
	}
	calc := &cost.Calculator{Catalog: cat}
	usd, ok := calc.Estimate(model, cost.Usage{
		InputTokens:      totals.Input,
		OutputTokens:     totals.Output,
		CachedReadTokens: totals.CachedRead,
		CacheWriteTokens: totals.CacheWrite,
	})

	return sessionCostReport{
		Session:  sessionID,
		Provider: providerForModel(model),
		Model:    model,
		Tokens:   totals,
		CostUSD:  usd,
		Estimate: true,
		Known:    ok,
	}, nil
}

// aggregateUsage walks an event stream summing the token counters that
// gil's runner emits today. The model is taken from the first
// `provider_request` event (it can change mid-run via stuck recovery, but
// for cost display we report the originally-selected model — same as
// aider's cmd_tokens uses self.coder.main_model).
//
// Token data comes from `provider_response` events whose Data JSON has
// `input_tokens` and `output_tokens` numeric fields. Cached-read /
// cache-write fields are read if the runner ever starts emitting them
// (forward-compatible) but default to 0 today.
func aggregateUsage(events []event.Event) (tokenSummary, string) {
	var sum tokenSummary
	model := ""
	for _, e := range events {
		if e.Type == "provider_request" && model == "" {
			var data struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(e.Data, &data)
			if data.Model != "" {
				model = data.Model
			}
		}
		if e.Type == "provider_response" {
			var data struct {
				Input      int64 `json:"input_tokens"`
				Output     int64 `json:"output_tokens"`
				CachedRead int64 `json:"cached_read_tokens"`
				CacheWrite int64 `json:"cache_write_tokens"`
			}
			if err := json.Unmarshal(e.Data, &data); err == nil {
				sum.Input += data.Input
				sum.Output += data.Output
				sum.CachedRead += data.CachedRead
				sum.CacheWrite += data.CacheWrite
			}
		}
	}
	return sum, model
}

// providerForModel maps a model name to a best-effort provider label. We
// don't carry the provider name through events today, so we infer it from
// the model prefix. Unknown models return "unknown".
func providerForModel(model string) string {
	switch {
	case model == "":
		return ""
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"):
		return "openai"
	case strings.HasPrefix(model, "mock"):
		return "mock"
	default:
		return "unknown"
	}
}

func writeCostText(w io.Writer, r sessionCostReport) error {
	fmt.Fprintf(w, "Session: %s\n", r.Session)
	fmt.Fprintf(w, "Provider: %s\n", r.Provider)
	fmt.Fprintf(w, "Model:    %s\n\n", r.Model)
	fmt.Fprintln(w, "Tokens:")
	fmt.Fprintf(w, "  input         %s\n", formatThousands(r.Tokens.Input))
	fmt.Fprintf(w, "  output        %s\n", formatThousands(r.Tokens.Output))
	if r.Tokens.CachedRead > 0 {
		fmt.Fprintf(w, "  cached read   %s\n", formatThousands(r.Tokens.CachedRead))
	}
	if r.Tokens.CacheWrite > 0 {
		fmt.Fprintf(w, "  cache write   %s\n", formatThousands(r.Tokens.CacheWrite))
	}
	fmt.Fprintln(w)
	if r.Known {
		fmt.Fprintf(w, "Cost (USD):    $%.4f  (estimate; public list prices)\n", r.CostUSD)
	} else if r.Model == "" {
		fmt.Fprintln(w, "Cost (USD):    n/a  (no provider response recorded)")
	} else {
		fmt.Fprintf(w, "Cost (USD):    n/a  (model %q not in catalog; override Cache/models.json)\n", r.Model)
	}
	return nil
}

func writeCostJSON(w io.Writer, r sessionCostReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// formatThousands formats n with comma separators (US style). Used by
// both cost and stats. Negative numbers are not expected in token counts
// so we keep the implementation minimal.
func formatThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}
