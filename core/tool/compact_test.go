package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeRequester struct{ called int }

func (f *fakeRequester) RequestCompact() { f.called++ }

func TestCompactNow_CallsRequester(t *testing.T) {
	fr := &fakeRequester{}
	c := &CompactNow{Requester: fr}
	res, err := c.Run(context.Background(), json.RawMessage(`{"reason":"long context"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, 1, fr.called)
	require.Contains(t, res.Content, "compaction requested")
}

func TestCompactNow_NilRequester_NoCrash(t *testing.T) {
	c := &CompactNow{}
	_, err := c.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestCompactNow_NameSchemaDescription(t *testing.T) {
	c := &CompactNow{}
	require.Equal(t, "compact_now", c.Name())
	require.NotEmpty(t, c.Description())
	require.NotEmpty(t, c.Schema())
}
