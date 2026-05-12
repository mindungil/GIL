package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultRequestTimeout caps every request/response round-trip. LSP queries
// against large workspaces can be slow on first warm-up (gopls indexing,
// pyright analysing dependencies); 10s is the agent-tool guidance from the
// Phase 18 Track C spec and matches opencode's range.
const DefaultRequestTimeout = 10 * time.Second

// initializeTimeout is wider than DefaultRequestTimeout because the very
// first `initialize` request triggers the server's full workspace scan.
const initializeTimeout = 45 * time.Second

// Client is a JSON-RPC client to one LSP server subprocess. One Client
// owns one process. Methods are safe for concurrent use; the request
// dispatcher serialises writes and routes responses back to the calling
// goroutine via a per-request channel keyed on the JSON-RPC id.
type Client struct {
	serverID string
	rootURI  string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex // guards writes to stdin
	idCtr   atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rawMessage // id → response channel

	diagMu      sync.Mutex
	diagnostics map[string][]Diagnostic // file path → latest published diagnostics

	closed atomic.Bool
	doneCh chan struct{} // closed when the read loop exits
}

// NewClient wires the I/O channels of an already-spawned subprocess into
// a Client, kicks off the read loop, and returns. Initialize must still be
// called separately (because the server-spawn path differs per language).
//
// The caller retains responsibility for `cmd` lifecycle until Shutdown is
// called; Shutdown sends `shutdown` + `exit` and waits for the process to
// exit.
func NewClient(serverID, rootURI string, cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser) *Client {
	c := &Client{
		serverID:    serverID,
		rootURI:     rootURI,
		cmd:         cmd,
		stdin:       stdin,
		stdout:      stdout,
		pending:     make(map[int64]chan rawMessage),
		diagnostics: make(map[string][]Diagnostic),
		doneCh:      make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// readLoop reads framed JSON-RPC messages off stdout and either routes a
// response to its waiting caller (by id) or stores a published-diagnostics
// notification. Notifications other than `textDocument/publishDiagnostics`
// are ignored — we don't need workDone progress, registerCapability, etc.
// for the synchronous request flow the agent tool uses.
func (c *Client) readLoop() {
	defer close(c.doneCh)
	r := bufio.NewReader(c.stdout)
	for {
		msg, err := readMessage(r)
		if err != nil {
			// EOF or framing error: fail every pending request.
			c.pendingMu.Lock()
			for id, ch := range c.pending {
				ch <- rawMessage{Error: &rpcError{Code: -32603, Message: "lsp connection closed: " + err.Error()}}
				close(ch)
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			return
		}
		// Response to a request we made.
		if len(msg.ID) > 0 && (msg.Result != nil || msg.Error != nil) {
			id, perr := parseID(msg.ID)
			if perr == nil {
				c.pendingMu.Lock()
				ch, ok := c.pending[id]
				if ok {
					delete(c.pending, id)
				}
				c.pendingMu.Unlock()
				if ok {
					ch <- msg
					close(ch)
				}
			}
			continue
		}
		// Notification from the server.
		if msg.Method == "textDocument/publishDiagnostics" {
			var p struct {
				URI         string       `json:"uri"`
				Diagnostics []Diagnostic `json:"diagnostics"`
			}
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				if path, perr := uriToPath(p.URI); perr == nil {
					c.diagMu.Lock()
					c.diagnostics[path] = p.Diagnostics
					c.diagMu.Unlock()
				}
			}
			continue
		}
		// Server-to-client request (e.g. workspace/configuration). Some
		// servers refuse to proceed unless we reply, so send a null
		// response with the same id so the server doesn't deadlock.
		if len(msg.ID) > 0 && msg.Method != "" {
			_ = c.write(rawMessage{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  json.RawMessage("null"),
			})
		}
	}
}

// readMessage parses one Content-Length-framed JSON-RPC message off the
// reader. It tolerates additional headers (Content-Type) and stops at the
// blank line per the LSP base protocol.
func readMessage(r *bufio.Reader) (rawMessage, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return rawMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.EqualFold(k, "Content-Length") {
			n, err := strconv.Atoi(v)
			if err != nil {
				return rawMessage{}, fmt.Errorf("bad Content-Length %q: %w", v, err)
			}
			contentLength = n
		}
	}
	if contentLength == 0 {
		return rawMessage{}, errors.New("missing or zero Content-Length")
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return rawMessage{}, err
	}
	var msg rawMessage
	if err := json.Unmarshal(buf, &msg); err != nil {
		return rawMessage{}, fmt.Errorf("decode message: %w", err)
	}
	return msg, nil
}

// write framing-encodes one JSON-RPC message and sends it on stdin. Holds
// writeMu so concurrent requests can't interleave.
func (c *Client) write(msg rawMessage) error {
	if c.closed.Load() {
		return errors.New("client closed")
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err = c.stdin.Write(body)
	return err
}

// call issues a request and blocks until the response arrives, ctx is
// cancelled, or DefaultRequestTimeout elapses (whichever comes first). The
// reply's Result is decoded into out (which may be nil to discard).
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	return c.callWithTimeout(ctx, method, params, out, DefaultRequestTimeout)
}

func (c *Client) callWithTimeout(ctx context.Context, method string, params any, out any, timeout time.Duration) error {
	if c.closed.Load() {
		return errors.New("client closed")
	}
	id := c.idCtr.Add(1)
	ch := make(chan rawMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	idJSON, _ := json.Marshal(id)
	var paramsJSON json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			c.dropPending(id)
			return err
		}
		paramsJSON = b
	}
	if err := c.write(rawMessage{JSONRPC: "2.0", ID: idJSON, Method: method, Params: paramsJSON}); err != nil {
		c.dropPending(id)
		return err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("lsp %s: %s", method, msg.Error.Message)
		}
		if out != nil && len(msg.Result) > 0 && string(msg.Result) != "null" {
			if err := json.Unmarshal(msg.Result, out); err != nil {
				return fmt.Errorf("lsp %s: decode result: %w", method, err)
			}
		}
		return nil
	case <-timer.C:
		c.dropPending(id)
		return fmt.Errorf("lsp %s: timeout after %s", method, timeout)
	case <-ctx.Done():
		c.dropPending(id)
		return ctx.Err()
	}
}

