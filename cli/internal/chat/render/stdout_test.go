package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newStdoutForTest(t *testing.T) (*StdoutChatRenderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	// Default helper renders unicode glyphs so the canonical strip
	// shape ("· " separators, "✓"/"✗" check marks) is what tests pin.
	// ASCII-mode collapse is exercised by dedicated tests below.
	r := NewStdoutChatRenderer(&buf, nil, false /*ascii*/, true /*noColor*/)
	return r, &buf
}

func newStdoutAsciiForTest(t *testing.T) (*StdoutChatRenderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r := NewStdoutChatRenderer(&buf, nil, true /*ascii*/, true /*noColor*/)
	return r, &buf
}

func TestStdout_Banner_PrintsName(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.Banner(SessionState{DisplayName: "add-dark-mode-0428", Phase: PhaseIdle})
	require.Contains(t, buf.String(), "add-dark-mode-0428")
}

func TestStdout_PromptCue_EmitsArrow(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.PromptCue()
	require.Equal(t, "> ", buf.String())
}

func TestStdout_AssistantText_AppendsAsIs(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.AssistantText("hello ")
	r.AssistantText("world")
	require.Equal(t, "hello world", buf.String())
}

// P33: AssistantReasoning prefixes every non-empty line with "[think]"
// so the user can scan the transcript and tell reasoning apart from
// the final answer. Multi-line input gets the prefix per line; empty
// chunks are no-ops.
func TestStdout_AssistantReasoning_PrefixesEveryLine(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.AssistantReasoning("first thought\nsecond thought")
	got := buf.String()
	require.Contains(t, got, "[think]")
	require.Contains(t, got, "first thought")
	require.Contains(t, got, "second thought")
	// Both lines must carry the prefix — count occurrences.
	require.Equal(t, 2, strings.Count(got, "[think]"),
		"expected [think] on both reasoning lines; got %q", got)
}

func TestStdout_AssistantReasoning_EmptyIsNoop(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.AssistantReasoning("")
	require.Empty(t, buf.String())
}

func TestStdout_StatusStrip_Idle(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{Phase: PhaseIdle})
	require.Equal(t, "[idle · type a prompt to start a new session]\n", buf.String())
}

// P49: idle strip with non-zero spend surfaces tokens + cost so the
// user sees accumulated cost between turns. Zero values fall back to
// the default idle text (TestStdout_StatusStrip_Idle above pins that).
func TestStdout_StatusStrip_Idle_WithSpend(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase:   PhaseIdle,
		Tokens:  4231,
		CostUSD: 0.0123,
	})
	require.Equal(t, "[idle · 4.2k · $0.0123 · type a prompt to continue]\n", buf.String())
}

// iter211: PhaseInterview / PhaseAwaitingConfirm removed (interview
// engine deleted in M3). The strip no longer has dedicated phases for
// interview / awaiting-confirm; tests for those were dropped with the
// producers.

func TestStdout_StatusStrip_Run(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseRun, Iter: 23, MaxIter: 100,
		CostUSD: 0.61, Autonomy: "ASK_DESTRUCTIVE",
	})
	require.Equal(t, "[run · iter 23/100 · $0.61 · ASK_DESTRUCTIVE]\n", buf.String())
}

func TestStdout_StatusStrip_Run_WithTokensAndLatency(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseRun, Iter: 4, MaxIter: 50,
		CostUSD:   0.18,
		Tokens:    4231,
		LatencyMs: 1240,
		Autonomy:  "FULL_AUTO",
	})
	// Tokens compact to "4.2k", latency 1240ms collapses to "1.2s".
	// Order must be: iter · cost · toks · latency · autonomy.
	require.Equal(t, "[run · iter 4/50 · $0.18 · 4.2k toks · 1.2s · FULL_AUTO]\n", buf.String())
}

func TestStdout_StatusStrip_Run_NoTokensOmitsCell(t *testing.T) {
	r, buf := newStdoutForTest(t)
	// Run with no token/latency reports yet — strip should NOT show
	// "0 toks · 0ms" placeholders, fall back to original 4-cell layout.
	r.StatusStrip(SessionState{
		Phase: PhaseRun, Iter: 1, MaxIter: 100,
		CostUSD: 0.00, Autonomy: "ASK_DESTRUCTIVE",
	})
	require.Equal(t, "[run · iter 1/100 · $0.00 · ASK_DESTRUCTIVE]\n", buf.String())
}

func TestStdout_StatusStrip_Stuck(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{Phase: PhaseStuck, Iter: 45, MaxIter: 100})
	require.Equal(t, "[run · iter 45/100 · STUCK after recovery]\n", buf.String())
}

