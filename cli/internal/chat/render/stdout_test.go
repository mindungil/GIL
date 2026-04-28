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
	r := NewStdoutChatRenderer(&buf, nil, true /*ascii*/, true /*noColor*/)
	return r, &buf
}

func TestStdout_Banner_PrintsName(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.Banner(SessionState{DisplayName: "add-dark-mode-0428", Phase: PhaseInterview})
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

func TestStdout_StatusStrip_Idle(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{Phase: PhaseIdle})
	require.Equal(t, "[idle · type a prompt to start, or /sessions to resume]\n", buf.String())
}

func TestStdout_StatusStrip_Interview(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseInterview, SlotsFilled: 4, SlotsTotal: 11,
		Saturation: 0.36, AdvFindings: 1,
	})
	require.Equal(t, "[interview · 4/11 slots · sat 36% · 1 adv finding]\n", buf.String())
}

func TestStdout_StatusStrip_AwaitingConfirm(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{Phase: PhaseAwaitingConfirm})
	require.Equal(t, "[interview · ready to freeze · /run to start, prompt to keep iterating]\n", buf.String())
}

func TestStdout_StatusStrip_Run(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseRun, Iter: 23, MaxIter: 100,
		CostUSD: 0.61, Autonomy: "ASK_DESTRUCTIVE",
	})
	require.Equal(t, "[run · iter 23/100 · $0.61 · ASK_DESTRUCTIVE]\n", buf.String())
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
	// ASCII mode (newStdoutForTest passes ascii=true): ✓ → OK, ✗ → FAIL.
	require.Equal(t, "[done · 87 iters · $2.34 · OK 4/4 checks · /diff /merge]\n", buf.String())
}

// Pluralization: "0 adv finding" should not show, "2 adv findings".
func TestStdout_StatusStrip_AdvPluralization(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseInterview, SlotsFilled: 5, SlotsTotal: 11,
		Saturation: 0.45, AdvFindings: 0,
	})
	require.Equal(t, "[interview · 5/11 slots · sat 45%]\n", buf.String())

	buf.Reset()
	r.StatusStrip(SessionState{
		Phase: PhaseInterview, SlotsFilled: 5, SlotsTotal: 11,
		Saturation: 0.45, AdvFindings: 2,
	})
	require.Equal(t, "[interview · 5/11 slots · sat 45% · 2 adv findings]\n", buf.String())
}

// Done with failing checks should still render.
func TestStdout_StatusStrip_DoneWithFailures(t *testing.T) {
	r, buf := newStdoutForTest(t)
	r.StatusStrip(SessionState{
		Phase: PhaseDone, Iter: 50, CostUSD: 1.10,
		ChecksPassed: 2, ChecksTotal: 4,
	})
	require.Equal(t, "[done · 50 iters · $1.10 · FAIL 2/4 checks · /diff /merge]\n", buf.String())
}

// Silence "imported and not used" if strings isn't referenced yet
var _ = strings.HasPrefix
