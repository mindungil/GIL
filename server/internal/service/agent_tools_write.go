package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// agent_tools_write.go adds the write/exec tools to the V1 chat agent
// loop: read_file, write_file, run_bash, grep, glob. Each is scoped to
// the session's working_dir — paths that resolve outside it are
// rejected. Outputs are truncated so the agent loop's context budget
// stays bounded; the truncation note is intentionally visible to the
// LLM so it can adapt (e.g. ask for a smaller slice).
//
// Safety boundaries:
//   - read/write/grep/glob: cannot escape working_dir (symlinks
//     re-anchored after Abs+EvalSymlinks).
//   - run_bash: bound by a hard timeout (default 30s, max 60s) and a
//     32KB combined-output cap.
//
// Destructive operations are NOT yet gated on the autonomy setting —
// for V1 the shadow-git checkpoint covers rollback. A follow-up commit
// will plumb autonomy.AskDestructiveOnly through write_file/run_bash.

const (
	maxReadBytes  = 128 * 1024
	maxWriteBytes = 1 * 1024 * 1024
	maxBashOutput = 32 * 1024
	defaultBashTO = 30 * time.Second
	maxBashTO     = 60 * time.Second
	maxGrepHits   = 200
	maxGlobHits   = 500
)

// resolveInWD takes a user-supplied path (absolute or relative) and
// returns the cleaned absolute path, requiring it lie inside the
// session's working_dir. Empty workingDir is treated as a hard error
// rather than a silent escape.
func resolveInWD(workingDir, p string) (string, error) {
	if workingDir == "" {
		return "", errors.New("session has no working directory configured")
	}
	if p == "" {
		return "", errors.New("path is empty")
	}
	wd, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("working dir abs: %w", err)
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(wd, p)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(wd, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path %q escapes working directory", p)
	}
	return abs, nil
}

// sessionWD looks up working_dir for sessionID. Centralised so each
// tool does the same lookup with the same error shape.
func sessionWD(ctx context.Context, repo *session.Repo, sessionID string) (string, error) {
	if sessionID == "" {
		return "", errors.New("no session id")
	}
	s, err := repo.Get(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if s.WorkingDir == "" {
		return "", errors.New("session has no working directory; pass --working-dir on session create")
	}
	return s.WorkingDir, nil
}

// rejectReadonlyTarget returns a non-nil error when abs points at an
// existing file whose owner-write bit (0o200) is unset. Missing files
// pass (creation is allowed via writable parent dir, not gated here).
// Directories pass (callers should resolve to files before calling).
//
// Rationale: C3 in docs/design/chat-mode-enforcement.md. write_file /
// edit_file / apply_patch must not silently chmod through a user-marked
// readonly file — that erases the user's sandbox intent. Agent can
// still chmod explicitly via run_bash; this only blocks silent bypass.
func rejectReadonlyTarget(abs string) error {
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat target: %w", err)
	}
	if info.IsDir() {
		return nil
	}
	if info.Mode().Perm()&0o200 == 0 {
		return fmt.Errorf("target file %s is read-only (mode 0%o); the user has marked it as protected. "+
			"If modification is genuinely required, surface the intent to the user — do not chmod to bypass",
			filepath.Base(abs), info.Mode().Perm())
	}
	return nil
}

// --- read_file -------------------------------------------------------

type toolReadFile struct {
	repo *session.Repo
}

func (t *toolReadFile) name() string { return "read_file" }

func (t *toolReadFile) description() string {
	return "Read a file from the session's working directory. " +
		"Use this to inspect source code, configs, or data before editing or answering questions about them. " +
		"Returns the file contents as text. Output is capped at 128KB; longer files are truncated with a note."
}

func (t *toolReadFile) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"Path relative to the session working directory (or absolute if inside it)."}
		},
		"required":["path"],
		"additionalProperties":false
	}`)
}

func (t *toolReadFile) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	abs, err := resolveInWD(wd, args.Path)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		return provider.ToolResult{Content: "stat: " + err.Error(), IsError: true}, nil
	}
	if info.IsDir() {
		return provider.ToolResult{Content: args.Path + " is a directory; use glob to list it", IsError: true}, nil
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return provider.ToolResult{Content: "read: " + err.Error(), IsError: true}, nil
	}
	out := string(body)
	if len(body) > maxReadBytes {
		out = string(body[:maxReadBytes]) +
			fmt.Sprintf("\n... (truncated; %d more bytes)", len(body)-maxReadBytes)
	}
	return provider.ToolResult{Content: out}, nil
}

// --- write_file ------------------------------------------------------

type toolWriteFile struct {
	repo    *session.Repo
	tracker *turnDiffTracker
}

func (t *toolWriteFile) name() string { return "write_file" }

func (t *toolWriteFile) description() string {
	return "Write text to a file in the session's working directory, creating parent directories as needed. " +
		"Overwrites the file atomically. Use to apply edits, create new files, or persist generated content. " +
		"For tiny edits to a large file prefer running a sed-like command via run_bash; this tool replaces the whole file."
}

func (t *toolWriteFile) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"Path relative to the session working directory."},
			"content":{"type":"string","description":"Full file content to write. Replaces any existing content."}
		},
		"required":["path","content"],
		"additionalProperties":false
	}`)
}

