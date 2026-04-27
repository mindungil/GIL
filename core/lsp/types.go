// Package lsp implements a minimal Language Server Protocol client and a
// per-workspace manager that lazily spawns language-server subprocesses on
// first use.
//
// Scope: this package is not a full LSP client. It exposes the nine code-
// intelligence operations the `lsp` agent tool surfaces (hover, definition,
// references, rename, completion, document_symbols, workspace_symbols,
// signature_help, diagnostics) and just enough of the initialize/shutdown
// dance to keep gopls / pyright / typescript-language-server / rust-analyzer
// happy. The wire format is JSON-RPC 2.0 with `Content-Length:` framing
// over the server's stdin/stdout.
//
// Reference: https://microsoft.github.io/language-server-protocol/
package lsp

import "encoding/json"

// Position is 0-based, matching the LSP wire format. The agent-callable
// tool accepts 1-based coordinates and converts them at the boundary.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open interval [Start, End).
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a (file, range) pair returned by definition/references/etc.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// MarkupContent is one of the shapes textDocument/hover may return inside
// its `contents` field. We accept either a plain string, a MarkupContent,
// or an array of either; Hover() flattens to a single string for the
// agent.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Hover is the simplified hover result the agent sees. We pre-flatten
// MarkupContent / arrays so the tool output is one consistent shape.
type Hover struct {
	Contents string `json:"contents"`
	Range    *Range `json:"range,omitempty"`
}

// CompletionItem is the trimmed shape we surface. The full LSP shape has
// dozens of optional fields; we keep label + detail + kind because that's
// what an agent can act on.
type CompletionItem struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Kind   int    `json:"kind,omitempty"`
}

// DocumentSymbol is the recursive symbol shape returned by
// textDocument/documentSymbol when the server supports the newer protocol.
// Kind is the LSP SymbolKind enum (1=File, 5=Class, 6=Method, 12=Function,
// etc.) — see https://microsoft.github.io/language-server-protocol/specifications/specification-current/#symbolKind.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation is the older flat shape used by workspace/symbol and
// by some servers' documentSymbol response.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
}

// SignatureInformation describes a single overload returned by
// textDocument/signatureHelp.
type SignatureInformation struct {
	Label         string                 `json:"label"`
	Documentation string                 `json:"documentation,omitempty"`
	Parameters    []ParameterInformation `json:"parameters,omitempty"`
}

// ParameterInformation describes a single parameter inside a SignatureInformation.
type ParameterInformation struct {
	Label         string `json:"label"`
	Documentation string `json:"documentation,omitempty"`
}

// SignatureHelp is the response to textDocument/signatureHelp.
type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature,omitempty"`
	ActiveParameter int                    `json:"activeParameter,omitempty"`
}

// Diagnostic is one error/warning published by the server. Severity 1=error,
// 2=warning, 3=info, 4=hint.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// TextEdit is a single edit inside a WorkspaceEdit.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is what textDocument/rename returns: a map of file URI to
// a list of TextEdits the agent can apply (or pass to the edit/apply_patch
// tool).
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes,omitempty"`
}

// rawMessage is the wire shape we read off stdin/stdout. Result and Error
// are kept as raw JSON so each method can decode into its specific type.
type rawMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
