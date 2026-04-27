package notify

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_DefaultsOnly(t *testing.T) {
	cfg, err := LoadConfig("", "")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Stdout {
		t.Errorf("default Stdout should be true, got false")
	}
	if cfg.Desktop {
		t.Errorf("default Desktop should be false")
	}
	if cfg.Webhook != "" {
		t.Errorf("default Webhook should be empty, got %q", cfg.Webhook)
	}
}

func TestLoadConfig_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	g := filepath.Join(dir, "global.toml")
	p := filepath.Join(dir, "project.toml")

	if err := os.WriteFile(g, []byte(`
[notify]
desktop = false
webhook = "https://global.example/webhook"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`
[notify]
desktop = true
webhook = "https://hooks.slack.com/services/T/B/X"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(g, p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Desktop {
		t.Errorf("project should enable desktop")
	}
	if cfg.Webhook != "https://hooks.slack.com/services/T/B/X" {
		t.Errorf("project webhook should win: %q", cfg.Webhook)
	}
}

func TestLoadConfig_BadTOML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(p, []byte(`not = valid = toml`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig("", p); err == nil {
		t.Errorf("expected parse error")
	}
}

func TestConfig_Build_Empty(t *testing.T) {
	cfg := Config{}
	if cfg.Build(nil) != nil {
		t.Errorf("empty config should yield nil notifier")
	}
}

func TestConfig_Build_StdoutOnly(t *testing.T) {
	cfg := Config{Stdout: true}
	var buf bytes.Buffer
	n := cfg.Build(&buf)
	if n == nil {
		t.Fatalf("nil notifier")
	}
	_ = n.Notify(context.Background(), Notification{Title: "t", Body: "b"})
	if buf.Len() == 0 {
		t.Errorf("stdout not invoked")
	}
}

func TestConfig_Build_MultiCombines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{Stdout: true, Webhook: srv.URL}
	var buf bytes.Buffer
	n := cfg.Build(&buf)
	if _, ok := n.(*MultiNotifier); !ok {
		t.Errorf("expected *MultiNotifier with 2 channels, got %T", n)
	}
}

func TestFilterByUrgency_DropsBelowFloor(t *testing.T) {
	var buf bytes.Buffer
	inner := &StdoutNotifier{Out: &buf}
	f := FilterByUrgency(inner, "high")

	_ = f.Notify(context.Background(), Notification{Title: "x", Urgency: "low"})
	_ = f.Notify(context.Background(), Notification{Title: "x", Urgency: "normal"})
	if buf.Len() != 0 {
		t.Errorf("low + normal should be dropped at high floor: %q", buf.String())
	}
	_ = f.Notify(context.Background(), Notification{Title: "x", Urgency: "high"})
	if buf.Len() == 0 {
		t.Errorf("high should pass through high floor")
	}
}

func TestFilterByUrgency_PassthroughWhenNoFloor(t *testing.T) {
	var buf bytes.Buffer
	inner := &StdoutNotifier{Out: &buf}
	if FilterByUrgency(inner, "") != inner {
		t.Errorf("empty floor should return inner unchanged")
	}
	if FilterByUrgency(inner, "low") != inner {
		t.Errorf("low floor (no filter) should return inner unchanged")
	}
}