func (c *Client) dropPending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *Client) notify(method string, params any) error {
	var paramsJSON json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		paramsJSON = b
	}
	return c.write(rawMessage{JSONRPC: "2.0", Method: method, Params: paramsJSON})
}

// Initialize performs the LSP handshake. Must be called once before any
// other method.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"processId":    nil,
		"rootUri":      c.rootURI,
		"capabilities": defaultCapabilities(),
		"workspaceFolders": []map[string]string{
			{"uri": c.rootURI, "name": "workspace"},
		},
	}
	if err := c.callWithTimeout(ctx, "initialize", params, nil, initializeTimeout); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func defaultCapabilities() map[string]any {
	return map[string]any{
		"workspace": map[string]any{
			"workspaceFolders": true,
			"configuration":    true,
		},
		"textDocument": map[string]any{
			"synchronization": map[string]any{
				"didOpen":   true,
				"didChange": true,
			},
			"hover":            map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
			"definition":       map[string]any{},
			"references":       map[string]any{},
			"rename":           map[string]any{"prepareSupport": false},
			"completion":       map[string]any{"completionItem": map[string]any{"snippetSupport": false}},
			"documentSymbol":   map[string]any{"hierarchicalDocumentSymbolSupport": true},
			"signatureHelp":    map[string]any{},
			"publishDiagnostics": map[string]any{"versionSupport": false},
		},
	}
}

