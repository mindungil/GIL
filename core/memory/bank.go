// Package memory implements the gil Memory Bank: 6 persistent markdown files
// per session that capture cross-compaction state. Inspired by Cline's
// Memory Bank pattern (which prompts the agent to maintain these files);
// gil owns them as first-class storage.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

const (
	FileProjectBrief   = "projectbrief.md"
	FileProductContext = "productContext.md"
	FileActiveContext  = "activeContext.md"
	FileSystemPatterns = "systemPatterns.md"
	FileTechContext    = "techContext.md"
	FileProgress       = "progress.md"
)

// AllFiles is the canonical ordered list of bank files.
var AllFiles = []string{
	FileProjectBrief,
	FileProductContext,
	FileActiveContext,
	FileSystemPatterns,
	FileTechContext,
	FileProgress,
}

// ErrUnknownFile is returned for filenames not in AllFiles.
var ErrUnknownFile = errors.New("unknown memory bank file")

// ErrNotFound is returned by Read when the file does not exist on disk.
var ErrNotFound = errors.New("memory bank file not found")

// Bank manages the 6 memory bank markdown files for a session.
type Bank struct {
	Dir string // typically <sessionDir>/memory
}

// New creates a new Bank rooted at dir.
func New(dir string) *Bank {
	return &Bank{Dir: dir}
}

// titleFor returns a human-readable title for a bank file.
func titleFor(name string) string {
	switch name {
	case FileProjectBrief:
		return "Project Brief"
	case FileProductContext:
		return "Product Context"
	case FileActiveContext:
		return "Active Context"
	case FileSystemPatterns:
		return "System Patterns"
	case FileTechContext:
		return "Tech Context"
	case FileProgress:
		return "Progress"
	default:
		return name
	}
}

// stubContent returns the initial stub content for a bank file.
func stubContent(name string) string {
	return fmt.Sprintf("# %s\n\n_(stub — not yet populated)_\n", titleFor(name))
}

// normalizeName accepts both short names (e.g., "progress") and full names
// (e.g., "progress.md"), returning the canonical full filename or an error.
func normalizeName(name string) (string, error) {
	// Try exact match first.
	for _, f := range AllFiles {
		if f == name {
			return f, nil
		}
	}
	// Try adding .md suffix.
	withMd := name + ".md"
	for _, f := range AllFiles {
		if f == withMd {
			return f, nil
		}
	}
	return "", ErrUnknownFile
}

// validFile returns whether name is a known bank filename (full or short).
func validFile(name string) bool {
	_, err := normalizeName(name)
	return err == nil
}

// Init creates Dir and writes initial stubs for any missing files. Existing
// files are left untouched. Each stub has a single H1 heading and a
// "(stub - not yet populated)" placeholder line, used by InitFromSpec to
// detect unmodified files.
func (b *Bank) Init() error {
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return fmt.Errorf("memory.Bank.Init: mkdir %s: %w", b.Dir, err)
	}
	for _, f := range AllFiles {
		path := filepath.Join(b.Dir, f)
		if _, err := os.Stat(path); err == nil {
			// File already exists — leave it untouched.
			continue
		}
		if err := os.WriteFile(path, []byte(stubContent(f)), 0o644); err != nil {
			return fmt.Errorf("memory.Bank.Init: write %s: %w", f, err)
		}
	}
	return nil
}

// InitFromSpec populates files from a frozen spec. Only overwrites files
// whose current contents match the stub layout (i.e., the agent hasn't
// touched them yet). Files NOT in AllFiles or files with custom content
// are left alone. Returns the list of filenames actually populated.
//
// Mapping (best-effort; missing spec fields → empty):
//
//	projectbrief.md   ← spec.Goal.OneLiner + spec.Goal.SuccessCriteriaNatural
//	productContext.md ← spec.Goal.Why (if exists; otherwise "(no productContext provided)")
//	activeContext.md  ← "Initialized from frozen spec; no activity yet."
//	systemPatterns.md ← spec.Constraints.TechStack joined as bullets, or "(none)"
//	techContext.md    ← spec.Constraints.TechStack + spec.Workspace info
//	progress.md       ← "## Done\n- (none)\n## In Progress\n- (none)\n## Blocked\n- (none)\n"
func (b *Bank) InitFromSpec(spec *gilv1.FrozenSpec) ([]string, error) {
	contents := specContents(spec)
	var populated []string

	for _, f := range AllFiles {
		content, ok := contents[f]
		if !ok {
			continue
		}

		path := filepath.Join(b.Dir, f)
		existing, err := os.ReadFile(path)
		if err != nil {
			// File doesn't exist — write it.
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("memory.Bank.InitFromSpec: read %s: %w", f, err)
			}
			if err := os.MkdirAll(b.Dir, 0o755); err != nil {
				return nil, fmt.Errorf("memory.Bank.InitFromSpec: mkdir: %w", err)
			}
		} else {
			// File exists — only overwrite if it still has stub content.
			if string(existing) != stubContent(f) {
				continue
			}
		}

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("memory.Bank.InitFromSpec: write %s: %w", f, err)
		}
		populated = append(populated, f)
	}

	return populated, nil
}