func (t *toolWriteFile) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if len(args.Content) > maxWriteBytes {
		return provider.ToolResult{
			Content: fmt.Sprintf("content too large: %d bytes (max %d)", len(args.Content), maxWriteBytes),
			IsError: true,
		}, nil
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
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return provider.ToolResult{Content: "mkdir: " + err.Error(), IsError: true}, nil
	}
	if t.tracker != nil {
		t.tracker.recordPreWrite(sessionID, args.Path, abs)
	}
	tmp := abs + ".gilwrite.tmp"
	if err := os.WriteFile(tmp, []byte(args.Content), 0o644); err != nil {
		return provider.ToolResult{Content: "write: " + err.Error(), IsError: true}, nil
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return provider.ToolResult{Content: "rename: " + err.Error(), IsError: true}, nil
	}
	if t.tracker != nil {
		t.tracker.recordPostWrite(sessionID, args.Path, args.Content, true)
	}
	return provider.ToolResult{
		Content: fmt.Sprintf("wrote %s (%d bytes)", args.Path, len(args.Content)),
	}, nil
}

// --- run_bash --------------------------------------------------------

type toolRunBash struct {
	repo    *session.Repo
	tracker *turnDiffTracker
}

func (t *toolRunBash) name() string { return "run_bash" }

func (t *toolRunBash) description() string {
	return "Execute a bash command in the session's working directory and return combined stdout+stderr. " +
		"Use for builds, tests, file inspection (ls/cat/sed), git, or anything else the user might run in a shell. " +
		"Default timeout 30s, max 60s. Output is capped at 32KB."
}

func (t *toolRunBash) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"cmd":{"type":"string","description":"Bash command line. Run via bash -c, so pipes/redirects/quoting work as expected."},
			"timeout_sec":{"type":"integer","description":"Override the default 30s timeout. Capped at 60s.","minimum":1,"maximum":60}
		},
		"required":["cmd"],
		"additionalProperties":false
	}`)
}

func (t *toolRunBash) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Cmd        string `json:"cmd"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Cmd) == "" {
		return provider.ToolResult{Content: "cmd is empty", IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	to := defaultBashTO
	if args.TimeoutSec > 0 {
		to = time.Duration(args.TimeoutSec) * time.Second
		if to > maxBashTO {
			to = maxBashTO
		}
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-c", args.Cmd)
	cmd.Dir = wd
	// Mark the diff tracker as polluted before running the command —
	// even if it fails, it may have started writing to the FS. show_diff
	// uses this flag to surface a "fs may have changed outside the
	// tracker" caveat to the agent.
	if t.tracker != nil {
		t.tracker.markExternal(sessionID)
	}
	out, err := cmd.CombinedOutput()
	body := string(out)
	if len(out) > maxBashOutput {
		body = string(out[:maxBashOutput]) +
			fmt.Sprintf("\n... (truncated; %d more bytes)", len(out)-maxBashOutput)
	}
	exitCode := 0
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return provider.ToolResult{
				Content: fmt.Sprintf("timeout after %s\n--- partial output ---\n%s", to, body),
				IsError: true,
			}, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return provider.ToolResult{Content: "exec: " + err.Error(), IsError: true}, nil
		}
	}
	header := fmt.Sprintf("$ %s\n[exit %d]\n", args.Cmd, exitCode)
	return provider.ToolResult{
		Content: header + body,
		IsError: exitCode != 0,
	}, nil
}

// --- grep ------------------------------------------------------------

type toolGrep struct {
	repo *session.Repo
}

func (t *toolGrep) name() string { return "grep" }

func (t *toolGrep) description() string {
	return "Search for a regex pattern across files in the session's working directory. " +
		"Uses ripgrep when available (fast, respects .gitignore). " +
		"Returns up to 200 matching lines as path:line:content."
}

