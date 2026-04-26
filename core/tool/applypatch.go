package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jedutools/gil/core/patch"
)

// ApplyPatch is the agent-callable tool that parses and applies an apply_patch
// DSL string against the workspace. Supports Add File, Delete File, and
// Update File hunks (with optional Move to:). See core/patch for the DSL spec.
type ApplyPatch struct {
	WorkspaceDir string
}

const applyPatchSchema = `{
  "type":"object",
  "properties":{
    "patch":{"type":"string","description":"Patch text in apply_patch DSL: starts with '*** Begin Patch', ends with '*** End Patch'. Hunks: '*** Add File: <path>' followed by '+'-prefixed lines; '*** Delete File: <path>'; '*** Update File: <path>' optionally with '*** Move to: <new>' followed by '@@ <ctx>' chunks of ' '/+/- prefixed lines."}
  },
  "required":["patch"]
}`

func (a *ApplyPatch) Name() string { return "apply_patch" }

func (a *ApplyPatch) Description() string {
	return "Apply a structured multi-file patch in the apply_patch DSL. Use for atomic multi-file edits (add+modify+delete in one call). Each hunk targets one file. For surgical single-block edits within a file, prefer the 'edit' tool."
}

func (a *ApplyPatch) Schema() json.RawMessage { return json.RawMessage(applyPatchSchema) }

func (a *ApplyPatch) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct{ Patch string `json:"patch"` }
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("apply_patch unmarshal: %w", err)
	}
	if args.Patch == "" {
		return Result{Content: "patch is empty", IsError: true}, nil
	}

	p, err := patch.Parse(args.Patch)
	if err != nil {
		return Result{Content: "apply_patch parse error: " + err.Error(), IsError: true}, nil
	}

	applier := &patch.Applier{WorkspaceDir: a.WorkspaceDir}
	results := applier.Apply(p)

	var sb strings.Builder
	var nApplied, nFailed int
	for i, r := range results {
		kind := hunkKindName(r.Hunk.Kind)
		if r.Err != nil {
			nFailed++
			fmt.Fprintf(&sb, "[hunk %d] %s %s: FAILED: %v\n", i+1, kind, r.Hunk.Path, r.Err)
			continue
		}
		nApplied++
		if r.Hunk.Kind == patch.HunkUpdateFile && r.Hunk.MovePath != "" {
			fmt.Fprintf(&sb, "[hunk %d] %s %s → %s: applied\n", i+1, kind, r.Hunk.Path, r.Hunk.MovePath)
		} else {
			fmt.Fprintf(&sb, "[hunk %d] %s %s: applied\n", i+1, kind, r.Hunk.Path)
		}
	}
	sb.WriteString(fmt.Sprintf("\n%d applied, %d failed.", nApplied, nFailed))
	return Result{Content: sb.String(), IsError: nFailed > 0}, nil
}

func hunkKindName(k patch.HunkKind) string {
	switch k {
	case patch.HunkAddFile:
		return "Add"
	case patch.HunkDeleteFile:
		return "Delete"
	case patch.HunkUpdateFile:
		return "Update"
	}
	return "?"
}
