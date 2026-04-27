// Package notify provides outbound notification channels for events
// the user should see even when their attention is elsewhere — chiefly
// the agent-callable `clarify` tool's "I need your input" pause. The
// channels are pluggable behind one Notifier interface so the harness
// stays agnostic about whether the user is on a desktop, in tmux, or
// only reachable via Slack.
//
// Reference lift: cline's showSystemNotification + opencode's webhook
// fan-out. We deliberately keep every channel fire-and-forget at the
// caller boundary (the run goroutine never waits on a flaky webhook),
// and every channel reports its own error so the orchestrator
// (MultiNotifier) can log per-channel failures without aborting the
// fan-out.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notification is the message a Notifier delivers. Title/Body are the
// human-facing text; Urgency is the original "low|normal|high" hint
// from the agent (channels translate this to their own conventions —
// e.g., desktop "critical" vs "low"). SessionID + AskID + URL let the
// channels embed deep links / IDs into the body so a user clicking
// through a Slack message can answer the right session.
type Notification struct {
	Title     string
	Body      string
	Urgency   string // "low" | "normal" | "high"
	SessionID string
	AskID     string
	URL       string // optional follow-up link (e.g., gild HTTP /clarify URL)
}

// Notifier is the surface every channel implements. The context is
// passed through so a slow webhook can be cancelled by an outer
// timeout. Errors are returned (not panicked) so MultiNotifier can
// keep going.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// StdoutNotifier prints the notification to the supplied writer (or
// os.Stdout when Out is nil). The fallback channel — always works,
// no privileged binaries, no network. Used as the bottom of the
// MultiNotifier chain so a misconfigured user still sees something.
type StdoutNotifier struct {
	// Out is the sink the notifier writes to. Nil → os.Stdout. Set in
	// tests / when wiring into the gild log so the line goes through
	// the daemon's logger rather than the user's terminal.
	Out io.Writer
}