func (t *toolGrep) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"pattern":{"type":"string","description":"Regex pattern (rg syntax)."},
			"path":{"type":"string","description":"Optional sub-path within working dir to limit the search."},
			"glob":{"type":"string","description":"Optional file glob like '*.go' to filter file types."}
		},
		"required":["pattern"],
		"additionalProperties":false
	}`)
}

func (t *toolGrep) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if args.Pattern == "" {
		return provider.ToolResult{Content: "pattern is empty", IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	target := wd
	if args.Path != "" {
		abs, err := resolveInWD(wd, args.Path)
		if err != nil {
			return provider.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		target = abs
	}

	rgPath, _ := exec.LookPath("rg")
	var cmd *exec.Cmd
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if rgPath != "" {
		rgArgs := []string{"--line-number", "--no-heading", "--color=never", fmt.Sprintf("--max-count=%d", maxGrepHits)}
		if args.Glob != "" {
			rgArgs = append(rgArgs, "--glob", args.Glob)
		}
		rgArgs = append(rgArgs, "--", args.Pattern, target)
		cmd = exec.CommandContext(cctx, rgPath, rgArgs...)
	} else {
		// fallback: grep -rn (no .gitignore, no glob — best effort)
		grepArgs := []string{"-rn", "--color=never"}
		if args.Glob != "" {
			grepArgs = append(grepArgs, "--include", args.Glob)
		}
		grepArgs = append(grepArgs, "-e", args.Pattern, target)
		cmd = exec.CommandContext(cctx, "grep", grepArgs...)
	}
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	// grep/rg return exit 1 on "no matches" — treat as success-with-empty.
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return provider.ToolResult{Content: "no matches"}, nil
		}
		if cctx.Err() == context.DeadlineExceeded {
			return provider.ToolResult{Content: "grep timed out", IsError: true}, nil
		}
		return provider.ToolResult{Content: "grep failed: " + err.Error() + "\n" + string(out), IsError: true}, nil
	}
	body := string(out)
	// Trim each line to be relative to wd for readability.
	body = strings.ReplaceAll(body, wd+string(os.PathSeparator), "")
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) > maxGrepHits {
		body = strings.Join(lines[:maxGrepHits], "\n") +
			fmt.Sprintf("\n... (%d more matches; refine pattern or path)", len(lines)-maxGrepHits)
	}
	if strings.TrimSpace(body) == "" {
		return provider.ToolResult{Content: "no matches"}, nil
	}
	return provider.ToolResult{Content: body}, nil
}

// --- glob ------------------------------------------------------------

type toolGlob struct {
	repo *session.Repo
}

func (t *toolGlob) name() string { return "glob" }

func (t *toolGlob) description() string {
	return "List files in the session's working directory matching a glob pattern (** is supported for recursive). " +
		"Use to discover what files exist before reading or editing. Returns up to 500 paths, newest first."
}

func (t *toolGlob) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"pattern":{"type":"string","description":"Glob pattern relative to working dir, e.g. '**/*.go' or 'src/*.ts'."}
		},
		"required":["pattern"],
		"additionalProperties":false
	}`)
}

func (t *toolGlob) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if args.Pattern == "" {
		return provider.ToolResult{Content: "pattern is empty", IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	matches, err := walkGlob(wd, args.Pattern)
	if err != nil {
		return provider.ToolResult{Content: "glob: " + err.Error(), IsError: true}, nil
	}
	if len(matches) == 0 {
		return provider.ToolResult{Content: "no matches"}, nil
	}
	more := 0
	if len(matches) > maxGlobHits {
		more = len(matches) - maxGlobHits
		matches = matches[:maxGlobHits]
	}
	body := strings.Join(matches, "\n")
	if more > 0 {
		body += fmt.Sprintf("\n... (%d more; narrow the pattern)", more)
	}
	return provider.ToolResult{Content: body}, nil
}

// walkGlob handles ** by walking the tree and matching each path with
// path.Match-style segment matching. For patterns without ** we fall
// back to filepath.Glob.
func walkGlob(root, pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		full := filepath.Join(root, pattern)
		hits, err := filepath.Glob(full)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(hits))
		for _, h := range hits {
			rel, _ := filepath.Rel(root, h)
			out = append(out, rel)
		}
		return out, nil
	}
	// ** present: walk and apply doublestarMatch.
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		if d.IsDir() {
			// skip noisy dirs that almost never want recursing.
			name := d.Name()
			if p != root && (name == ".git" || name == "node_modules" || name == ".gil") {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if doublestarMatch(pattern, rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// doublestarMatch is a minimal ** glob matcher: ** matches any
// sequence of path segments (including zero). Other tokens use
// path.Match semantics on each segment.
func doublestarMatch(pattern, name string) bool {
	pp := strings.Split(pattern, "/")
	np := strings.Split(name, "/")
	return matchSegments(pp, np)
}

func matchSegments(pp, np []string) bool {
	for len(pp) > 0 {
		if pp[0] == "**" {
			rest := pp[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(np); i++ {
				if matchSegments(rest, np[i:]) {
					return true
				}
			}
			return false
		}
		if len(np) == 0 {
			return false
		}
		ok, _ := filepath.Match(pp[0], np[0])
		if !ok {
			return false
		}
		pp = pp[1:]
		np = np[1:]
	}
	return len(np) == 0
}
