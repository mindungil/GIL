// client.go — Daytona REST API client.
//
// Daytona (https://daytona.io) exposes a REST API for managing cloud
// workspaces. gil drives it directly over HTTPS using net/http; there is
// no Go SDK and we deliberately avoid Python shell-outs (unlike Modal,
// which has no Go-native API).
//
// Endpoints we touch (subset; see https://www.daytona.io/docs/api/):
//
//	POST   /workspaces           — create   → {id, status}
//	GET    /workspaces/{id}      — status   → {id, status, ...}
//	POST   /workspaces/{id}/exec — run cmd  → {stdout, stderr, exit}
//	DELETE /workspaces/{id}      — teardown → 204
//
// The Client uses a single *http.Client (caller-supplied or a sane
// default) and authenticates with `Authorization: Bearer <api-key>`.
//
// Tests inject httptest.NewServer URLs via NewClient's baseURL parameter
// and the DAYTONA_API_BASE env var on the Provider; no test ever hits
// the real api.daytona.io.
package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the production Daytona REST endpoint. Tests and
// staging deployments override it via DAYTONA_API_BASE.
const DefaultBaseURL = "https://api.daytona.io"

// Client is a thin wrapper around net/http for the Daytona REST surface
// gil needs. It is safe for concurrent use; the underlying *http.Client
// must be configured by the caller for timeouts/retries.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewClient constructs a Client. baseURL trailing slash is stripped so
// callers don't need to think about it. httpClient may be nil → a default
// with a 60s overall timeout is used.
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP:    httpClient,
	}
}

// Workspace mirrors the relevant fields of Daytona's workspace resource.
// Daytona's real schema is richer; we only decode what the lifecycle needs.
type Workspace struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "creating" | "ready" | "stopped" | ...
}

// ExecRequest is the JSON body for POST /workspaces/{id}/exec.
type ExecRequest struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
	Cwd  string   `json:"cwd,omitempty"`
}

// ExecResult is the response from POST /workspaces/{id}/exec.
type ExecResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

// createWorkspaceRequest is the JSON body for POST /workspaces.
type createWorkspaceRequest struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// CreateWorkspace creates a new workspace and returns its initial state.
// Caller is expected to poll GetWorkspace until Status == "ready" before
// calling Exec.
func (c *Client) CreateWorkspace(ctx context.Context, name, image string) (*Workspace, error) {
	body, err := json.Marshal(createWorkspaceRequest{Name: name, Image: image})
	if err != nil {
		return nil, fmt.Errorf("daytona: marshal create body: %w", err)
	}
	var ws Workspace
	if err := c.do(ctx, http.MethodPost, "/workspaces", body, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

// GetWorkspace fetches the current state of a workspace by ID.
func (c *Client) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	var ws Workspace
	if err := c.do(ctx, http.MethodGet, "/workspaces/"+id, nil, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

// Exec runs `cmd` with `args` inside the workspace and returns the full
// result in one round-trip. cwd defaults to "/workspace" if empty.
func (c *Client) Exec(ctx context.Context, id, cmd string, args []string, cwd string) (*ExecResult, error) {
	if cwd == "" {
		cwd = "/workspace"
	}
	body, err := json.Marshal(ExecRequest{Cmd: cmd, Args: args, Cwd: cwd})
	if err != nil {
		return nil, fmt.Errorf("daytona: marshal exec body: %w", err)
	}
	var res ExecResult
	if err := c.do(ctx, http.MethodPost, "/workspaces/"+id+"/exec", body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Delete tears down a workspace. Idempotent at the REST layer (404 is OK
// since the workspace might already be gone).
func (c *Client) Delete(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/workspaces/"+id, nil, nil)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil // already gone — treat as success
	}
	return err
}

// WaitReady polls GetWorkspace at `interval` until Status == "ready" or
// the context expires. The first poll happens immediately.
func (c *Client) WaitReady(ctx context.Context, id string, interval time.Duration) (*Workspace, error) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	for {
		ws, err := c.GetWorkspace(ctx, id)
		if err != nil {
			return nil, err
		}
		if ws.Status == "ready" || ws.Status == "running" {
			return ws, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("daytona: workspace %s not ready: %w", id, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// APIError is returned for non-2xx HTTP responses. Wraps the status code
// and (best-effort) the response body so logs explain what went wrong.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("daytona: HTTP %d: %s", e.Status, e.Body)
}

// do performs an HTTP request with bearer auth and JSON Content-Type.
// If `out` is non-nil, the response body is decoded into it on 2xx.
// Non-2xx responses produce an *APIError carrying the body for diagnosis.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("daytona: build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("daytona: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("daytona: decode %s %s: %w", method, path, err)
	}
	return nil
}