// NotifyDidOpen ensures the server has the file's contents loaded. Many
// servers refuse hover/definition queries against files they haven't been
// told about. Idempotent — sending the same file twice is harmless. The
// caller (typically the agent-callable lsp tool) reads the file body off
// disk and passes it here.
func (c *Client) NotifyDidOpen(file string, languageID, text string) error {
	uri := pathToURI(file)
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// Hover returns the documentation/type-info shown when an editor's cursor
// hovers over `(line, col)`. Returns nil + nil if the server has nothing
// to say at that position.
func (c *Client) Hover(ctx context.Context, file string, line, col int) (*Hover, error) {
	var raw struct {
		Contents json.RawMessage `json:"contents"`
		Range    *Range          `json:"range,omitempty"`
	}
	err := c.call(ctx, "textDocument/hover", positionParams(file, line, col), &raw)
	if err != nil {
		return nil, err
	}
	if len(raw.Contents) == 0 {
		return nil, nil
	}
	contents := flattenHoverContents(raw.Contents)
	return &Hover{Contents: contents, Range: raw.Range}, nil
}

// Definition returns the source location(s) where the symbol at
// `(line, col)` is defined. Empty slice when the server doesn't know.
func (c *Client) Definition(ctx context.Context, file string, line, col int) ([]Location, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/definition", positionParams(file, line, col), &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

// References returns every place the symbol at `(line, col)` is used,
// including the declaration itself.
func (c *Client) References(ctx context.Context, file string, line, col int) ([]Location, error) {
	params := positionParams(file, line, col)
	params["context"] = map[string]bool{"includeDeclaration": true}
	var locs []Location
	if err := c.call(ctx, "textDocument/references", params, &locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// Rename returns the WorkspaceEdit the agent should apply to rename the
// symbol at `(line, col)` to `newName`. Returns nil + nil if the server
// declines the rename (e.g. position not on a symbol).
func (c *Client) Rename(ctx context.Context, file string, line, col int, newName string) (*WorkspaceEdit, error) {
	params := positionParams(file, line, col)
	params["newName"] = newName
	var edit WorkspaceEdit
	if err := c.call(ctx, "textDocument/rename", params, &edit); err != nil {
		return nil, err
	}
	if len(edit.Changes) == 0 {
		return nil, nil
	}
	return &edit, nil
}

// Completion returns suggested completions at `(line, col)`. We trim the
// shape down to label/detail/kind because that's all the agent can act on.
func (c *Client) Completion(ctx context.Context, file string, line, col int) ([]CompletionItem, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/completion", positionParams(file, line, col), &raw); err != nil {
		return nil, err
	}
	// Server may return either CompletionItem[] or CompletionList { items: ... }.
	if len(raw) == 0 {
		return nil, nil
	}
	var list struct {
		Items []CompletionItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		return list.Items, nil
	}
	var items []CompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode completion: %w", err)
	}
	return items, nil
}

// DocumentSymbols returns the outline of the file. Servers may reply with
// either DocumentSymbol[] (hierarchical) or SymbolInformation[] (flat); we
// flatten the latter into the former so the tool output is consistent.
func (c *Client) DocumentSymbols(ctx context.Context, file string) ([]DocumentSymbol, error) {
	uri := pathToURI(file)
	var raw json.RawMessage
	err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]string{"uri": uri},
	}, &raw)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Try hierarchical shape first.
	var docs []DocumentSymbol
	if err := json.Unmarshal(raw, &docs); err == nil && (len(docs) == 0 || docs[0].Name != "" || docs[0].Kind != 0) {
		// Heuristic: hierarchical symbols always have a Name OR Kind set.
		// SymbolInformation also has Name+Kind so fall through to flat
		// decode if the hierarchical decode didn't produce anything useful.
		if len(docs) > 0 {
			return docs, nil
		}
	}
	var flat []SymbolInformation
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("decode documentSymbol: %w", err)
	}
	out := make([]DocumentSymbol, 0, len(flat))
	for _, s := range flat {
		out = append(out, DocumentSymbol{
			Name:           s.Name,
			Kind:           s.Kind,
			Range:          s.Location.Range,
			SelectionRange: s.Location.Range,
		})
	}
	return out, nil
}

// WorkspaceSymbols searches every open file in the workspace for symbols
// whose name matches `query` (server-defined matching, usually substring).
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolInformation, error) {
	var syms []SymbolInformation
	if err := c.call(ctx, "workspace/symbol", map[string]string{"query": query}, &syms); err != nil {
		return nil, err
	}
	return syms, nil
}

