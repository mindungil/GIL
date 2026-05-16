package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// agent_tools_extra.go adds three high-value tools that close the
// biggest gaps relative to opencode/codex (see #267 head-to-head):
//
//   - edit_file: line-range targeted edit. write_file replaces the
//     whole file which costs context tokens on big files; edit_file
//     only sends the slice the agent wants to change.
//   - todowrite: lightweight per-session todo list the agent can
//     persist + mutate. Used by the "plan" agent to render a plan;
//     the "default" agent can also use it to track in-flight work.
//   - webfetch: GET a URL, return text body. Capped at 256 KB and
//     15s, scoped to http(s) only — no file:// or other schemes.
//
// V1 scope: in-memory todo store (lost on daemon restart, same as
// chatHistory). webfetch follows redirects up to 5 hops, no auth.

const (
	maxEditFileSize  = 1 * 1024 * 1024
	maxFetchBytes    = 256 * 1024
	fetchTimeout     = 15 * time.Second
	fetchMaxRedirect = 5
)

// --- edit_file -------------------------------------------------------

type toolEditFile struct {
	repo    *session.Repo
	tracker *turnDiffTracker
}

func (t *toolEditFile) name() string { return "edit_file" }

func (t *toolEditFile) description() string {
	return "Replace exact text inside a file with new text. " +
		"Prefer this over write_file when changing a small slice of a large file — it sends only the diff. " +
		"old_text must match the file content exactly once; if the snippet is ambiguous (matches >1 location) the call is rejected, " +
		"so include enough surrounding context to make it unique."
}

func (t *toolEditFile) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"Path relative to working dir."},
			"old_text":{"type":"string","description":"Exact text snippet currently in the file. Must match exactly once."},
			"new_text":{"type":"string","description":"Replacement text. May be empty to delete the snippet."}
		},
		"required":["path","old_text","new_text"],
		"additionalProperties":false
	}`)
}

func (t *toolEditFile) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if args.OldText == "" {
		return provider.ToolResult{Content: "old_text is empty; use write_file to create a new file", IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	abs, err := resolveInWD(wd, args.Path)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := rejectReadonlyTarget(abs); err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return provider.ToolResult{Content: "read: " + err.Error(), IsError: true}, nil
	}
	if len(body) > maxEditFileSize {
		return provider.ToolResult{
			Content: fmt.Sprintf("file too large: %d bytes (max %d)", len(body), maxEditFileSize),
			IsError: true,
		}, nil
	}
	src := string(body)
	count := strings.Count(src, args.OldText)
	if count == 0 {
		return provider.ToolResult{Content: "old_text not found in file; check whitespace and surrounding context", IsError: true}, nil
	}
	if count > 1 {
		return provider.ToolResult{
			Content: fmt.Sprintf("old_text matches %d locations; add surrounding context to make it unique", count),
			IsError: true,
		}, nil
	}
	if t.tracker != nil {
		t.tracker.recordPreWrite(sessionID, args.Path, abs)
	}
	updated := strings.Replace(src, args.OldText, args.NewText, 1)
	tmp := abs + ".giledit.tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o644); err != nil {
		return provider.ToolResult{Content: "write: " + err.Error(), IsError: true}, nil
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return provider.ToolResult{Content: "rename: " + err.Error(), IsError: true}, nil
	}
	if t.tracker != nil {
		t.tracker.recordPostWrite(sessionID, args.Path, updated, true)
	}
	return provider.ToolResult{
		Content: fmt.Sprintf("edited %s (-%d/+%d bytes)", args.Path, len(args.OldText), len(args.NewText)),
	}, nil
}

// --- todowrite -------------------------------------------------------

// todoStore is the daemon's in-memory todo list per session. Loss on
// restart is acceptable for V1: todos are a working scratchpad, not
// durable state. A SQLite-backed follow-up can land alongside the
// chatHistory persistence work.
type todoStore struct {
	mu    sync.Mutex
	items map[string][]todoItem // sessionID → list
}

type todoItem struct {
	ID      int    `json:"id"`
	Text    string `json:"text"`
	Status  string `json:"status"` // "pending" | "in_progress" | "completed"
	Created int64  `json:"created_unix"`
}

var globalTodoStore = &todoStore{items: make(map[string][]todoItem)}

func (s *todoStore) snapshot(sid string) []todoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.items[sid]
	out := make([]todoItem, len(src))
	copy(out, src)
	return out
}

func (s *todoStore) replace(sid string, items []todoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]todoItem, len(items))
	copy(cp, items)
	s.items[sid] = cp
}

type toolTodoWrite struct{}

func (t *toolTodoWrite) name() string { return "todowrite" }

func (t *toolTodoWrite) description() string {
	return "Persist a todo list for the current session. Replaces any existing list. " +
		"Use to track multi-step work — break a task into steps with statuses pending/in_progress/completed. " +
		"The list survives across turns within the session. Pass an empty list to clear."
}

func (t *toolTodoWrite) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"items":{
				"type":"array",
				"description":"Full todo list. Replaces the existing list — pass all items each call.",
				"items":{
					"type":"object",
					"properties":{
						"text":{"type":"string"},
						"status":{"type":"string","enum":["pending","in_progress","completed"]}
					},
					"required":["text","status"],
					"additionalProperties":false
				}
			}
		},
		"required":["items"],
		"additionalProperties":false
	}`)
}

