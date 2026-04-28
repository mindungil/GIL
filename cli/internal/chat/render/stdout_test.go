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

// Silence "imported and not used" if strings isn't referenced yet
var _ = strings.HasPrefix
