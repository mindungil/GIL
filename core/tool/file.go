package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile creates or overwrites a file relative to the working directory.
// Parent directories are created automatically (mode 0755).
type WriteFile struct {
	WorkingDir string
}

const writeFileSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"file path relative to the project working directory"},
    "content":{"type":"string","description":"full file content (overwrites if exists)"}
  },
  "required":["path","content"]
}`

// Name implements Tool.
func (w *WriteFile) Name() string { return "write_file" }

// Description implements Tool.
func (w *WriteFile) Description() string {
	return "Create or overwrite a file with the given content. Parent directories are created automatically."
}

// Schema implements Tool.
func (w *WriteFile) Schema() json.RawMessage { return json.RawMessage(writeFileSchema) }

// Run writes the file. Returns IsError=true on filesystem error.
func (w *WriteFile) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("write_file unmarshal: %w", err)
	}
	if args.Path == "" {
		return Result{Content: "path is empty", IsError: true}, nil
	}
	abs := filepath.Join(w.WorkingDir, args.Path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := os.WriteFile(abs, []byte(args.Content), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)}, nil
}

// ReadFile returns the contents of a file relative to the working directory.
// Output is truncated at 16KB to keep LLM context manageable.
type ReadFile struct {
	WorkingDir string
}

const readFileSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"file path relative to the project working directory"}
  },
  "required":["path"]
}`

// Name implements Tool.
func (r *ReadFile) Name() string { return "read_file" }

// Description implements Tool.
func (r *ReadFile) Description() string {
	return "Return the contents of a file. Output is truncated at 16KB."
}

// Schema implements Tool.
func (r *ReadFile) Schema() json.RawMessage { return json.RawMessage(readFileSchema) }

// Run reads the file. Returns IsError=true if the file is missing or unreadable.
func (r *ReadFile) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("read_file unmarshal: %w", err)
	}
	if args.Path == "" {
		return Result{Content: "path is empty", IsError: true}, nil
	}
	abs := filepath.Join(r.WorkingDir, args.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: truncate(string(data), 16384)}, nil
}