func (t *toolTodoWrite) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Items []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	now := time.Now().Unix()
	items := make([]todoItem, 0, len(args.Items))
	for i, it := range args.Items {
		st := it.Status
		switch st {
		case "pending", "in_progress", "completed":
		default:
			return provider.ToolResult{
				Content: fmt.Sprintf("item %d: status %q must be pending|in_progress|completed", i+1, st),
				IsError: true,
			}, nil
		}
		if strings.TrimSpace(it.Text) == "" {
			return provider.ToolResult{Content: fmt.Sprintf("item %d: text is empty", i+1), IsError: true}, nil
		}
		items = append(items, todoItem{
			ID:      i + 1,
			Text:    it.Text,
			Status:  st,
			Created: now,
		})
	}
	globalTodoStore.replace(sessionID, items)

	if len(items) == 0 {
		return provider.ToolResult{Content: "todo list cleared"}, nil
	}
	var b strings.Builder
	for _, it := range items {
		mark := "[ ]"
		switch it.Status {
		case "in_progress":
			mark = "[~]"
		case "completed":
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%s %d. %s\n", mark, it.ID, it.Text)
	}
	return provider.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// --- webfetch --------------------------------------------------------

// webfetchSafeDial is the http.Transport.DialContext used by webfetch.
// Resolves the target host, rejects internal IPs, then dials the first
// public IP. Env GIL_WEBFETCH_ALLOW_INTERNAL=1 disables the check for
// dev workflows that legitimately need localhost.
func webfetchSafeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if rejection := webfetchValidateHost(host); rejection != "" {
		return nil, errors.New(rejection)
	}
	// Resolve and dial the first allowed IP. We do the resolve again
	// here even though webfetchValidateHost did one (TOCTOU mitigation:
	// reuse the same lookup result).
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, ipa := range ips {
		if !webfetchAllowInternal() && webfetchIPInternal(ipa.IP) {
			dialErr = errors.New("resolved IP is internal/loopback/link-local: " + ipa.IP.String())
			continue
		}
		var d net.Dialer
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		dialErr = derr
	}
	if dialErr == nil {
		dialErr = errors.New("no usable address for " + host)
	}
	return nil, dialErr
}

