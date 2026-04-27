package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

// watchCmd returns the `gil watch <id>` command.
//
// Watch is the "TUI-without-the-TUI" surface — a single-pane in-place
// monitor for environments where the full Bubbletea TUI is unsuitable
// (CI logs, bare ssh, screen-readers piping output). The frame redraws
// every 2s by clearing the screen and re-rendering, which keeps the
// implementation deterministic and avoids the alternate-screen-buffer
// dance of a full TUI.
//
// --once → render a single frame and exit (script friendly)
// --no-clear → skip the ANSI clear-screen, just append (pipe friendly,
//   eg `gil watch <id> --no-clear | tee log`)
func watchCmd() *cobra.Command {
	var (
		socket  string
		once    bool
		noClear bool
		every   time.Duration
	)
	c := &cobra.Command{
		Use:   "watch <session-id>",
		Short: "Live single-pane progress monitor for a session",
		Long: `Render a one-screen "mission-control" view of a session and refresh
in place every 2 seconds (configurable with --interval).

Designed for environments where the full TUI is awkward — bare ssh,
CI logs, screen-readers. For richer interaction use 'giltui'.

  --once       render one frame and exit (script friendly)
  --no-clear   do not clear the screen between frames (pipe friendly)
  --interval   refresh cadence (default 2s; ignored with --once)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer cancel()

			fetcher := &grpcWatchFetcher{cli: cli, sessionID: sessionID}

			r := &watchRenderer{
				out:     cmd.OutOrStdout(),
				glyphs:  uistyle.NewGlyphs(asciiMode),
				palette: uistyle.NewPalette(false),
				clear:   !noClear,
			}
			return runWatch(ctx, r, fetcher, every, once)
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().BoolVar(&once, "once", false, "render one frame then exit")
	c.Flags().BoolVar(&noClear, "no-clear", false, "do not clear the screen between frames")
	c.Flags().DurationVar(&every, "interval", 2*time.Second, "refresh interval (ignored with --once)")
	return c
}

// watchFetcher abstracts the data source so tests can drive the
// renderer with a fixed snapshot. Two methods because the live event
// list and the session metadata travel on different RPCs.
type watchFetcher interface {
	Snapshot(ctx context.Context) (*sdk.Session, []watchEvent, error)
}

// watchEvent is the renderable shape for one log line. The full proto
// envelope is reduced to four fields the activity panel cares about,
// keeping the renderer trivial to test.
type watchEvent struct {
	When time.Time
	Iter int32
	Type string
	Body string
}

// grpcWatchFetcher is the production fetcher. Each Snapshot does:
//   1. cli.GetSession(id) — for goal/iter/cost/stuck
//   2. cli.TailRun(id) for ~250ms — drain into a ring buffer
// The 250ms cap keeps the per-frame call snappy even on a high-traffic
// session; the ring buffer is reused across frames so events accumulate.
type grpcWatchFetcher struct {
	cli       *sdk.Client
	sessionID string
	mu        sync.Mutex
	ring      []watchEvent
}

func (f *grpcWatchFetcher) Snapshot(ctx context.Context) (*sdk.Session, []watchEvent, error) {
	sess, err := f.cli.GetSession(ctx, f.sessionID)
	if err != nil {
		return nil, nil, wrapRPCError(err)
	}
	// Drain a short window of new events. We cancel the stream by
	// timeout so this method always returns within ~300ms regardless
	// of stream state — important for the 2s render cadence.
	streamCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	stream, err := f.cli.TailRun(streamCtx, f.sessionID)
	if err == nil {
		f.drain(streamCtx, stream)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]watchEvent(nil), f.ring...)
	return sess, out, nil
}

func (f *grpcWatchFetcher) drain(ctx context.Context, stream gilv1.RunService_TailClient) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		evt, err := stream.Recv()
		if err != nil {
			return
		}
		var ts time.Time
		if t := evt.GetTimestamp(); t != nil {
			ts = t.AsTime()
		}
		f.appendEvent(watchEvent{
			When: ts,
			Type: evt.GetType(),
			Body: shortDataPreview(evt.GetDataJson()),
		})
	}
}

// appendEvent grows the ring buffer up to 32 slots (10x the visible
// 5-line activity panel) so the renderer never starves between frames.
func (f *grpcWatchFetcher) appendEvent(e watchEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const cap = 32
	f.ring = append(f.ring, e)
	if len(f.ring) > cap {
		f.ring = f.ring[len(f.ring)-cap:]
	}
}

// runWatch is the loop. Pulled out of RunE so the test can call it
// directly with a fake fetcher and time-out.
func runWatch(ctx context.Context, r *watchRenderer, f watchFetcher, every time.Duration, once bool) error {
	tick := time.NewTicker(every)
	defer tick.Stop()
	frame := 0
	for {
		sess, evts, err := f.Snapshot(ctx)
		if err != nil {
			return err
		}
		r.render(sess, evts, frame)
		frame++
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// watchRenderer owns the writer, palette, and clear-screen behaviour.
// Pulling it out of runWatch keeps the snapshot test simple — the
// test gives it a bytes.Buffer and asserts on the rendered string.
type watchRenderer struct {
	out          io.Writer
	glyphs       uistyle.Glyphs
	palette      uistyle.Palette
	clear        bool
	costStart    float64   // cost at first frame (for trend)
	costStartT   time.Time // when costStart was recorded
	lastCost     float64
	lastCostT    time.Time
}

// ANSI helpers — terminal-aesthetic.md §8 (motion).
const (
	ansiClearScreen = "\x1b[2J\x1b[H"
)

func (r *watchRenderer) render(sess *sdk.Session, evts []watchEvent, frame int) {
	if r.clear {
		fmt.Fprint(r.out, ansiClearScreen)
	}
	g, p := r.glyphs, r.palette

	// --- Header ----------------------------------------------------------
	statusGlyphStr, role := sessionStatusGlyph(g,sess.Status)
	header := fmt.Sprintf("%s   %s  %s   %s",
		p.Primary("G I L"),
		colourMarker(p, statusGlyphStr, role),
		p.Dim(shortID(sess.ID)),
		p.Surface(truncRune(sess.GoalHint, 60)))
	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, header)
	fmt.Fprintln(r.out)

	// --- Progress block --------------------------------------------------
	const maxIter int32 = 100
	bar := uistyle.BarFixed(g, int(sess.CurrentIteration), int(maxIter))
	spinner := ""
	if strings.EqualFold(sess.Status, "RUNNING") {
		spinner = "  " + p.Info(g.SpinFrames[frame%len(g.SpinFrames)])
	}
	fmt.Fprintf(r.out, "   %s   %s   %d / %d%s\n",
		p.Bold("Progress"), bar, sess.CurrentIteration, maxIter, spinner)

	// Verify check matrix — placeholder (server doesn't yet expose
	// per-check pass/fail in the Session proto). We render a dim
	// dash row so the layout is stable for when the data lands.
	verify := strings.Repeat(g.LightHRule+" ", 6)
	fmt.Fprintf(r.out, "   %s     %s  %s\n",
		p.Bold("Verify"), p.Dim(strings.TrimRight(verify, " ")), p.Dim("0 / 0"))

	// Cost — derive a trend across frames if we've seen >=2 samples.
	cost := 0.0
	costStr := fmt.Sprintf("$%0.2f", cost)
	trendStr := p.Dim(g.LightHRule)
	if r.lastCostT.IsZero() {
		r.costStart = cost
		r.costStartT = time.Now()
	}
	r.lastCost, r.lastCostT = cost, time.Now()
	fmt.Fprintf(r.out, "   %s       %s   %s\n",
		p.Bold("Cost"), p.Surface(costStr), trendStr)

	// Stuck row — dash unless the session status flagged it.
	stuckStr := p.Dim(g.LightHRule)
	if strings.EqualFold(sess.Status, "STUCK") {
		stuckStr = p.Caution(g.Warn + " STUCK")
	}
	fmt.Fprintf(r.out, "   %s      %s\n", p.Bold("Stuck"), stuckStr)

	fmt.Fprintln(r.out)

	// --- Activity tail (last 5, newest first) ----------------------------
	tail := evts
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	for i := len(tail) - 1; i >= 0; i-- {
		e := tail[i]
		when := uistyle.HHMMSS(e.When)
		line := fmt.Sprintf("%s %s  iter %d  %s  %s",
			p.Dim(g.QuoteBar), p.Dim(when), e.Iter, eventGlyph(g, p, e.Type), p.Surface(truncRune(e.Body, 60)))
		fmt.Fprintf(r.out, "   %s\n", line)
	}

	// Footer
	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "   %s\n", p.Meta("live · ctrl-c to exit"))
}

// eventGlyph picks the action/observation/result symbol per spec §3.
// Reused by the events filter renderer; centralising here keeps the
// two surfaces in sync.
func eventGlyph(g uistyle.Glyphs, p uistyle.Palette, kind string) string {
	switch {
	case strings.HasPrefix(kind, "tool_call"), strings.HasSuffix(kind, "_request"):
		return p.Info(g.ActionMark) + " " + p.Surface(kind)
	case strings.HasPrefix(kind, "tool_result"), strings.HasSuffix(kind, "_response"):
		return p.Dim(g.ObserveMrk) + " " + p.Surface(kind)
	case strings.Contains(kind, "stuck"):
		return p.Caution(g.Warn) + " " + p.Caution(kind)
	case strings.HasSuffix(kind, "_error"), strings.HasSuffix(kind, "_failed"):
		return p.Alert(g.Failed) + " " + p.Alert(kind)
	case strings.Contains(kind, "verify_result"), strings.Contains(kind, "_done"):
		return p.Success(g.Done) + " " + p.Surface(kind)
	default:
		return p.Dim(g.Bullet) + " " + p.Surface(kind)
	}
}

// shortDataPreview gives the renderer a one-glance summary of the
// data_json payload — keys' first values, comma-joined. We deliberately
// keep this dumb; the full inspection lives in `gil events --tail`.
func shortDataPreview(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	s := string(data)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		s = s[:79] + "…"
	}
	return s
}
