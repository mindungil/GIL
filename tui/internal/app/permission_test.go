package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func TestParsePermissionAsk_Valid(t *testing.T) {
	data := []byte(`{"request_id":"abc","tool":"bash","key":"rm -rf /"}`)
	p := parsePermissionAsk("sess1", "permission_ask", data)
	require.NotNil(t, p)
	require.Equal(t, "sess1", p.SessionID)
	require.Equal(t, "abc", p.RequestID)
	require.Equal(t, "bash", p.Tool)
	require.Equal(t, "rm -rf /", p.Key)
}

func TestParsePermissionAsk_WrongType_ReturnsNil(t *testing.T) {
	require.Nil(t, parsePermissionAsk("s", "tool_call", []byte(`{}`)))
}

func TestParsePermissionAsk_BadJSON_ReturnsNil(t *testing.T) {
	require.Nil(t, parsePermissionAsk("s", "permission_ask", []byte(`not json`)))
}

func TestParsePermissionAsk_MissingRequestID_ReturnsNil(t *testing.T) {
	require.Nil(t, parsePermissionAsk("s", "permission_ask", []byte(`{"tool":"x"}`)))
}

// TestPermissionKeyToDecision_AllSixTiers pins the key->decision mapping
// for every documented modal option. New keys added in the future should
// add a row here; removed keys should fail this test as a reminder to
// update the modal text in view.go in lockstep.
func TestPermissionKeyToDecision_AllSixTiers(t *testing.T) {
	cases := []struct {
		key  string
		want gilv1.PermissionDecision
	}{
		{"a", gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ONCE},
		{"s", gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_SESSION},
		{"A", gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ALWAYS},
		{"d", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE},
		{"D", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ALWAYS},
		{"esc", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE},
		{"q", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE},
		{"Q", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			require.Equal(t, tc.want, permissionKeyToDecision(tc.key))
		})
	}
}

// TestPermissionKeyToDecision_UnknownKey_ReturnsUnspecified verifies that
// non-permission keys leave the modal open (UNSPECIFIED is the sentinel
// the Update loop uses to decide "swallow this and keep waiting").
func TestPermissionKeyToDecision_UnknownKey_ReturnsUnspecified(t *testing.T) {
	for _, k := range []string{"x", "1", "enter", "tab", " ", ""} {
		require.Equal(t,
			gilv1.PermissionDecision_PERMISSION_DECISION_UNSPECIFIED,
			permissionKeyToDecision(k),
			"key %q should not dismiss the modal", k,
		)
	}
}

// TestOverlayModal_RendersAllSixOptions ensures the visible modal text
// shows every option a user can press. Phase 14 restyle moved the
// renderer to view.go's renderPermissionModal and switched labels to
// lowercase per the aesthetic spec §11.
func TestOverlayModal_RendersAllSixOptions(t *testing.T) {
	SetNoColor(true)
	defer SetNoColor(false)
	ask := &pendingAskMsg{
		SessionID: "s1", RequestID: "r1",
		Tool: "bash", Key: "rm -rf /tmp/x",
	}
	out := renderPermissionModal(ask, 120)
	for _, snippet := range []string{
		"agent wants to run",
		"bash",
		"rm -rf /tmp/x",
		"[a] allow once",
		"[s] allow session",
		"[A] allow always",
		"[d] deny once",
		"[D] deny always",
		"[esc] cancel",
	} {
		require.Truef(t, strings.Contains(out, snippet),
			"modal output is missing %q\nfull output:\n%s", snippet, out)
	}
}
