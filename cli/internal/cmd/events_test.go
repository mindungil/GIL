package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mindungil/gil/core/cliutil"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
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

func TestTailEventsJSON_NDJSONLines(t *testing.T) {
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
			Timestamp: timestamppb.New(tm.Add(time.Second)),
			Source:    gilv1.EventSource_ENVIRONMENT,
			Kind:      gilv1.EventKind_OBSERVATION,
			Type:      "tool_result",
			DataJson:  []byte(`{"exit_code":0}`),
		},
		{
			// Missing/invalid data should fall back to {} so the
			// envelope's "data" key always parses.
			Source: gilv1.EventSource_SYSTEM,
			Kind:   gilv1.EventKind_NOTE,
			Type:   "heartbeat",
		},
	}
	mock := &mockTailClient{events: events}
	var buf bytes.Buffer
	require.NoError(t, tailEventsJSON(context.Background(), mock, &buf))

	// One JSON object per line (NDJSON). Parse each and verify keys.
	scanner := bufio.NewScanner(strings.NewReader(buf.String()))
	count := 0
	for scanner.Scan() {
		var envelope struct {
			Timestamp time.Time       `json:"timestamp"`
			Source    string          `json:"source"`
			Kind      string          `json:"kind"`
			Type      string          `json:"type"`
			Data      json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &envelope), "line %d not JSON: %s", count, scanner.Text())
		require.NotEmpty(t, envelope.Source)
		require.NotEmpty(t, envelope.Kind)
		require.NotEmpty(t, envelope.Type)
		require.True(t, json.Valid(envelope.Data), "data should be valid JSON")
		count++
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, 3, count, "expected one NDJSON line per event")
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
