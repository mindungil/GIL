package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
)

func TestMetrics_DefaultRegistryServesMetrics(t *testing.T) {
	SetVersion("0.9.0-test")
	RunIterationsTotal.Inc()
	CompactDoneTotal.Inc()
	StuckDetectedTotal.WithLabelValues("RepeatedActionObservation").Inc()
	ToolCallsTotal.WithLabelValues("bash", "ok").Inc()
	SessionsRunning.Set(3)

	srv := httptest.NewServer(promhttp.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	for _, want := range []string{
		"gil_run_iterations_total",
		"gil_compact_done_total",
		"gil_stuck_detected_total",
		"gil_tool_calls_total",
		"gil_sessions_running",
		"gil_build_info",
		`version="0.9.0-test"`,
	} {
		require.Contains(t, s, want, "metric/label %q missing", want)
	}
}