// SignatureHelp returns the signatures the function call at `(line, col)`
// matches. Returns nil + nil when the cursor is not inside a call.
func (c *Client) SignatureHelp(ctx context.Context, file string, line, col int) (*SignatureHelp, error) {
	var help SignatureHelp
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/signatureHelp", positionParams(file, line, col), &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &help); err != nil {
		return nil, err
	}
	if len(help.Signatures) == 0 {
		return nil, nil
	}
	return &help, nil
}

// Diagnostics returns the cached diagnostics for `file` (last set the
// server published via textDocument/publishDiagnostics). Servers that use
// the newer pull model are not yet supported by this client; if the agent
// asks for diagnostics on such a server it just gets the empty slice
// (which is honest — the client genuinely has nothing).
func (c *Client) Diagnostics(file string) []Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	out := make([]Diagnostic, len(c.diagnostics[file]))
	copy(out, c.diagnostics[file])
	return out
}

// Shutdown sends `shutdown` + `exit` and waits for the subprocess. After
// Shutdown the client is unusable. Best-effort: when the server is
// already dead (or there's no real subprocess, as in tests) we still
// close stdin and the read loop so cleanup completes synchronously.
func (c *Client) Shutdown(ctx context.Context) error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	// Try the polite shutdown handshake but don't block long on it —
	// many of our shutdown paths are post-error, where the server has
	// already vanished and any further write blocks until the OS pipe
	// closes. Close stdin first so the server's read side EOFs even
	// when the request itself never gets a reply.
	if c.cmd != nil { // real subprocess: do the protocol shutdown
		shutdownCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		_ = c.callWithTimeout(shutdownCtx, "shutdown", nil, nil, 1*time.Second)
		cancel()
		_ = c.notify("exit", nil)
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		// Closing stdout (PipeReader in tests, OS pipe in prod) breaks
		// the read loop's blocking ReadString, which is what allows
		// Shutdown to return promptly.
		_ = c.stdout.Close()
	}
	select {
	case <-c.doneCh:
	case <-time.After(500 * time.Millisecond):
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
	return nil
}

// --- helpers --------------------------------------------------------------

func positionParams(file string, line, col int) map[string]any {
	return map[string]any{
		"textDocument": map[string]string{"uri": pathToURI(file)},
		"position":     map[string]int{"line": line, "character": col},
	}
}

// pathToURI converts an absolute filesystem path to a `file://` URI per
// RFC 8089. Windows drive letters get the leading slash dance the LSP spec
// requires.
func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.ToSlash(abs)
	if runtime.GOOS == "windows" {
		// file:///C:/foo
		if !strings.HasPrefix(abs, "/") {
			abs = "/" + abs
		}
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String()
}

func uriToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI: %s", uri)
	}
	p := u.Path
	if runtime.GOOS == "windows" && strings.HasPrefix(p, "/") && len(p) > 2 && p[2] == ':' {
		p = p[1:]
	}
	return p, nil
}

// decodeLocations handles the three shapes textDocument/definition can
// reply with: a single Location, an array of Locations, or an array of
// LocationLink. We collapse all three to []Location.
func decodeLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// Try array of Locations.
	var arr []Location
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 && arr[0].URI != "" {
		return arr
	}
	// Try single Location.
	var single Location
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{single}
	}
	// Try array of LocationLink (different field name).
	var links []struct {
		TargetURI   string `json:"targetUri"`
		TargetRange Range  `json:"targetRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil {
		out := make([]Location, 0, len(links))
		for _, l := range links {
			if l.TargetURI != "" {
				out = append(out, Location{URI: l.TargetURI, Range: l.TargetRange})
			}
		}
		return out
	}
	return nil
}

func flattenHoverContents(raw json.RawMessage) string {
	// Try string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try MarkupContent.
	var mc MarkupContent
	if err := json.Unmarshal(raw, &mc); err == nil && mc.Value != "" {
		return mc.Value
	}
	// Try array of strings or MarkupContent.
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, item := range arr {
			parts = append(parts, flattenHoverContents(item))
		}
		return strings.Join(parts, "\n\n")
	}
	// Fallback: return raw JSON as string.
	return string(raw)
}

// parseID accepts either a numeric or string LSP id. Our calls always use
// numeric so the string branch is mostly defensive.
func parseID(raw json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseInt(s, 10, 64)
	}
	return 0, errors.New("unrecognised id type")
}