// webfetchValidateHost returns "" if the host is OK to fetch, or a
// rejection reason string otherwise. Performs DNS resolution.
func webfetchValidateHost(host string) string {
	if webfetchAllowInternal() {
		return ""
	}
	host = strings.ToLower(strings.TrimSpace(host))
	// Cheap string checks for common loopback hostnames before resolving.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "host '" + host + "' is loopback/local; refusing to fetch"
	}
	// Resolve and inspect each address.
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		// Unresolvable hosts will fail the dial anyway; pass through so
		// the caller sees a normal DNS error rather than an SSRF reject.
		return ""
	}
	for _, ipa := range ips {
		if webfetchIPInternal(ipa.IP) {
			return "host '" + host + "' resolves to internal IP " + ipa.IP.String()
		}
	}
	return ""
}

// webfetchIPInternal reports whether ip is loopback, link-local, or a
// private RFC1918/ULA range. Link-local 169.254.0.0/16 covers cloud
// instance metadata (AWS/GCP/Azure all use 169.254.169.254).
func webfetchIPInternal(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	if ip.IsUnspecified() {
		return true
	}
	return false
}

func webfetchAllowInternal() bool {
	return os.Getenv("GIL_WEBFETCH_ALLOW_INTERNAL") == "1"
}

type toolWebFetch struct{}

func (t *toolWebFetch) name() string { return "webfetch" }

func (t *toolWebFetch) description() string {
	return "Fetch a URL (http/https only) and return the response body as text. " +
		"Capped at 256 KB and 15s. Use to read documentation, GitHub issues, or other public web content. " +
		"Does NOT execute JavaScript — many SPA pages will return an empty shell."
}

func (t *toolWebFetch) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"url":{"type":"string","description":"Absolute http(s) URL to fetch."}
		},
		"required":["url"],
		"additionalProperties":false
	}`)
}

func (t *toolWebFetch) run(ctx context.Context, _ string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	rawURL := strings.TrimSpace(args.URL)
	if !(strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")) {
		return provider.ToolResult{Content: "url must be http(s)://", IsError: true}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", rawURL, nil)
	if err != nil {
		return provider.ToolResult{Content: "request: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "gil-webfetch/1.0")

	client := &http.Client{
		Timeout: fetchTimeout,
		// iter34a: SSRF defense. The bare http.Client happily resolves
		// and connects to internal IPs — eval-loop iter34 confirmed
		// localhost/127.0.0.1 + RFC1918 + 169.254.169.254 (cloud
		// instance metadata) all reachable. A prompt-injected URL in
		// fetched content (issue body, docs page) could chain to leak
		// credentials via the metadata service.
		//
		// Custom Dialer resolves AND inspects the resolved IP before
		// connecting. Rejects loopback / link-local / private. Power
		// users can re-enable via env (legitimate dev workflows fetching
		// a local server — those should usually go through run_bash curl
		// anyway).
		Transport: &http.Transport{
			DialContext: webfetchSafeDial,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= fetchMaxRedirect {
				return errors.New("too many redirects")
			}
			// iter34a: re-validate redirect target host (CheckRedirect
			// is the only place to catch redirects to internal IPs).
			if u, perr := url.Parse(req.URL.String()); perr == nil {
				if rejection := webfetchValidateHost(u.Hostname()); rejection != "" {
					return errors.New("redirect rejected: " + rejection)
				}
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return provider.ToolResult{Content: "fetch: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return provider.ToolResult{Content: "read body: " + err.Error(), IsError: true}, nil
	}
	truncated := false
	if len(body) > maxFetchBytes {
		body = body[:maxFetchBytes]
		truncated = true
	}
	header := fmt.Sprintf("[%s] %s\nContent-Type: %s\n",
		resp.Status, resp.Request.URL.String(), resp.Header.Get("Content-Type"))
	out := header + "\n" + string(body)
	if truncated {
		out += fmt.Sprintf("\n... (truncated at %d bytes)", maxFetchBytes)
	}
	isErr := resp.StatusCode >= 400
	return provider.ToolResult{Content: out, IsError: isErr}, nil
}
