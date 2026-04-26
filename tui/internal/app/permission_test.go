package app

import (
	"testing"

	"github.com/stretchr/testify/require"
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