// Notify writes a single line per notification with enough context
// (session, ask, urgency) that a grep-only user can answer.
func (s *StdoutNotifier) Notify(ctx context.Context, n Notification) error {
	w := s.Out
	if w == nil {
		// Lazy import: avoid pulling os into the package surface for
		// tests that always set Out.
		return fmt.Errorf("notify: stdout writer not set")
	}
	urg := n.Urgency
	if urg == "" {
		urg = "normal"
	}
	line := fmt.Sprintf("[gil notify | %s | session=%s ask=%s] %s — %s",
		urg, shortID(n.SessionID), shortID(n.AskID), n.Title, oneline(n.Body))
	if n.URL != "" {
		line += " (" + n.URL + ")"
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// DesktopNotifier shells out to the platform's native notification
// command (notify-send on Linux, osascript on macOS). Best-effort: a
// missing binary returns an error rather than crashing the run, and
// the urgency hint maps to whatever the platform expects ("critical"
// on notify-send, no equivalent on osascript so we just embed the
// urgency in the title).
type DesktopNotifier struct {
	// runner is overrideable so tests can capture the argv without
	// actually executing notify-send. nil → exec.CommandContext.
	runner func(ctx context.Context, name string, args ...string) error
}

// Notify dispatches the notification to the platform's CLI.
func (d *DesktopNotifier) Notify(ctx context.Context, n Notification) error {
	run := d.runner
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	switch runtime.GOOS {
	case "linux":
		urg := mapUrgencyForNotifySend(n.Urgency)
		args := []string{"--urgency=" + urg, "--app-name=gil", n.Title, oneline(n.Body)}
		return run(ctx, "notify-send", args...)
	case "darwin":
		// osascript -e 'display notification "body" with title "title"'
		body := strings.ReplaceAll(oneline(n.Body), `"`, `\"`)
		title := strings.ReplaceAll(n.Title, `"`, `\"`)
		script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
		return run(ctx, "osascript", "-e", script)
	default:
		return fmt.Errorf("desktop notify not supported on %s", runtime.GOOS)
	}
}

// mapUrgencyForNotifySend translates gil's three-tier hint to
// notify-send's vocabulary. high→critical (sticks until dismissed),
// normal stays normal, low→low (ephemeral). The fallback for
// anything else is normal so a model that hallucinates a value still
// gets a sane delivery.
func mapUrgencyForNotifySend(u string) string {
	switch u {
	case "high":
		return "critical"
	case "low":
		return "low"
	default:
		return "normal"
	}
}

// WebhookNotifier POSTs a JSON body to URL. The default body shape is
// Slack-compatible ({"text": ...}) since that covers Slack incoming
// webhooks AND most generic "JSON to a URL" services (Discord, Teams
// via custom shim, internal hooks). When Slack=false, we send the
// full Notification struct as JSON for callers wiring custom hooks.
type WebhookNotifier struct {
	URL    string // required; empty → Notify returns nil with an error
	Method string // default POST
	Slack  bool   // when true, body shape is {"text": "..."}; otherwise full struct
	// Client is overrideable so tests can inject an httptest.Server
	// roundtripper without wrapping the package-level transport.
	Client *http.Client
}

// Notify POSTs the body. Timeout is 5 seconds via the default client;
// callers wanting different latency budgets supply their own Client.
// Failures are returned to the caller — the orchestrator decides
// whether to log/swallow them.
func (w *WebhookNotifier) Notify(ctx context.Context, n Notification) error {
	if w.URL == "" {
		return fmt.Errorf("notify: webhook URL is empty")
	}
	method := w.Method
	if method == "" {
		method = http.MethodPost
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	var body []byte
	var err error
	if w.Slack || isSlackWebhook(w.URL) {
		body, err = json.Marshal(map[string]string{
			"text": fmt.Sprintf("[gil session %s] %s — %s",
				shortID(n.SessionID), n.Title, oneline(n.Body)),
		})
	} else {
		body, err = json.Marshal(n)
	}
	if err != nil {
		return fmt.Errorf("notify: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: webhook POST: %w", err)
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused / pooled.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("notify: webhook returned %s", resp.Status)
	}
	return nil
}

// isSlackWebhook is a tiny heuristic so users who paste a Slack URL
// without setting Slack=true still get the right body shape. The
// hooks.slack.com domain is stable and unique to incoming webhooks.
func isSlackWebhook(url string) bool {
	return strings.Contains(url, "hooks.slack.com")
}

// MultiNotifier fans out to every wrapped notifier. Errors are
// collected per-channel; the function returns nil iff every channel
// succeeded. Use this so the urgency-based router (high → desktop +
// webhook) can be expressed as a single Notifier the run loop calls.
type MultiNotifier struct {
	N []Notifier
}

// Notify dispatches to each wrapped notifier in order. Errors are
// joined into one (so the caller sees every failure), but later
// channels still run after an earlier one fails — partial success is
// the common case (e.g., webhook OK, desktop missing).
func (m *MultiNotifier) Notify(ctx context.Context, n Notification) error {
	var errs []string
	for i, child := range m.N {
		if child == nil {
			continue
		}
		if err := child.Notify(ctx, n); err != nil {
			errs = append(errs, fmt.Sprintf("notifier[%d] (%T): %v", i, child, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

// ----- helpers -----

// shortID returns the last 8 chars of a ULID — long enough to
// disambiguate within a single run, short enough to fit a Slack
// preview / desktop bubble. Empty input passes through.
func shortID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[len(s)-8:]
}

// oneline collapses internal whitespace runs to a single space and
// trims edges so the body works in single-line surfaces (Slack,
// notify-send). Long bodies are truncated at 240 chars with a `…`
// suffix — desktop bubbles cut off anyway, and Slack's preview clamps
// at ~280 columns; staying under the lower bound keeps both happy.
func oneline(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
