package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/runtime/cloud"
)

// --- interface conformance --------------------------------------------------

func TestProvider_ImplementsCloudProvider(t *testing.T) {
	var _ cloud.Provider = (*Provider)(nil)
}

func TestWrapper_ImplementsCloudWrapper(t *testing.T) {
	var _ cloud.CommandWrapper = (*Wrapper)(nil)
}

// Wrapper must also implement cloud.RemoteExecutor (which is the
// structural twin of core/tool.RemoteExecutor) — that's the whole point
// of this driver. A compile-time assertion catches accidental drift.
func TestWrapper_ImplementsRemoteExecutor(t *testing.T) {
	var _ cloud.RemoteExecutor = (*Wrapper)(nil)
}

// --- Available --------------------------------------------------------------

func TestProvider_Available_RequiresAPIKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	require.False(t, New().Available())
	t.Setenv(EnvAPIKey, "test-key")
	require.True(t, New().Available())
}

func TestProvider_Available_ExplicitKeyOverridesEnv(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	p := &Provider{APIKey: "explicit"}
	require.True(t, p.Available())
}

// --- Provision: error paths -------------------------------------------------

func TestProvider_Provision_NotConfigured(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	_, err := New().Provision(context.Background(), cloud.ProvisionOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, cloud.ErrNotConfigured))
}

func TestProvider_Provision_CreateFailure_ReturnsErr(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createStatus: http.StatusBadRequest,
		createBody:   `{"error":"image required"}`,
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "test-key")
	t.Setenv(EnvAPIBase, srv.URL)
	_, err := New().Provision(context.Background(), cloud.ProvisionOptions{
		SessionID: "abc",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "create workspace")
}

// --- Provision: happy path with poll ---------------------------------------

func TestProvider_Provision_PollsThenReady(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createResp:    Workspace{ID: "ws-7", Status: "creating"},
		readyAfterGet: 2,
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "test-key")
	t.Setenv(EnvAPIBase, srv.URL)
	p := &Provider{PollInterval: 5 * time.Millisecond}

	sb, err := p.Provision(context.Background(), cloud.ProvisionOptions{
		Image:     "python:3.12-slim",
		SessionID: "sess-1",
	})
	require.NoError(t, err)
	require.NotNil(t, sb)

	require.Equal(t, "daytona", sb.Info["provider"])
	require.Equal(t, "gil-sess-1", sb.Info["workspace"])
	require.Equal(t, "ws-7", sb.Info["workspace_id"])
	require.Equal(t, "python:3.12-slim", sb.Info["image"])
	require.Equal(t, "ready", sb.Info["status"])
	require.Equal(t, srv.URL, sb.Info["api_base"])
}

func TestProvider_Provision_AcceptsImmediateReady(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createResp: Workspace{ID: "ws-immediate", Status: "ready"},
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvAPIBase, srv.URL)
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{
		Image:     "alpine",
		SessionID: "x",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&srv.getCalls), "no polling needed when create returns ready")
	require.Equal(t, "ready", sb.Info["status"])
}

// --- Wrapper.ExecRemote round-trip -----------------------------------------

func TestWrapper_ExecRemote_RoundTrip(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createResp: Workspace{ID: "ws-rt", Status: "ready"},
		execResp:   ExecResult{Stdout: "OK\n", Stderr: "", Exit: 0},
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvAPIBase, srv.URL)
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{
		Image:     "alpine",
		SessionID: "rt",
	})
	require.NoError(t, err)

	wrapper, ok := sb.Wrapper.(*Wrapper)
	require.True(t, ok)
	stdout, stderr, exit, err := wrapper.ExecRemote(context.Background(), "bash", []string{"-c", "echo OK"})
	require.NoError(t, err)
	require.Equal(t, "OK\n", stdout)
	require.Equal(t, "", stderr)
	require.Equal(t, 0, exit)
}

func TestWrapper_ExecRemote_NonZeroExit(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createResp: Workspace{ID: "ws-fail", Status: "ready"},
		execResp:   ExecResult{Stdout: "", Stderr: "boom", Exit: 7},
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvAPIBase, srv.URL)
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{SessionID: "f"})
	require.NoError(t, err)
	wrapper := sb.Wrapper.(*Wrapper)
	stdout, stderr, exit, err := wrapper.ExecRemote(context.Background(), "false", nil)
	require.NoError(t, err) // HTTP succeeded
	require.Equal(t, "", stdout)
	require.Equal(t, "boom", stderr)
	require.Equal(t, 7, exit)
}

// --- Wrap (legacy / observability) -----------------------------------------

