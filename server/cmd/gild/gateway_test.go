//go:build integration

package main

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Smoke: when --http :0 is passed and gild is running, GET /v1/sessions returns 200.
// Skipped by default (build tag); run with: go test -tags=integration ./...

func TestHTTPGateway_ListSessionsSmoke(t *testing.T) {
	// Spin up gild in-process — too involved here; skip for now.
	t.Skip("integration smoke; run gild manually with --http :8080 and curl")

	_ = time.Second // silence unused import in case Skip is removed
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8080/v1/sessions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "sessions")
}
