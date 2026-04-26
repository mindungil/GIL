package cmd

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jedutools/gil/core/cliutil"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// mockTailClient is a minimal in-memory implementation of
// gilv1.RunService_TailClient that returns queued events then io.EOF.
type mockTailClient struct {
	events []*gilv1.Event
	pos    int
}

func (m *mockTailClient) Recv() (*gilv1.Event, error) {
	if m.pos >= len(m.events) {
		return nil, io.EOF
	}
	e := m.events[m.pos]
	m.pos++
	return e, nil
}

func (m *mockTailClient) Header() (metadata.MD, error) { return nil, nil }
func (m *mockTailClient) Trailer() metadata.MD         { return nil }
func (m *mockTailClient) CloseSend() error             { return nil }
func (m *mockTailClient) Context() context.Context     { return context.Background() }
func (m *mockTailClient) SendMsg(interface{}) error    { return nil }
func (m *mockTailClient) RecvMsg(interface{}) error    { return nil }

func TestTailEvents_FormatsOutput(t *testing.T) {
	tm, err := time.Parse(time.RFC3339, "2026-04-26T10:15:23Z")
	require.NoError(t, err)

	events := []*gilv1.Event{
		{
			Timestamp: timestamppb.New(tm),
			Source:    gilv1.EventSource_AGENT,
			Kind:      gilv1.EventKind_ACTION,
			Type:      "provider_request",
			DataJson:  []byte(`{"iteration":1,"messages":3}`),
		},
		{
			Timestamp: timestamppb.New(tm),
			Source:    gilv1.EventSource_ENVIRONMENT,
			Kind:      gilv1.EventKind_OBSERVATION,
			Type:      "tool_result",
			DataJson:  []byte(`{"exit_code":0}`),
		},
	}

	mock := &mockTailClient{events: events}
	var buf bytes.Buffer
	err = tailEvents(context.Background(), mock, &buf)
	require.NoError(t, err)

	out := buf.String()
	// First event: AGENT source, ACTION kind, with data
	require.Contains(t, out, "AGENT")
	require.Contains(t, out, "ACTION")
	require.Contains(t, out, `{"iteration":1,"messages":3}`)

	// Second event: ENVIRONMENT source, OBSERVATION kind, with data
	require.Contains(t, out, "ENVIRONMENT")
	require.Contains(t, out, "OBSERVATION")
	require.Contains(t, out, `{"exit_code":0}`)

	// Timestamp appears for both lines
	require.Contains(t, out, "2026-04-26T10:15:23Z")
}

func TestTailEvents_EmptyDataJson_PrintsBraces(t *testing.T) {
	events := []*gilv1.Event{
		{
			Source:   gilv1.EventSource_SYSTEM,
			Kind:     gilv1.EventKind_NOTE,
			Type:     "heartbeat",
			DataJson: nil,
		},
	}
	mock := &mockTailClient{events: events}
	var buf bytes.Buffer
	err := tailEvents(context.Background(), mock, &buf)
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "{}")
	require.Contains(t, out, "SYSTEM")
	require.Contains(t, out, "NOTE")
	// nil timestamp prints as dash
	require.Contains(t, out, "- ")
}

func TestEvents_NoTailFlag_Errors(t *testing.T) {
	var out bytes.Buffer
	cmd := eventsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	var ue *cliutil.UserError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Hint, "--tail")
}
