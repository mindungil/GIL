// Package metrics defines Prometheus metrics for the gil server.
// Metrics are registered with the default registry and exposed via
// promhttp.Handler() (gild --metrics :PORT).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RunIterationsTotal is incremented on each iteration_start event.
	RunIterationsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gil_run_iterations_total",
		Help: "Total number of agent loop iterations across all sessions.",
	})

	// CompactDoneTotal is incremented on each compact_done event.
	CompactDoneTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gil_compact_done_total",
		Help: "Total number of context compactions performed.",
	})

	// StuckDetectedTotal is incremented on each stuck_detected event,
	// labeled by pattern (e.g., "RepeatedActionObservation").
	StuckDetectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gil_stuck_detected_total",
		Help: "Total number of stuck patterns detected, by pattern.",
	}, []string{"pattern"})

	// ToolCallsTotal is incremented on each tool_result event, labeled
	// by tool name and result ("ok" or "error").
	ToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gil_tool_calls_total",
		Help: "Total number of tool calls, by tool name and outcome.",
	}, []string{"tool", "result"})

	// SessionsRunning is the current count of sessions in RUNNING state.
	SessionsRunning = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gil_sessions_running",
		Help: "Number of sessions currently in the RUNNING state.",
	})

	// BuildInfo is a static metric carrying version label.
	BuildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gil_build_info",
		Help: "Static build info; value is always 1.",
	}, []string{"version"})
)

// SetVersion sets the gil_build_info{version} metric. Call once at startup.
func SetVersion(v string) {
	BuildInfo.WithLabelValues(v).Set(1)
}