// specContents builds the content map from a FrozenSpec.
func specContents(spec *gilv1.FrozenSpec) map[string]string {
	contents := make(map[string]string)

	// projectbrief.md
	{
		var sb strings.Builder
		sb.WriteString("# Project Brief\n\n")
		if spec.GetGoal() != nil && spec.GetGoal().GetOneLiner() != "" {
			sb.WriteString("## Goal\n\n")
			sb.WriteString(spec.GetGoal().GetOneLiner())
			sb.WriteString("\n")
		}
		if spec.GetGoal() != nil && len(spec.GetGoal().GetSuccessCriteriaNatural()) > 0 {
			sb.WriteString("\n## Success Criteria\n\n")
			for _, c := range spec.GetGoal().GetSuccessCriteriaNatural() {
				sb.WriteString("- ")
				sb.WriteString(c)
				sb.WriteString("\n")
			}
		}
		contents[FileProjectBrief] = sb.String()
	}

	// productContext.md
	{
		var sb strings.Builder
		sb.WriteString("# Product Context\n\n")
		// Goal.Why is not a field in the proto; use Detailed as a proxy or fallback.
		if spec.GetGoal() != nil && spec.GetGoal().GetDetailed() != "" {
			sb.WriteString(spec.GetGoal().GetDetailed())
			sb.WriteString("\n")
		} else {
			sb.WriteString("(no productContext provided)\n")
		}
		contents[FileProductContext] = sb.String()
	}

	// activeContext.md
	contents[FileActiveContext] = "# Active Context\n\nInitialized from frozen spec; no activity yet.\n"

	// systemPatterns.md
	{
		var sb strings.Builder
		sb.WriteString("# System Patterns\n\n")
		if spec.GetConstraints() != nil && len(spec.GetConstraints().GetTechStack()) > 0 {
			for _, t := range spec.GetConstraints().GetTechStack() {
				sb.WriteString("- ")
				sb.WriteString(t)
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString("(none)\n")
		}
		contents[FileSystemPatterns] = sb.String()
	}

	// techContext.md
	{
		var sb strings.Builder
		sb.WriteString("# Tech Context\n\n")
		if spec.GetConstraints() != nil && len(spec.GetConstraints().GetTechStack()) > 0 {
			sb.WriteString("## Tech Stack\n\n")
			for _, t := range spec.GetConstraints().GetTechStack() {
				sb.WriteString("- ")
				sb.WriteString(t)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
		if spec.GetWorkspace() != nil && spec.GetWorkspace().GetPath() != "" {
			sb.WriteString("## Workspace\n\n")
			sb.WriteString("- Path: ")
			sb.WriteString(spec.GetWorkspace().GetPath())
			sb.WriteString("\n")
		}
		contents[FileTechContext] = sb.String()
	}

	// progress.md
	contents[FileProgress] = "# Progress\n\n## Done\n- (none)\n## In Progress\n- (none)\n## Blocked\n- (none)\n"

	return contents
}

// Read returns file contents. Returns ErrUnknownFile for unknown filenames,
// ErrNotFound when the file doesn't exist on disk.
func (b *Bank) Read(file string) (string, error) {
	canonical, err := normalizeName(file)
	if err != nil {
		return "", ErrUnknownFile
	}
	data, err := os.ReadFile(filepath.Join(b.Dir, canonical))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("memory.Bank.Read %s: %w", canonical, err)
	}
	return string(data), nil
}

// Write replaces the entire file contents. Creates Dir if missing. Returns
// ErrUnknownFile for invalid filenames.
func (b *Bank) Write(file, content string) error {
	canonical, err := normalizeName(file)
	if err != nil {
		return ErrUnknownFile
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return fmt.Errorf("memory.Bank.Write: mkdir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, canonical), []byte(content), 0o644); err != nil {
		return fmt.Errorf("memory.Bank.Write %s: %w", canonical, err)
	}
	return nil
}

// Append appends content to a file (with a leading newline if file already
// has content and doesn't end with one). Creates the file with exactly the
// provided content if it doesn't exist. ErrUnknownFile for invalid names.
func (b *Bank) Append(file, content string) error {
	canonical, err := normalizeName(file)
	if err != nil {
		return ErrUnknownFile
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return fmt.Errorf("memory.Bank.Append: mkdir: %w", err)
	}

	path := filepath.Join(b.Dir, canonical)
	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("memory.Bank.Append: read %s: %w", canonical, err)
		}
		// File doesn't exist — create with content as-is.
		return os.WriteFile(path, []byte(content), 0o644)
	}

	// File exists — append with separator newline if needed.
	newContent := string(existing)
	if len(newContent) > 0 && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	newContent += content

	return os.WriteFile(path, []byte(newContent), 0o644)
}

// AppendSection appends content under a markdown heading "## <section>".
// If the heading exists, content is inserted at the end of that section
// (right before the next "## " or EOF). If the heading doesn't exist,
// it's added at the end of the file with content underneath.
// ErrUnknownFile for invalid names.
func (b *Bank) AppendSection(file, section, content string) error {
	canonical, err := normalizeName(file)
	if err != nil {
		return ErrUnknownFile
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return fmt.Errorf("memory.Bank.AppendSection: mkdir: %w", err)
	}

	path := filepath.Join(b.Dir, canonical)
	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("memory.Bank.AppendSection: read %s: %w", canonical, err)
		}
		// File doesn't exist — create with section + content.
		newContent := fmt.Sprintf("## %s\n%s\n", section, content)
		return os.WriteFile(path, []byte(newContent), 0o644)
	}

	result := appendSection(string(existing), section, content)
	return os.WriteFile(path, []byte(result), 0o644)
}

