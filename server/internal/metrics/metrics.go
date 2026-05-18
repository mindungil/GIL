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

	// OrphanRunsReapedTotal is incremented on each orphan reap (P36
	// startup sweep and P38 mid-session heartbeat sweep). Labeled by
	// reason: "daemon_restart" (P36) or "stale_heartbeat" (P38). With
	// these counters surfaced to Prometheus, operators can:
	//   - Track baseline restart-reap rate (should equal # of daemon
	//     bounces × mean active runs).
	//   - Track stale-heartbeat false-positive rate (suspiciously high
	//     count = threshold too tight; suspiciously zero = sweeper or
	//     heartbeat refresh broken).
	OrphanRunsReapedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gil_orphan_runs_reaped_total",
		Help: "Total number of orphan runs reaped (P36 startup or P38 sweeper), by reason.",
	}, []string{"reason"})

	// AutoResumeKickedTotal is incremented when P37's opt-in
	// auto-resume fires a Start goroutine after orphan reap. Lets
	// operators tell apart "users opting in" from "users staying
	// manual." Always paired with an OrphanRunsReapedTotal{reason=
	// "daemon_restart"} increment for the same session.
	AutoResumeKickedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gil_auto_resume_kicked_total",
		Help: "Total P37 auto-resume Start invocations from orphan reaping.",
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
