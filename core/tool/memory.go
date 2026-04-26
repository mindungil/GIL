package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jedutools/gil/core/memory"
)

// MemoryUpdate is the agent-callable tool that writes to the memory bank.
//
// Args:
//
//	{ "file": "progress",                          // short name; one of memory.AllFiles
//	  "content": "...",                            // the text to store/append
//	  "section": "Done" (optional),                // when set, AppendSection
//	  "replace": false (optional, default false) } // true → Write (full overwrite)
//
// Behavior:
//   - replace=true            → Bank.Write(file, content)
//   - section!="", replace=false → Bank.AppendSection(file, section, content)
//   - section=="", replace=false → Bank.Append(file, content)
type MemoryUpdate struct {
	Bank *memory.Bank
}

const memoryUpdateSchema = `{
  "type":"object",
  "properties":{
    "file":{"type":"string","description":"short bank filename: projectbrief, productContext, activeContext, systemPatterns, techContext, progress"},
    "content":{"type":"string","description":"text to store"},
    "section":{"type":"string","description":"optional markdown section heading; when set, content is appended under '## <section>'"},
    "replace":{"type":"boolean","description":"when true, overwrites the entire file"}
  },
  "required":["file","content"]
}`

func (m *MemoryUpdate) Name() string { return "memory_update" }
func (m *MemoryUpdate) Description() string {
	return "Update a memory bank file. Use 'replace':true to overwrite, 'section':\"Done\" to append under a heading, or omit both to append to the file end."
}
func (m *MemoryUpdate) Schema() json.RawMessage { return json.RawMessage(memoryUpdateSchema) }

func (m *MemoryUpdate) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		File    string `json:"file"`
		Content string `json:"content"`
		Section string `json:"section"`
		Replace bool   `json:"replace"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("memory_update unmarshal: %w", err)
	}
	if args.File == "" {
		return Result{Content: "file is required", IsError: true}, nil
	}
	if m.Bank == nil {
		return Result{Content: "memory bank not configured for this run", IsError: true}, nil
	}

	var err error
	var op string
	switch {
	case args.Replace:
		op = "replaced"
		err = m.Bank.Write(args.File, args.Content)
	case args.Section != "":
		op = fmt.Sprintf("appended to ## %s in", args.Section)
		err = m.Bank.AppendSection(args.File, args.Section, args.Content)
	default:
		op = "appended to"
		err = m.Bank.Append(args.File, args.Content)
	}
	if err != nil {
		msg := err.Error()
		if errors.Is(err, memory.ErrUnknownFile) {
			return Result{Content: "unknown memory file: " + args.File + " (valid: projectbrief, productContext, activeContext, systemPatterns, techContext, progress)", IsError: true}, nil
		}
		return Result{Content: "memory_update failed: " + msg, IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("%s %s", op, normalizeNameForMsg(args.File))}, nil
}

// MemoryLoad reads a single memory bank file.
//
// Args: { "file": "techContext" }
type MemoryLoad struct {
	Bank *memory.Bank
}

const memoryLoadSchema = `{
  "type":"object",
  "properties":{
    "file":{"type":"string","description":"short bank filename: projectbrief, productContext, activeContext, systemPatterns, techContext, progress"}
  },
  "required":["file"]
}`

func (m *MemoryLoad) Name() string { return "memory_load" }
func (m *MemoryLoad) Description() string {
	return "Read the current contents of a memory bank file."
}
func (m *MemoryLoad) Schema() json.RawMessage { return json.RawMessage(memoryLoadSchema) }

func (m *MemoryLoad) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("memory_load unmarshal: %w", err)
	}
	if args.File == "" {
		return Result{Content: "file is required", IsError: true}, nil
	}
	if m.Bank == nil {
		return Result{Content: "memory bank not configured for this run", IsError: true}, nil
	}
	content, err := m.Bank.Read(args.File)
	if err != nil {
		if errors.Is(err, memory.ErrUnknownFile) {
			return Result{Content: "unknown memory file: " + args.File, IsError: true}, nil
		}
		if errors.Is(err, memory.ErrNotFound) {
			return Result{Content: "(file does not exist yet)", IsError: false}, nil
		}
		return Result{Content: "memory_load failed: " + err.Error(), IsError: true}, nil
	}
	return Result{Content: content}, nil
}

// normalizeNameForMsg returns the canonical filename for human-readable
// success messages. Strictly cosmetic.
func normalizeNameForMsg(s string) string {
	// Bank already accepts short names; for the message, suffix .md if missing.
	if len(s) > 3 && s[len(s)-3:] == ".md" {
		return s
	}
	return s + ".md"
}
