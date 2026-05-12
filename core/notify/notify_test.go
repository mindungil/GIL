package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestStdoutNotifier(t *testing.T) {
	var buf bytes.Buffer
	n := &StdoutNotifier{Out: &buf}
	err := n.Notify(context.Background(), Notification{
		Title:     "Clarify",
		Body:      "Need credential",
		Urgency:   "high",
		SessionID: "01HXSESSIONXYZ12345",
		AskID:     "01HXASKXYZ12345",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Clarify") {
		t.Errorf("title missing: %q", out)
	}
	if !strings.Contains(out, "high") {
		t.Errorf("urgency missing: %q", out)
	}
	if !strings.Contains(out, "Need credential") {
		t.Errorf("body missing: %q", out)
	}
}

func TestStdoutNotifier_NoWriter(t *testing.T) {
	n := &StdoutNotifier{}
	err := n.Notify(context.Background(), Notification{Title: "x"})
	if err == nil {
		t.Errorf("expected error when no writer set")
	}
}

func TestDesktopNotifier_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-specific test")
	}
	var got struct {
		name string
		args []string
	}
	d := &DesktopNotifier{
		runner: func(ctx context.Context, name string, args ...string) error {
			got.name = name
			got.args = args
			return nil
		},
	}
	err := d.Notify(context.Background(), Notification{Title: "Q", Body: "Why?", Urgency: "high"})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.name != "notify-send" {
		t.Errorf("expected notify-send, got %q", got.name)
	}
	joined := strings.Join(got.args, " ")
	if !strings.Contains(joined, "--urgency=critical") {
		t.Errorf("high urgency should map to critical: %q", joined)
	}
	if !strings.Contains(joined, "Q") {
		t.Errorf("title missing: %q", joined)
	}
}

func TestDesktopNotifier_LinuxLowUrgency(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-specific test")
	}
	var args []string
	d := &DesktopNotifier{
		runner: func(ctx context.Context, name string, a ...string) error {
			args = a
			return nil
		},
	}
	_ = d.Notify(context.Background(), Notification{Title: "Q", Body: "B", Urgency: "low"})
	if !strings.Contains(strings.Join(args, " "), "--urgency=low") {
		t.Errorf("low urgency mapping wrong: %v", args)
	}
}

func TestDesktopNotifier_RunnerError(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("desktop test")
	}
	d := &DesktopNotifier{
		runner: func(ctx context.Context, name string, a ...string) error {
			return errors.New("boom")
		},
	}
	err := d.Notify(context.Background(), Notification{Title: "x", Body: "y"})
	if err == nil {
		t.Errorf("expected runner error to propagate")
	}
}

func TestWebhookNotifier_GenericJSON(t *testing.T) {
	var (
		mu      sync.Mutex
		payload Notification
		ct      string
		hits    int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		ct = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &WebhookNotifier{URL: srv.URL}
	err := wh.Notify(context.Background(), Notification{Title: "Q", Body: "context", Urgency: "normal"})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("expected 1 hit, got %d", hits)
	}
	if ct != "application/json" {
		t.Errorf("content-type: %q", ct)
	}
	if payload.Title != "Q" {
		t.Errorf("title: %q", payload.Title)
	}
}

func TestWebhookNotifier_Slack(t *testing.T) {
	var seen map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &WebhookNotifier{URL: srv.URL, Slack: true}
	err := wh.Notify(context.Background(), Notification{
		Title:     "Q",
		Body:      "ctx",
		SessionID: "01HXSESSIONXYZ",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	text, ok := seen["text"]
	if !ok || text == "" {
		t.Fatalf("Slack body missing 'text': %+v", seen)
	}
	if !strings.Contains(text, "Q") || !strings.Contains(text, "ctx") {
		t.Errorf("Slack body missing fields: %q", text)
	}
}

func TestWebhookNotifier_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	wh := &WebhookNotifier{URL: srv.URL}
	err := wh.Notify(context.Background(), Notification{Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 error, got %v", err)
	}
}

func TestWebhookNotifier_EmptyURL(t *testing.T) {
	wh := &WebhookNotifier{}
	err := wh.Notify(context.Background(), Notification{Title: "x"})
	if err == nil {
		t.Errorf("expected error on empty URL")
	}
}

func TestMultiNotifier_FanOut(t *testing.T) {
	var a, b bytes.Buffer
	mn := &MultiNotifier{N: []Notifier{
		&StdoutNotifier{Out: &a},
		&StdoutNotifier{Out: &b},
	}}
	err := mn.Notify(context.Background(), Notification{Title: "T", Body: "B"})
	if err != nil {
		t.Fatalf("MultiNotifier: %v", err)
	}
	if !strings.Contains(a.String(), "T") || !strings.Contains(b.String(), "T") {
		t.Errorf("fan-out broken: a=%q b=%q", a.String(), b.String())
	}
}

func TestMultiNotifier_PartialFailure(t *testing.T) {
	var ok bytes.Buffer
	mn := &MultiNotifier{N: []Notifier{
		&StdoutNotifier{}, // no writer → errors
		&StdoutNotifier{Out: &ok},
	}}
	err := mn.Notify(context.Background(), Notification{Title: "T", Body: "B"})
	if err == nil {
		t.Errorf("expected aggregated error")
	}
	if !strings.Contains(ok.String(), "T") {
		t.Errorf("second notifier should still have run after first failed: %q", ok.String())
	}
}

func TestMultiNotifier_NilSkips(t *testing.T) {
	var buf bytes.Buffer
	mn := &MultiNotifier{N: []Notifier{nil, &StdoutNotifier{Out: &buf}, nil}}
	err := mn.Notify(context.Background(), Notification{Title: "T", Body: "B"})
	if err != nil {
		t.Fatalf("nil entries should be skipped: %v", err)
	}
	if !strings.Contains(buf.String(), "T") {
		t.Errorf("non-nil notifier did not run")
	}
}

func TestOneline_Truncation(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := oneline(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix on long body, got len=%d", len(got))
	}
	if len(got) > 250 {
		t.Errorf("body not truncated: %d", len(got))
	}
}

func TestShortID(t *testing.T) {
	if shortID("01HXABCDEFGHIJKL") != "GHIJKL" && shortID("01HXABCDEFGHIJKL") != "FGHIJKL" {
		// Just check len <= 8
		if len(shortID("01HXABCDEFGHIJKL")) != 8 {
			t.Errorf("shortID should keep last 8: got %q", shortID("01HXABCDEFGHIJKL"))
		}
	}
	if shortID("short") != "short" {
		t.Errorf("short input passes through")
	}
}
