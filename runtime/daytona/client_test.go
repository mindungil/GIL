package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeServer wires a small mux around the four Daytona endpoints we use.
// Each handler may be nil; nil handlers respond 404 so accidentally-hit
// routes are visible in test failures.
type fakeServer struct {
	t              *testing.T
	createWorkspace http.HandlerFunc
	getWorkspace    http.HandlerFunc
	exec            http.HandlerFunc
	deleteWorkspace http.HandlerFunc
}

func (f *fakeServer) start() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && f.createWorkspace != nil {
			f.createWorkspace(w, r)
			return
		}
		http.NotFound(w, r)
	})
	// Subroutes: /workspaces/{id} (GET, DELETE) and /workspaces/{id}/exec (POST)
	mux.HandleFunc("/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/workspaces/")
		switch {
		case strings.HasSuffix(path, "/exec"):
			if r.Method == http.MethodPost && f.exec != nil {
				f.exec(w, r)
				return
			}
		case r.Method == http.MethodGet && f.getWorkspace != nil:
			f.getWorkspace(w, r)
			return
		case r.Method == http.MethodDelete && f.deleteWorkspace != nil:
			f.deleteWorkspace(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return httptest.NewServer(mux)
}

// --- CreateWorkspace --------------------------------------------------------

func TestClient_CreateWorkspace_SendsAuthAndBody(t *testing.T) {
	var (
		gotAuth        string
		gotContentType string
		gotMethod      string
		gotBody        createWorkspaceRequest
	)
	srv := (&fakeServer{
		t: t,
		createWorkspace: func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotContentType = r.Header.Get("Content-Type")
			gotMethod = r.Method
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Workspace{ID: "ws-123", Status: "creating"})
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "secret-key", srv.Client())
	ws, err := c.CreateWorkspace(context.Background(), "gil-abc", "alpine:latest")
	require.NoError(t, err)
	require.Equal(t, "ws-123", ws.ID)
	require.Equal(t, "creating", ws.Status)

	require.Equal(t, "Bearer secret-key", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "gil-abc", gotBody.Name)
	require.Equal(t, "alpine:latest", gotBody.Image)
}

func TestClient_CreateWorkspace_NonJSONErrorBubbles(t *testing.T) {
	srv := (&fakeServer{
		t: t,
		createWorkspace: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "image required")
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	_, err := c.CreateWorkspace(context.Background(), "x", "")
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusBadRequest, apiErr.Status)
	require.Contains(t, apiErr.Body, "image required")
}

// --- GetWorkspace + WaitReady -----------------------------------------------

func TestClient_WaitReady_PollsUntilReady(t *testing.T) {
	var calls int32
	srv := (&fakeServer{
		t: t,
		getWorkspace: func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&calls, 1)
			status := "creating"
			if n >= 3 {
				status = "ready"
			}
			_ = json.NewEncoder(w).Encode(Workspace{ID: "ws-1", Status: status})
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	ws, err := c.WaitReady(context.Background(), "ws-1", 5*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, "ready", ws.Status)
	require.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(3))
}

func TestClient_WaitReady_HonorsContextCancel(t *testing.T) {
	srv := (&fakeServer{
		t: t,
		getWorkspace: func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(Workspace{ID: "ws-1", Status: "creating"})
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.WaitReady(ctx, "ws-1", 10*time.Millisecond)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
}

// --- Exec -------------------------------------------------------------------

func TestClient_Exec_RoundTrip(t *testing.T) {
	var got ExecRequest
	srv := (&fakeServer{
		t: t,
		exec: func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
			require.Equal(t, "/workspaces/ws-1/exec", r.URL.Path)
			_ = json.NewEncoder(w).Encode(ExecResult{
				Stdout: "hello\n",
				Stderr: "",
				Exit:   0,
			})
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	res, err := c.Exec(context.Background(), "ws-1", "bash", []string{"-c", "echo hello"}, "/workspace")
	require.NoError(t, err)
	require.Equal(t, "hello\n", res.Stdout)
	require.Equal(t, 0, res.Exit)
	require.Equal(t, "bash", got.Cmd)
	require.Equal(t, []string{"-c", "echo hello"}, got.Args)
	require.Equal(t, "/workspace", got.Cwd)
}

func TestClient_Exec_DefaultsCwdWhenEmpty(t *testing.T) {
	var got ExecRequest
	srv := (&fakeServer{
		t: t,
		exec: func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
			_ = json.NewEncoder(w).Encode(ExecResult{Stdout: "", Stderr: "", Exit: 0})
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	_, err := c.Exec(context.Background(), "ws-1", "ls", nil, "")
	require.NoError(t, err)
	require.Equal(t, "/workspace", got.Cwd)
}

func TestClient_Exec_PropagatesNonZeroExit(t *testing.T) {
	srv := (&fakeServer{
		t: t,
		exec: func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ExecResult{Stdout: "", Stderr: "boom", Exit: 7})
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	res, err := c.Exec(context.Background(), "ws-1", "false", nil, "")
	require.NoError(t, err) // HTTP succeeded; the *command* failed
	require.Equal(t, 7, res.Exit)
	require.Equal(t, "boom", res.Stderr)
}

// --- Delete -----------------------------------------------------------------

func TestClient_Delete_204IsSuccess(t *testing.T) {
	var called bool
	srv := (&fakeServer{
		t: t,
		deleteWorkspace: func(w http.ResponseWriter, r *http.Request) {
			called = true
			require.Equal(t, "/workspaces/ws-1", r.URL.Path)
			require.Equal(t, http.MethodDelete, r.Method)
			w.WriteHeader(http.StatusNoContent)
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	require.NoError(t, c.Delete(context.Background(), "ws-1"))
	require.True(t, called)
}

func TestClient_Delete_404IsSuccess(t *testing.T) {
	srv := (&fakeServer{
		t: t,
		deleteWorkspace: func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	// 404 → already gone → no error.
	require.NoError(t, c.Delete(context.Background(), "ws-missing"))
}

func TestClient_Delete_500BubblesError(t *testing.T) {
	srv := (&fakeServer{
		t: t,
		deleteWorkspace: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "kaboom")
		},
	}).start()
	defer srv.Close()

	c := NewClient(srv.URL, "k", srv.Client())
	err := c.Delete(context.Background(), "ws-1")
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusInternalServerError, apiErr.Status)
}

// --- NewClient defaults -----------------------------------------------------

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("", "", nil)
	require.Equal(t, DefaultBaseURL, c.BaseURL)
	require.NotNil(t, c.HTTP)
	require.Equal(t, 60*time.Second, c.HTTP.Timeout)
}

func TestNewClient_StripsTrailingSlash(t *testing.T) {
	c := NewClient("https://example.com///", "k", nil)
	require.Equal(t, "https://example.com", c.BaseURL)
}
