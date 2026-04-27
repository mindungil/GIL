package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mindungil/gil/core/lsp"
)

// LSP is the agent-callable code-intelligence tool. One tool with an
// `operation` enum (mirroring opencode's design) keeps the surface area
// the model has to learn small.
//
// Operations:
//   - hover: docs/type info at (file,line,column)
//   - definition: where the symbol at (file,line,column) is defined
//   - references: where the symbol at (file,line,column) is used
//   - rename: produce a WorkspaceEdit renaming the symbol at (file,line,column)
//     to new_name. Does NOT apply the edit — the agent runs apply_patch /
//     edit afterward.
//   - completion: completion suggestions at (file,line,column)
//   - document_symbols: outline of file
//   - workspace_symbols: search every open file for symbols matching `query`
//   - signature_help: parameter hints at (file,line,column)
//   - diagnostics: errors/warnings the LSP server has published for `file`
//
// Lazy spawn: the underlying lsp.Manager spins up gopls / pyright /
// typescript-language-server / rust-analyzer on first use. If the binary
// isn't installed the tool returns a one-line install hint and the agent
// falls back to grep / repomap.
//
// Coordinates: the tool accepts 1-based line/column (matching what the
// model reads off of editor screenshots and the way most CLIs print
// matches) and converts to LSP's 0-based wire format internally.
type LSP struct {
	// Manager is the per-workspace LSP manager. Required (constructor
	// asserts non-nil to fail loud if the run.go wiring forgets it).
	Manager *lsp.Manager

	// WorkingDir is the workspace root used to resolve relative file
	// paths. May equal Manager.Workspace; we keep them separate so a
	// caller could (e.g.) keep the manager rooted at the git root while
	// the tool resolves relative paths against a sub-package.
	WorkingDir string
}

const lspSchema = `{
  "type":"object",
  "properties":{
    "operation":{
      "type":"string",
      "enum":["hover","definition","references","rename","completion","document_symbols","workspace_symbols","signature_help","diagnostics"],
      "description":"Which LSP operation to perform"
    },
    "file":{"type":"string","description":"Workspace-relative or absolute path. Required for every operation EXCEPT workspace_symbols."},
    "line":{"type":"integer","description":"1-based line number, as shown in editors. Required for hover/definition/references/rename/completion/signature_help."},
    "column":{"type":"integer","description":"1-based column (character offset) on that line. Required wherever line is required."},
    "new_name":{"type":"string","description":"Required for rename — the new identifier."},
    "query":{"type":"string","description":"Required for workspace_symbols — the symbol-name search query (server-defined matching, usually substring)."}
  },
  "required":["operation"]
}`

func (l *LSP) Name() string { return "lsp" }

func (l *LSP) Description() string {
	return "Code intelligence: hover, definition, references, rename, completion, symbols, signature_help, diagnostics. Wraps gopls / pyright / typescript-language-server / rust-analyzer. Use this instead of grep when you need PRECISE symbol resolution across files (e.g., \"rename Foo everywhere it's referenced\", \"where is this function defined\"). Returns clear errors when no language server is available — agent should fall back to grep/repomap in that case."
}

func (l *LSP) Schema() json.RawMessage { return json.RawMessage(lspSchema) }