func TestStdout_StatusStrip_Done(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseDone, Iter: 87, CostUSD: 2.34,
		ChecksPassed: 4, ChecksTotal: 4,
	})
	// Unicode mode: ✓ for pass, ✗ for fail.
	// iter211: the trailing "/diff /merge" hint was dropped — the chat
	// surface has no slash commands, so dangling /diff /merge in a
	// natural-language UI was a goal-fit miscue. Users phrase their
	// next step naturally ("show me the diff", "merge it").
	require.Equal(t, "[done · 87 iters · $2.34 · ✓ 4/4 checks]\n", buf.String())
}

func TestStdout_StatusStrip_AsciiCollapsesMiddleDot(t *testing.T) {
	// --ascii mode must substitute ` · ` for ` | ` in the strip body
	// so terminals without UTF-8 don't render the middle-dot as
	// mojibake. Done-strip's check mark also flips ✓/✗ → OK/FAIL.
	r, buf := newStdoutAsciiForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseRun, Iter: 4, MaxIter: 50,
		CostUSD: 0.18, Autonomy: "FULL_AUTO",
	})
	require.Equal(t, "[run | iter 4/50 | $0.18 | FULL_AUTO]\n", buf.String())

	buf.Reset()
	r.StatusStrip(SessionState{
		Phase: PhaseDone, Iter: 87, CostUSD: 2.34,
		ChecksPassed: 4, ChecksTotal: 4,
	})
	require.Equal(t, "[done | 87 iters | $2.34 | OK 4/4 checks]\n", buf.String())
}

// iter211: TestStdout_StatusStrip_AdvPluralization removed — its
// only branch exercised the interview strip's adv-finding pluralization,
// which is gone with PhaseInterview.

// Done with failing checks should still render.
func TestStdout_StatusStrip_DoneWithFailures(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseDone, Iter: 50, CostUSD: 1.10,
		ChecksPassed: 2, ChecksTotal: 4,
	})
	require.Equal(t, "[done · 50 iters · $1.10 · ✗ 2/4 checks]\n", buf.String())
}

// Silence "imported and not used" if strings isn't referenced yet
var _ = strings.HasPrefix

func TestStdout_SystemNote_PrefixesByKind(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.SystemNote(NoteSpec, "success_criteria += dark mode toggle")
	require.Equal(t, "[spec] success_criteria += dark mode toggle\n", buf.String())

	buf.Reset()
	r.SystemNote(NoteAdversary, "1 medium finding — accessibility contrast not specified")
	require.Equal(t, "[adversary] 1 medium finding — accessibility contrast not specified\n", buf.String())

	buf.Reset()
	r.SystemNote(NoteQueued, "agent will see at iter 24")
	require.Equal(t, "[note] agent will see at iter 24\n", buf.String())
}

// iter118a-dod: SystemNote must strip control bytes so a hostile event
// field that slips into a system note cannot repaint the terminal.
func TestStdout_SystemNote_StripsControlBytes(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.SystemNote(NoteSystem, "ok\x1b[2J\x07\x7fbeep\nnewline")
	got := buf.String()
	require.NotContains(t, got, "\x1b")
	require.NotContains(t, got, "\x07")
	require.NotContains(t, got, "\x7f")
	// Stripping ESC neutralises the escape sequence even though the
	// trailing literal `[2J` chars remain (printable, harmless).
	require.Equal(t, "[system] ok[2Jbeepnewline\n", got)
}

func TestStdout_Confirm_DefaultYes(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("\n") // empty input → default
	r := NewStdoutChatRenderer(&out, in, true, true)
	ok, err := r.Confirm("Start the run?", true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, out.String(), "Start the run? [Y/n]")
}

func TestStdout_Confirm_DefaultNo_AcceptsY(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("y\n")
	r := NewStdoutChatRenderer(&out, in, true, true)
	ok, err := r.Confirm("Apply diff?", false)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, out.String(), "Apply diff? [y/N]")
}

func TestStdout_Diff_SummariesPaths(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.Diff([]DiffHunk{
		{Path: "src/web/Toggle.tsx", Added: 42, Removed: 0},
		{Path: "src/web/Settings.tsx", Added: 18, Removed: 4},
	})
	out := buf.String()
	require.Contains(t, out, "src/web/Toggle.tsx")
	require.Contains(t, out, "+42 -0")
	require.Contains(t, out, "src/web/Settings.tsx")
	require.Contains(t, out, "+18 -4")
}

func TestStdout_Spec_PrintsYAML(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.Spec(&SpecView{YAML: "goal:\n  one_liner: add dark mode\n"})
	require.Contains(t, buf.String(), "goal:")
	require.Contains(t, buf.String(), "add dark mode")
}
