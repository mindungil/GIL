package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mindungil/gil/core/web"
)

// WebFetch is the agent-callable tool that GETs a URL and returns its
// content as markdown. Lifted from opencode's webfetch and aider's
// /web command, but stripped to a stdlib-only HTML→markdown walker so
// gil ships no extra runtime deps. Permissions: read-only verb;
// allowed by default at FULL and ASK_DESTRUCTIVE_ONLY autonomy levels.
//
// Args (JSON): { url: string, max_bytes?: int }
//
// Output is a single text block with a header (URL/Title/Status/Size)
// followed by the markdown body. Non-2xx statuses are NOT errors at
// the tool layer — the agent sees the status code and decides what
// to do. The 2 MiB default cap is a hard ceiling against runaway
// docs pages or rogue servers; agents can lower it but not raise
// past the package-level web.DefaultMaxBytes.
type WebFetch struct {
	// MaxBytesCeiling, when > 0, caps any agent-supplied max_bytes argument.
	// Defaults to web.DefaultMaxBytes when zero. Tests can lower it.
	MaxBytesCeiling int64
}

const webFetchSchema = `{
  "type":"object",
  "properties":{
    "url":{
      "type":"string",
      "description":"Full URL including http:// or https:// scheme"
    },
    "max_bytes":{
      "type":"integer",
      "description":"Optional cap; default 2097152 (2 MiB). Cannot exceed the package ceiling."
    }
  },
  "required":["url"]
}`

func (w *WebFetch) Name() string { return "web_fetch" }

func (w *WebFetch) Description() string {
	return "Fetch a URL and return its content as markdown. Use for library docs, GitHub issues, RFCs, blog posts. Returns title + markdown body. Body is truncated to ~2 MiB."
}

func (w *WebFetch) Schema() json.RawMessage { return json.RawMessage(webFetchSchema) }

func (w *WebFetch) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		URL      string `json:"url"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return Result{}, fmt.Errorf("web_fetch unmarshal: %w", err)
		}
	}
	if args.URL == "" {
		return Result{Content: "url is empty", IsError: true}, nil
	}

	ceiling := w.MaxBytesCeiling
	if ceiling <= 0 {
		ceiling = web.DefaultMaxBytes
	}
	maxBytes := args.MaxBytes
	if maxBytes <= 0 || maxBytes > ceiling {
		maxBytes = ceiling
	}

	res, err := web.Fetch(ctx, web.FetchOptions{
		URL:      args.URL,
		MaxBytes: maxBytes,
	})
	if err != nil {
		return Result{Content: "web_fetch: " + err.Error(), IsError: true}, nil
	}

	return Result{Content: formatWebFetchResult(res, maxBytes)}, nil
}

// formatWebFetchResult builds the human/agent-readable output: a short
// header followed by the markdown body. When the response was truncated
// we append a hint so the agent knows to narrow its query.
func formatWebFetchResult(res *web.FetchResult, maxBytes int64) string {
	var sb strings.Builder
	sb.WriteString("URL: ")
	sb.WriteString(res.URL)
	sb.WriteString("\n")
	if res.Title != "" {
		sb.WriteString("Title: ")
		sb.WriteString(res.Title)
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("Status: %d\n", res.StatusCode))
	if res.ContentType != "" {
		sb.WriteString("Content-Type: ")
		sb.WriteString(res.ContentType)
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("Size: %s\n", humanBytes(res.SizeBytes)))
	sb.WriteString("\n")
	sb.WriteString(res.Markdown)
	if res.Truncated {
		if !strings.HasSuffix(res.Markdown, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("\n... [truncated at %s — use a more specific URL or set max_bytes lower]\n", humanBytes(maxBytes)))
	}
	return sb.String()
}

// humanBytes formats a size with the smallest unit ≥ B (no decimals to
// keep the header line predictable).
func humanBytes(n int64) string {
	const (
		KiB int64 = 1024
		MiB       = 1024 * KiB
	)
	switch {
	case n >= MiB:
		return fmt.Sprintf("%d MiB", n/MiB)
	case n >= KiB:
		return fmt.Sprintf("%d KiB", n/KiB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