// Args is the parsed input for one tool call.
type lspArgs struct {
	Operation string `json:"operation"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	NewName   string `json:"new_name"`
	Query     string `json:"query"`
}

func (l *LSP) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	if l.Manager == nil {
		return Result{Content: "lsp: not configured (no manager wired)", IsError: true}, nil
	}
	var a lspArgs
	if err := json.Unmarshal(argsJSON, &a); err != nil {
		return Result{Content: "lsp unmarshal: " + err.Error(), IsError: true}, nil
	}
	if a.Operation == "" {
		return Result{Content: "lsp: operation is required", IsError: true}, nil
	}

	// workspace_symbols is the one operation with no file.
	if a.Operation == "workspace_symbols" {
		return l.runWorkspaceSymbols(ctx, a)
	}
	if a.File == "" {
		return Result{Content: "lsp " + a.Operation + ": file is required", IsError: true}, nil
	}
	abs := a.File
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(l.WorkingDir, abs)
	}
	if _, err := os.Stat(abs); err != nil {
		return Result{Content: "lsp " + a.Operation + ": cannot read file: " + err.Error(), IsError: true}, nil
	}

	// document_symbols and diagnostics need the file but no position.
	if a.Operation == "document_symbols" {
		return l.runDocumentSymbols(ctx, abs)
	}
	if a.Operation == "diagnostics" {
		return l.runDiagnostics(ctx, abs)
	}

	// Everything else needs a position. Validate before opening a client.
	if a.Line < 1 || a.Column < 1 {
		return Result{Content: "lsp " + a.Operation + ": line and column are required (1-based)", IsError: true}, nil
	}
	// LSP wire format is 0-based.
	wireLine := a.Line - 1
	wireCol := a.Column - 1

	client, err := l.openAndSeed(ctx, abs)
	if err != nil {
		return unavailableResult(a.Operation, err), nil
	}

	switch a.Operation {
	case "hover":
		hov, err := client.Hover(ctx, abs, wireLine, wireCol)
		if err != nil {
			return Result{Content: fmt.Sprintf("lsp hover error: %v", err), IsError: true}, nil
		}
		if hov == nil || hov.Contents == "" {
			return Result{Content: fmt.Sprintf("hover: no information at %s:%d:%d", relTo(l.WorkingDir, abs), a.Line, a.Column)}, nil
		}
		return Result{Content: hov.Contents}, nil

	case "definition":
		locs, err := client.Definition(ctx, abs, wireLine, wireCol)
		if err != nil {
			return Result{Content: fmt.Sprintf("lsp definition error: %v", err), IsError: true}, nil
		}
		return Result{Content: formatLocations("definition", locs, l.WorkingDir)}, nil

	case "references":
		locs, err := client.References(ctx, abs, wireLine, wireCol)
		if err != nil {
			return Result{Content: fmt.Sprintf("lsp references error: %v", err), IsError: true}, nil
		}
		return Result{Content: formatLocations("references", locs, l.WorkingDir)}, nil

	case "completion":
		items, err := client.Completion(ctx, abs, wireLine, wireCol)
		if err != nil {
			return Result{Content: fmt.Sprintf("lsp completion error: %v", err), IsError: true}, nil
		}
		return Result{Content: formatCompletions(items)}, nil

	case "signature_help":
		help, err := client.SignatureHelp(ctx, abs, wireLine, wireCol)
		if err != nil {
			return Result{Content: fmt.Sprintf("lsp signature_help error: %v", err), IsError: true}, nil
		}
		return Result{Content: formatSignatureHelp(help)}, nil

	case "rename":
		if a.NewName == "" {
			return Result{Content: "lsp rename: new_name is required", IsError: true}, nil
		}
		edit, err := client.Rename(ctx, abs, wireLine, wireCol, a.NewName)
		if err != nil {
			return Result{Content: fmt.Sprintf("lsp rename error: %v", err), IsError: true}, nil
		}
		return Result{Content: formatRename(edit, l.WorkingDir, a.NewName)}, nil
	}

	return Result{Content: "lsp: unknown operation: " + a.Operation, IsError: true}, nil
}

// openAndSeed spawns (lazily) the right LSP client for `file` and sends
// textDocument/didOpen so position-based queries don't fail with "file
// not synced". We read the file from disk; for the scope of this tool we
// don't track unsaved buffers — the agent's edit/apply_patch tools always
// flush to disk before invoking lsp.
func (l *LSP) openAndSeed(ctx context.Context, abs string) (*lspClientWrapper, error) {
	client, err := l.Manager.ClientFor(ctx, abs)
	if err != nil {
		return nil, err
	}
	wrapper := &lspClientWrapper{Client: client}
	// Seed the file contents.
	body, err := os.ReadFile(abs)
	if err != nil {
		return wrapper, fmt.Errorf("read file: %w", err)
	}
	langID := l.Manager.LanguageIDFor(abs)
	// didOpen errors are non-fatal: some servers don't track open
	// documents but still answer queries. We surface only catastrophic
	// errors via the actual operation's response.
	_ = wrapper.openHelper(abs, langID, string(body))
	return wrapper, nil
}

// lspClientWrapper exists so the tool can reach Client.didOpen, which is
// unexported from the lsp package. Rather than exporting it (and thereby
// committing the public API to it), we expose a shim that calls through
// the public methods we already have plus a tool-side helper.
type lspClientWrapper struct {
	*lsp.Client
}

func (w *lspClientWrapper) openHelper(abs, langID, body string) error {
	return w.Client.NotifyDidOpen(abs, langID, body)
}

func (l *LSP) runDocumentSymbols(ctx context.Context, abs string) (Result, error) {
	client, err := l.openAndSeed(ctx, abs)
	if err != nil {
		return unavailableResult("document_symbols", err), nil
	}
	syms, err := client.DocumentSymbols(ctx, abs)
	if err != nil {
		return Result{Content: fmt.Sprintf("lsp document_symbols error: %v", err), IsError: true}, nil
	}
	return Result{Content: formatDocumentSymbols(syms, "")}, nil
}

func (l *LSP) runWorkspaceSymbols(ctx context.Context, a lspArgs) (Result, error) {
	if a.Query == "" {
		return Result{Content: "lsp workspace_symbols: query is required", IsError: true}, nil
	}
	// We have no file, so we can't pick a server by extension. Strategy:
	// run the query against every server already spawned. If none has
	// been spawned the agent gets an actionable hint to call something
	// position-based first (which will warm the right server).
	//
	// This is a simpler approach than opencode's runAll because gil
	// doesn't keep a registry of every spawned server; the manager does
	// that internally. For the same reason we don't currently re-exec
	// across all configured servers — it would mean spawning every
	// language server for every workspace, which is not what the user
	// asked for.
	//
	// In practice the agent calls hover/definition first to orient, and
	// the right server is then warm by the time it asks for workspace
	// symbols.
	clients := l.Manager.ActiveClients()
	if len(clients) == 0 {
		return Result{Content: "workspace_symbols: no LSP server is warm yet. Try `lsp definition` or `lsp hover` on a file first to spawn the right server, then retry workspace_symbols."}, nil
	}
	var all []lsp.SymbolInformation
	for _, c := range clients {
		syms, err := c.WorkspaceSymbols(ctx, a.Query)
		if err != nil {
			continue // best-effort across servers
		}
		all = append(all, syms...)
	}
	return Result{Content: formatWorkspaceSymbols(all, l.WorkingDir)}, nil
}

func (l *LSP) runDiagnostics(ctx context.Context, abs string) (Result, error) {
	client, err := l.openAndSeed(ctx, abs)
	if err != nil {
		return unavailableResult("diagnostics", err), nil
	}
	// Diagnostics are pushed asynchronously by the server. Poll the
	// client's cache for up to ~1.5s; if nothing arrives the server
	// almost certainly has nothing to say and we report "clean".
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if d := client.Diagnostics(abs); len(d) > 0 {
			return Result{Content: formatDiagnostics(d, abs, l.WorkingDir)}, nil
		}
		select {
		case <-ctx.Done():
			return Result{Content: fmt.Sprintf("lsp diagnostics: %v", ctx.Err()), IsError: true}, nil
		case <-time.After(50 * time.Millisecond):
		}
	}
	d := client.Diagnostics(abs)
	return Result{Content: formatDiagnostics(d, abs, l.WorkingDir)}, nil
}

// unavailableResult is the friendly response when the manager refuses to
// spawn a server. We split the error into a single-line headline + one
// line of detail so the aesthetic spec ("dim, single-line meta") is met.
func unavailableResult(op string, err error) Result {
	if errors.Is(err, lsp.ErrNoServer) {
		return Result{Content: fmt.Sprintf("lsp %s: no language server configured for this file type. Fall back to grep/repomap.", op)}
	}
	if errors.Is(err, lsp.ErrServerUnavailable) {
		// The wrapper formatter already includes the install hint.
		return Result{Content: fmt.Sprintf("lsp %s: language server not installed. %s", op, err.Error())}
	}
	return Result{Content: fmt.Sprintf("lsp %s: %v", op, err), IsError: true}
}

// --- formatters -----------------------------------------------------------
//
// Output is plain text. The aesthetic spec governs visible terminal
// surfaces (CLI / TUI); tool output is consumed by the model, so the
// rules we keep are: no emoji, surface-text only, prefer relative paths,
// one fact per line. Compact list headers use the `›` arrow per the
// aesthetic glyph table when listing actionable items.

func formatLocations(label string, locs []lsp.Location, root string) string {
	if len(locs) == 0 {
		return fmt.Sprintf("%s: no results", label)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d result(s)\n", label, len(locs))
	for _, loc := range locs {
		path, err := uriToFilePath(loc.URI)
		if err != nil {
			path = loc.URI
		}
		fmt.Fprintf(&b, "  › %s:%d:%d\n", relTo(root, path), loc.Range.Start.Line+1, loc.Range.Start.Character+1)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCompletions(items []lsp.CompletionItem) string {
	if len(items) == 0 {
		return "completion: no suggestions"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "completion: %d suggestion(s)\n", len(items))
	for i, it := range items {
		if i >= 50 {
			fmt.Fprintf(&b, "  … %d more truncated\n", len(items)-50)
			break
		}
		if it.Detail != "" {
			fmt.Fprintf(&b, "  › %s — %s\n", it.Label, it.Detail)
		} else {
			fmt.Fprintf(&b, "  › %s\n", it.Label)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSignatureHelp(help *lsp.SignatureHelp) string {
	if help == nil || len(help.Signatures) == 0 {
		return "signature_help: not at a callable"
	}
	var b strings.Builder
	for i, sig := range help.Signatures {
		marker := "  "
		if i == help.ActiveSignature {
			marker = "› "
		}
		fmt.Fprintf(&b, "%s%s\n", marker, sig.Label)
		if sig.Documentation != "" {
			fmt.Fprintf(&b, "    %s\n", sig.Documentation)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatDocumentSymbols(syms []lsp.DocumentSymbol, indent string) string {
	if len(syms) == 0 {
		return "document_symbols: no symbols"
	}
	var b strings.Builder
	if indent == "" {
		fmt.Fprintf(&b, "document_symbols: %d top-level symbol(s)\n", len(syms))
	}
	for _, s := range syms {
		fmt.Fprintf(&b, "%s  › %s (%s) at %d:%d\n", indent, s.Name, symbolKindName(s.Kind), s.Range.Start.Line+1, s.Range.Start.Character+1)
		if len(s.Children) > 0 {
			b.WriteString(formatDocumentSymbols(s.Children, indent+"  "))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatWorkspaceSymbols(syms []lsp.SymbolInformation, root string) string {
	if len(syms) == 0 {
		return "workspace_symbols: no matches"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "workspace_symbols: %d match(es)\n", len(syms))
	for i, s := range syms {
		if i >= 50 {
			fmt.Fprintf(&b, "  … %d more truncated\n", len(syms)-50)
			break
		}
		path, err := uriToFilePath(s.Location.URI)
		if err != nil {
			path = s.Location.URI
		}
		fmt.Fprintf(&b, "  › %s (%s) %s:%d\n",
			s.Name, symbolKindName(s.Kind), relTo(root, path), s.Location.Range.Start.Line+1)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatRename(edit *lsp.WorkspaceEdit, root string, newName string) string {
	if edit == nil || len(edit.Changes) == 0 {
		return "rename: server declined the rename (cursor likely not on a renameable symbol)"
	}
	var b strings.Builder
	totalEdits := 0
	for _, edits := range edit.Changes {
		totalEdits += len(edits)
	}
	fmt.Fprintf(&b, "rename → %s: %d edit(s) across %d file(s)\n", newName, totalEdits, len(edit.Changes))
	fmt.Fprintf(&b, "(server returned the WorkspaceEdit; apply via the edit / apply_patch tool)\n\n")
	for uri, edits := range edit.Changes {
		path, err := uriToFilePath(uri)
		if err != nil {
			path = uri
		}
		fmt.Fprintf(&b, "%s — %d edit(s)\n", relTo(root, path), len(edits))
		for _, e := range edits {
			fmt.Fprintf(&b, "  › %d:%d-%d:%d → %q\n",
				e.Range.Start.Line+1, e.Range.Start.Character+1,
				e.Range.End.Line+1, e.Range.End.Character+1,
				e.NewText)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatDiagnostics(diags []lsp.Diagnostic, abs, root string) string {
	if len(diags) == 0 {
		return fmt.Sprintf("diagnostics: %s — clean (no issues, or server hasn't published yet)", relTo(root, abs))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diagnostics: %s — %d issue(s)\n", relTo(root, abs), len(diags))
	for _, d := range diags {
		sev := severityName(d.Severity)
		fmt.Fprintf(&b, "  › %d:%d %s: %s",
			d.Range.Start.Line+1, d.Range.Start.Character+1, sev, d.Message)
		if d.Source != "" {
			fmt.Fprintf(&b, " (%s)", d.Source)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func severityName(n int) string {
	switch n {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "note"
	}
}

// symbolKindName converts the LSP SymbolKind enum to a short label. Source:
// https://microsoft.github.io/language-server-protocol/specifications/specification-current/#symbolKind
func symbolKindName(k int) string {
	switch k {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "func"
	case 13:
		return "var"
	case 14:
		return "const"
	case 23:
		return "struct"
	case 26:
		return "typeparam"
	default:
		return "sym"
	}
}

func relTo(root, abs string) string {
	if root == "" {
		return abs
	}
	if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return abs
}

func uriToFilePath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI: %s", uri)
	}
	return u.Path, nil
}

