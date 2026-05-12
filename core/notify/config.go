package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the on-disk shape of the [notify] section in
// $XDG_CONFIG_HOME/gil/config.toml or <workspace>/.gil/config.toml.
//
// All fields are zero-tolerant: a missing [notify] table or empty
// fields produce an empty Notifier (callers fall back to a stdout-
// only default). The shape is deliberately minimal — three switches
// for three channels — so users don't need to read docs to wire it.
type Config struct {
	// Desktop, when true, enables DesktopNotifier (notify-send /
	// osascript). Defaults to false because not every host has a
	// desktop session, and a missing binary error in a server install
	// is more annoying than helpful.
	Desktop bool `toml:"desktop"`

	// Webhook is the URL to POST notifications to. Empty disables the
	// webhook channel. Slack incoming webhooks (hooks.slack.com) are
	// auto-detected and use the {"text": "..."} body shape; any other
	// URL receives the full Notification struct as JSON.
	Webhook string `toml:"webhook"`

	// Stdout, when true, mirrors notifications to gild's stdout (the
	// daemon log). Useful for headless installs where the user reads
	// `journalctl -u gild`. Default true so the operator always sees
	// at least one channel firing.
	Stdout bool `toml:"stdout"`
}

// LoadConfig reads the [notify] table out of one or two config files.
// Order: globalPath then projectPath; per-field "set" wins (project
// over global). Missing files are not errors. Malformed TOML is.
func LoadConfig(globalPath, projectPath string) (Config, error) {
	cfg := Config{Stdout: true}
	for _, p := range []string{globalPath, projectPath} {
		if p == "" {
			continue
		}
		next, ok, err := loadOne(p)
		if err != nil {
			return Config{}, err
		}
		if !ok {
			continue
		}
		cfg = mergeConfig(cfg, next)
	}
	return cfg, nil
}

// loadOne parses a single config file and pulls the [notify] table.
// Returns ok=false on missing file (callers chain optional layers).
// We unmarshal into a flat parent struct so callers don't have to
// duplicate the [notify] header in their own TOML schemas.
func loadOne(path string) (Config, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("notify config: read %q: %w", path, err)
	}
	var parent struct {
		Notify Config `toml:"notify"`
	}
	if err := toml.Unmarshal(data, &parent); err != nil {
		return Config{}, false, fmt.Errorf("notify config: parse %q: %w", path, err)
	}
	return parent.Notify, true, nil
}

// mergeConfig applies `over` onto `base` field-by-field. We deliberately
// treat false as "set to false" rather than "unset" for Desktop / Stdout
// so a user can disable the global default in a per-project file. (TOML
// has no "missing vs zero" distinction without a custom marshaller; the
// trade-off is that you can't accidentally re-enable a channel by
// omitting the field.)
func mergeConfig(base, over Config) Config {
	out := base
	out.Desktop = over.Desktop || base.Desktop
	out.Stdout = over.Stdout || base.Stdout
	if over.Webhook != "" {
		out.Webhook = over.Webhook
	}
	return out
}

// Build assembles a MultiNotifier from cfg + the supplied stdout sink.
// Returns nil when no channels are configured (so the caller can skip
// the fan-out entirely).
func (cfg Config) Build(stdout io.Writer) Notifier {
	var ns []Notifier
	if cfg.Stdout && stdout != nil {
		ns = append(ns, &StdoutNotifier{Out: stdout})
	}
	if cfg.Desktop {
		ns = append(ns, &DesktopNotifier{})
	}
	if cfg.Webhook != "" {
		ns = append(ns, &WebhookNotifier{URL: cfg.Webhook})
	}
	if len(ns) == 0 {
		return nil
	}
	if len(ns) == 1 {
		return ns[0]
	}
	return &MultiNotifier{N: ns}
}

// FilterByUrgency returns a Notifier that drops the notification when
// the supplied urgency is below the configured floor. Used by the
// run goroutine to honour the "low → stdout/log only" hint without
// re-implementing the gating in every caller. minUrgency vocabulary:
// "low" (no filter), "normal" (drop low), "high" (drop low + normal).
//
// Returns the inner notifier unchanged when minUrgency is empty or
// "low" so the common case is allocation-free.
func FilterByUrgency(inner Notifier, minUrgency string) Notifier {
	if inner == nil || minUrgency == "" || minUrgency == "low" {
		return inner
	}
	return &urgencyFilter{inner: inner, min: minUrgency}
}

type urgencyFilter struct {
	inner Notifier
	min   string
}

func (f *urgencyFilter) Notify(ctx context.Context, n Notification) error {
	if !urgencyAtLeast(n.Urgency, f.min) {
		return nil
	}
	return f.inner.Notify(ctx, n)
}

// urgencyAtLeast returns true when `have` is >= `want` on the
// low<normal<high scale. Empty or unknown "have" treats as normal.
func urgencyAtLeast(have, want string) bool {
	rank := func(u string) int {
		switch u {
		case "low":
			return 0
		case "high":
			return 2
		default:
			return 1 // normal / unknown
		}
	}
	return rank(have) >= rank(want)
}
