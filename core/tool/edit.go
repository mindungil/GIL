package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mindungil/gil/core/edit"
)

// Edit is the agent-callable tool that applies SEARCH/REPLACE blocks parsed
// from a textual DSL. Each block targets one file; blocks for different
// files apply in document order. On a miss, the tool returns the
// find_similar_lines hint so the LLM can correct itself.
type Edit struct {
	WorkingDir string
	Engine     *edit.MatchEngine // optional; nil → defaults
}

const editSchema = `{
  "type":"object",
  "properties":{
    "blocks":{"type":"string","description":"SEARCH/REPLACE blocks in Aider's DSL format"}
  },
  "required":["blocks"]
}`

func (e *Edit) Name() string { return "edit" }

func (e *Edit) Description() string {
	return "Apply one or more SEARCH/REPLACE blocks to files in the workspace. Use this for surgical edits — much safer than write_file which overwrites the whole file. Format: '<path>\\n<<<<<<< SEARCH\\n<old code>\\n=======\\n<new code>\\n>>>>>>> REPLACE\\n'. The filename goes on its own line BEFORE the SEARCH marker; for codex compatibility a 'path: <filename>' prefix is also accepted (the 'path:' part is stripped). Multiple blocks for the same or different files are allowed; consecutive blocks for the same file may omit the path."
}

func (e *Edit) Schema() json.RawMessage { return json.RawMessage(editSchema) }

func (e *Edit) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Blocks string `json:"blocks"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("edit unmarshal: %w", err)
	}
	if args.Blocks == "" {
		return Result{Content: "blocks is empty", IsError: true}, nil
	}

	blocks, parseErr := edit.Parse(args.Blocks)
	if parseErr != nil {
		// Even on parse error, blocks contains successfully-parsed prefix
		// (per the edit.Parse contract). We still try to apply those.
		if len(blocks) == 0 {
			return Result{Content: "edit parse error: " + parseErr.Error(), IsError: true}, nil
		}
		// Fall through to apply the parsed prefix; report the parse error at the end.
	}

	eng := e.Engine
	if eng == nil {
		eng = &edit.MatchEngine{}
	}

	var summary strings.Builder
	var anyErr bool
	var nApplied, nFailed int

	for i, b := range blocks {
		path := b.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.WorkingDir, path)
		}
		original, rerr := os.ReadFile(path)
		if rerr != nil {
			anyErr = true
			nFailed++
			// Self-correcting hint: if the parsed filename still looks like
			// a stray label/prefix (contains ": " or starts with "path:")
			// we likely picked up garbage that should never have been a
			// filename — tell the agent how to fix the format.
			hint := ""
			if strings.Contains(b.File, ": ") || strings.HasPrefix(strings.ToLower(b.File), "path:") {
				hint = " — the parsed filename is '" + b.File + "' which looks like a label, not a path. Write the filename on its own line BEFORE '<<<<<<< SEARCH', e.g.:\n  cli/internal/cmd/status_render.go\n  <<<<<<< SEARCH\n  ..."
			}
			fmt.Fprintf(&summary, "[block %d] %s: read failed: %v%s\n", i+1, b.File, rerr, hint)
			continue
		}
		updated, mt, merr := eng.Replace(string(original), b.Search, b.Replace)
		if errors.Is(merr, edit.ErrNoMatch) {
			anyErr = true
			nFailed++
			hint := edit.FindSimilar(b.Search, string(original), 0.6)
			if hint != "" {
				fmt.Fprintf(&summary, "[block %d] %s: SEARCH not found. Did you mean:\n```\n%s\n```\n", i+1, b.File, hint)
			} else {
				fmt.Fprintf(&summary, "[block %d] %s: SEARCH not found and no similar chunk above threshold.\n", i+1, b.File)
			}
			continue
		}
		if merr != nil {
			anyErr = true
			nFailed++
			fmt.Fprintf(&summary, "[block %d] %s: replace error: %v\n", i+1, b.File, merr)
			continue
		}
		if werr := os.WriteFile(path, []byte(updated), 0o644); werr != nil {
			anyErr = true
			nFailed++
			fmt.Fprintf(&summary, "[block %d] %s: write failed: %v\n", i+1, b.File, werr)
			continue
		}
		nApplied++
		fmt.Fprintf(&summary, "[block %d] %s: applied (tier=%s)\n", i+1, b.File, mt.Tier.String())
	}

	if parseErr != nil {
		fmt.Fprintf(&summary, "\nNOTE: parse error after %d successfully parsed block(s): %v\n", len(blocks), parseErr)
		anyErr = true
	}
	summary.WriteString(fmt.Sprintf("\n%d applied, %d failed.", nApplied, nFailed))
	return Result{Content: summary.String(), IsError: anyErr}, nil
}
