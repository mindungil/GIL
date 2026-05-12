package uistyle

import (
	"fmt"
	"time"
)

// HHMM is the canonical wall-clock format used in summaries and
// status cards ("started 18:01"). We deliberately avoid a date
// component — the surfaces that show it are "now"-context and the
// extra width hurts the asymmetric-split layout the spec asks for.
func HHMM(t time.Time) string {
	if t.IsZero() {
		return "--:--"
	}
	return t.Format("15:04")
}

// HHMMSS is the canonical format for activity-log lines (`▏ 18:34:21
// iter 22  → tool_call ...`). Watch + events both share this so a line
// from one surface can be diff'd against the other.
func HHMMSS(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

// Duration formats an elapsed-since value compactly per the spec:
//   "<60s"      → "Ns"
//   "<60m"      → "Nm"
//   "<24h"      → "Nh Mm"   (e.g. "2h 36m")
//   ">=24h"     → "Nd Mh"   (e.g. "3d 4h")
// We never zero-pad — the variable widths are fine because the column
// is right-aligned by the caller.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d / (24 * time.Hour))
	hrs := int((d - time.Duration(days)*24*time.Hour).Hours())
	return fmt.Sprintf("%dd %dh", days, hrs)
}

// Ago is the "<n>m ago" form used in memory-bank excerpts and the
// no-arg summary. It deliberately reads from "now" so callers can mock
// the clock by passing a fixed reference time.
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 0 {
		d = -d
	}
	return Duration(d) + " ago"
}