func TestWrapper_Wrap_DocumentaryArgvShape(t *testing.T) {
	w := &Wrapper{WorkspaceName: "gil-x"}
	out := w.Wrap("ls", "-la")
	require.Equal(t, []string{"daytona", "exec", "gil-x", "--", "ls", "-la"}, out)
}

// --- Teardown ---------------------------------------------------------------

func TestProvider_Teardown_CallsDelete(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createResp: Workspace{ID: "ws-td", Status: "ready"},
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvAPIBase, srv.URL)
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{SessionID: "td"})
	require.NoError(t, err)

	require.NoError(t, sb.Teardown(context.Background()))
	require.Equal(t, int32(1), atomic.LoadInt32(&srv.deleteCalls))
	require.Equal(t, "/workspaces/ws-td", srv.lastDeletePath.Load().(string))
}

func TestProvider_Teardown_404NotAnError(t *testing.T) {
	srv := newFakeAPI(t, fakeAPIOpts{
		createResp:    Workspace{ID: "ws-gone", Status: "ready"},
		deleteHandler: http.NotFound,
	})
	defer srv.Close()

	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvAPIBase, srv.URL)
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{SessionID: "gone"})
	require.NoError(t, err)
	require.NoError(t, sb.Teardown(context.Background()))
}

// --- end-to-end driver lifecycle (full Provision → Exec → Teardown) --------

func TestProvider_FullLifecycle(t *testing.T) {
	var (
		createCalls int32
		execCalls   int32
		deleteCalls int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/workspaces":
			atomic.AddInt32(&createCalls, 1)
			require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(Workspace{ID: "ws-life", Status: "ready"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			atomic.AddInt32(&execCalls, 1)
			var req ExecRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "bash", req.Cmd)
			require.Equal(t, []string{"-c", "uname -s"}, req.Args)
			_ = json.NewEncoder(w).Encode(ExecResult{Stdout: "Linux\n", Exit: 0})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/workspaces/"):
			atomic.AddInt32(&deleteCalls, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv(EnvAPIKey, "test-key")
	t.Setenv(EnvAPIBase, srv.URL)

	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{
		SessionID: "life",
		Image:     "alpine:latest",
	})
	require.NoError(t, err)

	stdout, _, exit, err := sb.Wrapper.(*Wrapper).ExecRemote(
		context.Background(), "bash", []string{"-c", "uname -s"},
	)
	require.NoError(t, err)
	require.Equal(t, "Linux\n", stdout)
	require.Equal(t, 0, exit)

	require.NoError(t, sb.Teardown(context.Background()))

	require.Equal(t, int32(1), atomic.LoadInt32(&createCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&execCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&deleteCalls))
}

// --- helpers ---------------------------------------------------------------

// fakeAPIOpts configures the fake Daytona REST server. Any field left as
// the zero value uses a sensible default (200 with createResp, 200 with
// execResp, 204 on delete, GET returns "creating" until readyAfterGet
// calls then "ready").
type fakeAPIOpts struct {
	createStatus  int
	createBody    string
	createResp    Workspace
	readyAfterGet int32 // # of GETs that should still be "creating" before flipping to "ready"
	execResp      ExecResult
	deleteHandler http.HandlerFunc // override for non-204 delete behavior
}

type fakeAPI struct {
	*httptest.Server
	getCalls       int32
	deleteCalls    int32
	lastDeletePath atomic.Value // string
}

func newFakeAPI(t *testing.T, opts fakeAPIOpts) *fakeAPI {
	t.Helper()
	api := &fakeAPI{}
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if opts.createStatus != 0 && opts.createStatus != http.StatusOK {
			w.WriteHeader(opts.createStatus)
			_, _ = w.Write([]byte(opts.createBody))
			return
		}
		_ = json.NewEncoder(w).Encode(opts.createResp)
	})
	mux.HandleFunc("/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/workspaces/")
		switch {
		case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(opts.execResp)
		case r.Method == http.MethodGet:
			n := atomic.AddInt32(&api.getCalls, 1)
			status := "creating"
			if n > opts.readyAfterGet {
				status = "ready"
			}
			id := strings.TrimSuffix(path, "/")
			_ = json.NewEncoder(w).Encode(Workspace{ID: id, Status: status})
		case r.Method == http.MethodDelete:
			atomic.AddInt32(&api.deleteCalls, 1)
			api.lastDeletePath.Store(r.URL.Path)
			if opts.deleteHandler != nil {
				opts.deleteHandler(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	api.Server = httptest.NewServer(mux)
	return api
}
