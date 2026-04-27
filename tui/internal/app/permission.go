package app

import (
	"context"
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/sdk"
)

// pendingAskMsg is dispatched when an event of type "permission_ask" arrives.
// The TUI Update flips into a modal asking the user to choose one of the
// six allow/deny x once/session/always tiers (or Esc/q to deny once).
type pendingAskMsg struct {
	SessionID string
	RequestID string
	Tool      string
	Key       string
}

// askAnswerMsg is the result of the user's choice; the SDK call already
// happened in the answerCmd goroutine.
type askAnswerMsg struct {
	delivered bool
	err       string
}

// answerCmd fires the AnswerPermission RPC in a goroutine and returns a
// Cmd that delivers the result as an askAnswerMsg. The decision encodes
// both the allow/deny outcome AND the persistence intent so the server
// can record the rule in the appropriate store before unblocking the
// runner.
func answerCmd(client *sdk.Client, sessionID, requestID string, decision gilv1.PermissionDecision) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		delivered, err := client.AnswerPermissionDecision(ctx, sessionID, requestID, decision)
		if err != nil {
			return askAnswerMsg{err: err.Error()}
		}
		return askAnswerMsg{delivered: delivered}
	}
}

// permissionKeyToDecision maps a single TUI keystroke to the wire enum.
// The mapping is a deliberate compromise between codex's 3-tier
// (once/session/always) and cline's symmetric allow/deny lists:
//
//	a → ALLOW_ONCE     (lowercase a = "this time only")
//	s → ALLOW_SESSION  (s for "session")
//	A → ALLOW_ALWAYS   (capital = "stronger commitment")
//	d → DENY_ONCE
//	D → DENY_ALWAYS
//	esc/q → DENY_ONCE  (default-deny on dismissal)
//
// We intentionally do NOT bind a key for DENY_SESSION — denying for the
// rest of the session is rarely what users want when they could just
// permanently deny (capital D). Returning UNSPECIFIED signals "not a
// permission key, ignore" so the caller can keep the modal open.
func permissionKeyToDecision(k string) gilv1.PermissionDecision {
	switch k {
	case "a":
		return gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ONCE
	case "s":
		return gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_SESSION
	case "A":
		return gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ALWAYS
	case "d":
		return gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE
	case "D":
		return gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ALWAYS
	case "esc", "q", "Q":
		return gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE
	}
	return gilv1.PermissionDecision_PERMISSION_DECISION_UNSPECIFIED
}

// parsePermissionAsk inspects an event's type and data JSON for the
// permission_ask payload. Returns nil when the event isn't a permission_ask
// or when the payload is malformed / missing request_id.
func parsePermissionAsk(sessionID, eventType string, data []byte) *pendingAskMsg {
	if eventType != "permission_ask" {
		return nil
	}
	var d struct {
		RequestID string `json:"request_id"`
		Tool      string `json:"tool"`
		Key       string `json:"key"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil
	}
	if d.RequestID == "" {
		return nil
	}
	return &pendingAskMsg{
		SessionID: sessionID,
		RequestID: d.RequestID,
		Tool:      d.Tool,
		Key:       d.Key,
	}
}
