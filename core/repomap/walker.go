package repomap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// WalkOptions configures the behavior of WalkProject.
type WalkOptions struct {
	MaxFileSize int64    // skip files larger than this; 0 → 256 KB default
	Exclude     []string // additional glob patterns relative to the root (matched per-name)
}

// DefaultExcludeDirs are directory names skipped by default.
var DefaultExcludeDirs = []string{
	".git", ".hg", ".svn",
	"node_modules", "vendor", "__pycache__", ".pytest_cache",
	".venv", "venv", ".tox",
	"build", "dist", "out", "target",
	".next", ".nuxt", ".cache",
	".idea", ".vscode",
}

// WalkProject walks root depth-first, parses every supported file,
// and returns the aggregated symbols. Directories whose Base name is in
// DefaultExcludeDirs are skipped. Files larger than MaxFileSize are skipped.
//
// The returned FileSymbols entries have File set to the path RELATIVE to root
// (so output is portable across machines). Files that fail to parse emit a
// warning slice but do not abort the walk; the warnings are returned alongside
// the symbols so callers can surface them.
//
// Context cancellation is checked at each directory entry — if ctx is done,
// returns whatever was collected so far + ctx.Err().
func WalkProject(ctx context.Context, root string, opts WalkOptions) ([]*FileSymbols, []string, error) {
	maxSize := opts.MaxFileSize
	if maxSize <= 0 {
		maxSize = 256 * 1024
	}
	excludeDirs := make(map[string]bool, len(DefaultExcludeDirs))
	for _, d := range DefaultExcludeDirs {
		excludeDirs[d] = true
	}
	var symbols []*FileSymbols
	var warnings []string

	root = filepath.Clean(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("walk error at %s: %v", path, err))
			return nil // continue
		}
		if d.IsDir() {
			// Always allow root itself
			if path == root {
				return nil
			}
			if excludeDirs[d.Name()] {
				return filepath.SkipDir
			}
			// Apply user excludes (match against directory base name)
			for _, pat := range opts.Exclude {
				ok, _ := filepath.Match(pat, d.Name())
				if ok {
					return filepath.SkipDir
				}
			}
			return nil
		}
		// File path
		if LanguageOf(path) == "" {
			return nil
		}
		for _, pat := range opts.Exclude {
			ok, _ := filepath.Match(pat, d.Name())
			if ok {
				return nil
			}
		}
		info, ierr := d.Info()
		if ierr != nil {
			warnings = append(warnings, fmt.Sprintf("stat %s: %v", path, ierr))
			return nil
		}
		if info.Size() > maxSize {
			warnings = append(warnings, fmt.Sprintf("skipped (size %d > %d): %s", info.Size(), maxSize, path))
			return nil
		}
		fs2, perr := ParseFile(path)
		if perr != nil {
			if errors.Is(perr, ErrUnsupportedLanguage) {
				return nil // shouldn't happen given LanguageOf check, but defensive
			}
			warnings = append(warnings, fmt.Sprintf("parse %s: %v", path, perr))
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr == nil {
			fs2.File = rel
			for i := range fs2.Defs {
				fs2.Defs[i].File = rel
			}
			for i := range fs2.Refs {
				fs2.Refs[i].File = rel
			}
		}
		symbols = append(symbols, fs2)
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return symbols, warnings, err
	}
	return symbols, warnings, ctx.Err()
}