// appendSection is the pure string manipulation for AppendSection.
func appendSection(fileContent, section, content string) string {
	heading := "## " + section

	lines := strings.Split(fileContent, "\n")

	// Find the heading line index.
	headingIdx := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == heading {
			headingIdx = i
			break
		}
	}

	if headingIdx == -1 {
		// Section not found — append at end.
		trimmed := strings.TrimRight(fileContent, "\n")
		return trimmed + "\n## " + section + "\n" + content + "\n"
	}

	// Find the end of this section: next "## " heading or EOF.
	sectionEnd := len(lines) // default to EOF
	for i := headingIdx + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			sectionEnd = i
			break
		}
	}

	// Find the last non-empty line before sectionEnd (to trim trailing blanks).
	insertIdx := sectionEnd
	for insertIdx > headingIdx+1 && strings.TrimSpace(lines[insertIdx-1]) == "" {
		insertIdx--
	}

	// Insert content lines before insertIdx.
	contentLines := strings.Split(content, "\n")

	newLines := make([]string, 0, len(lines)+len(contentLines)+1)
	newLines = append(newLines, lines[:insertIdx]...)
	newLines = append(newLines, contentLines...)
	newLines = append(newLines, lines[insertIdx:]...)

	return strings.Join(newLines, "\n")
}

// Snapshot returns a map of filename → contents for every existing bank
// file. Missing files are omitted (not included as empty strings).
func (b *Bank) Snapshot() (map[string]string, error) {
	result := make(map[string]string)
	for _, f := range AllFiles {
		data, err := os.ReadFile(filepath.Join(b.Dir, f))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("memory.Bank.Snapshot: read %s: %w", f, err)
		}
		result[f] = string(data)
	}
	return result, nil
}

// EstimateTokens returns the rough total token count of all files
// (4 chars per token heuristic).
func (b *Bank) EstimateTokens() (int, error) {
	total := 0
	for _, f := range AllFiles {
		data, err := os.ReadFile(filepath.Join(b.Dir, f))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, fmt.Errorf("memory.Bank.EstimateTokens: read %s: %w", f, err)
		}
		total += len(data)
	}
	return total / 4, nil
}
