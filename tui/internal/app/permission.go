package app

import (
	"context"
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jedutools/gil/sdk"
)

// pendingAskMsg is dispatched when an event of type "permission_ask" arrives.
// The TUI Update flips into a modal asking y/n.
type pendingAskMsg struct {
	SessionID string
	RequestID string
	Tool      string
	Key       string
}

// askAnswerMsg is the result of the user's y/n choice; the SDK call already
// happened in the answerCmd goroutine.
type askAnswerMsg struct {
	delivered bool
	err       string
}

// answerCmd fires the AnswerPermission RPC in a goroutine and returns a Cmd
// that delivers the result as an askAnswerMsg.
func answerCmd(client *sdk.Client, sessionID, requestID string, allow bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		delivered, err := client.AnswerPermission(ctx, sessionID, requestID, allow)
		if err != nil {
			return askAnswerMsg{err: err.Error()}
		}
		return askAnswerMsg{delivered: delivered}
	}
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
