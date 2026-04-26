// Package repomap builds a structured map of a project's symbols (function,
// struct, class, etc.) and their references, used to give the agent a
// budget-bounded overview of the codebase.
//
// IMPLEMENTATION NOTE
// -------------------
// The original design called for tree-sitter, but tree-sitter Go bindings
// require CGO and the rest of gil is CGO-free. We use stdlib go/parser +
// go/ast for Go (most accurate) and regex-based extraction for Python /
// JavaScript / TypeScript (simpler, sufficient for overview purposes).
// Downstream packages (PageRank scoring, token fitter) operate on the
// uniform Symbol/Reference types and don't care about parsing precision.
package repomap

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol represents a named definition (function, type, variable, etc.)
// extracted from a source file.
type Symbol struct {
	Name    string
	Kind    string // "func" | "method" | "struct" | "interface" | "class" | "var" | "const" | "type"
	File    string
	Line    int
	EndLine int
}

// Reference represents a use of an identifier within a source file.
type Reference struct {
	Name string // identifier being referenced
	File string
	Line int
}

// FileSymbols holds the symbols and references extracted from a single file.
type FileSymbols struct {
	File string // path as passed to ParseFile (caller chooses absolute or rel)
	Defs []Symbol
	Refs []Reference
}

// ErrUnsupportedLanguage is returned by ParseFile when the file extension is
// not one of the supported languages.
var ErrUnsupportedLanguage = errors.New("repomap: unsupported language")

// LanguageOf returns a short language name for a path, or "" if unsupported.
func LanguageOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	}
	return ""
}

// ParseFile loads a single file and extracts its symbols + references.
// Language is determined by file extension:
//
//	.go              → Go via stdlib go/parser
//	.py              → Python via regex
//	.js, .jsx        → JavaScript via regex
//	.ts, .tsx        → TypeScript via regex
//
// Returns ErrUnsupportedLanguage for any other extension.
func ParseFile(path string) (*FileSymbols, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("repomap: read %s: %w", path, err)
	}
	switch LanguageOf(path) {
	case "go":
		return parseGo(path, src)
	case "python":
		return parsePython(path, src)
	case "javascript":
		return parseJSorTS(path, src, "javascript")
	case "typescript":
		return parseJSorTS(path, src, "typescript")
	}
	return nil, ErrUnsupportedLanguage
}

// --- Go parser (stdlib go/parser + go/ast) ---

func parseGo(path string, src []byte) (*FileSymbols, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("go parse %s: %w", path, err)
	}
	fs := &FileSymbols{File: path}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := "func"
			if d.Recv != nil {
				kind = "method"
			}
			fs.Defs = append(fs.Defs, Symbol{
				Name:    d.Name.Name,
				Kind:    kind,
				File:    path,
				Line:    fset.Position(d.Pos()).Line,
				EndLine: fset.Position(d.End()).Line,
			})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					fs.Defs = append(fs.Defs, Symbol{
						Name:    s.Name.Name,
						Kind:    kind,
						File:    path,
						Line:    fset.Position(s.Pos()).Line,
						EndLine: fset.Position(s.End()).Line,
					})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						if !ast.IsExported(n.Name) && len(s.Names) > 1 {
							continue
						}
						fs.Defs = append(fs.Defs, Symbol{
							Name:    n.Name,
							Kind:    kind,
							File:    path,
							Line:    fset.Position(n.Pos()).Line,
							EndLine: fset.Position(n.End()).Line,
						})
					}
				}
			}
		}
	}

	// References: walk and collect identifier uses (excluding names that are
	// themselves Defs in this file — we want cross-file pointers).
	defNames := make(map[string]bool, len(fs.Defs))
	for _, d := range fs.Defs {
		defNames[d.Name] = true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			// sel is always *ast.Ident in valid Go, but the spec allows any Ident
			fs.Refs = append(fs.Refs, Reference{
				Name: x.Sel.Name,
				File: path,
				Line: fset.Position(x.Sel.Pos()).Line,
			})
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok {
				if !defNames[id.Name] {
					fs.Refs = append(fs.Refs, Reference{
						Name: id.Name,
						File: path,
						Line: fset.Position(id.Pos()).Line,
					})
				}
			}
		}
		return true
	})
	return fs, nil
}

// --- Python regex parser ---

var (
	pyDefRE   = regexp.MustCompile(`(?m)^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	pyClassRE = regexp.MustCompile(`(?m)^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)`)
	pyCallRE  = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

func parsePython(path string, src []byte) (*FileSymbols, error) {
	fs := &FileSymbols{File: path}
	lines := strings.Split(string(src), "\n")
	for i, line := range lines {
		if m := pyDefRE.FindStringSubmatch(line); m != nil {
			fs.Defs = append(fs.Defs, Symbol{Name: m[1], Kind: "func", File: path, Line: i + 1, EndLine: i + 1})
		}
		if m := pyClassRE.FindStringSubmatch(line); m != nil {
			fs.Defs = append(fs.Defs, Symbol{Name: m[1], Kind: "class", File: path, Line: i + 1, EndLine: i + 1})
		}
	}
	// Refs: every identifier-followed-by-( that isn't a def
	defNames := make(map[string]bool)
	for _, d := range fs.Defs {
		defNames[d.Name] = true
	}
	for i, line := range lines {
		for _, m := range pyCallRE.FindAllStringSubmatch(line, -1) {
			name := m[1]
			if isPyKeyword(name) || defNames[name] {
				continue
			}
			fs.Refs = append(fs.Refs, Reference{Name: name, File: path, Line: i + 1})
		}
	}
	return fs, nil
}

func isPyKeyword(s string) bool {
	switch s {
	case "if", "elif", "else", "for", "while", "def", "class", "return", "yield",
		"import", "from", "as", "with", "try", "except", "finally", "raise",
		"pass", "break", "continue", "global", "nonlocal", "lambda", "assert",
		"del", "in", "is", "not", "and", "or", "True", "False", "None",
		"self", "cls":
		return true
	}
	return false
}

// --- JavaScript / TypeScript regex parser ---

var (
	jsFuncRE  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
	jsClassRE = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	jsConstRE = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s+)?\(.*\)\s*=>`)
	jsTypeRE  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:type|interface)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	jsCallRE  = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
)

func parseJSorTS(path string, src []byte, lang string) (*FileSymbols, error) {
	fs := &FileSymbols{File: path}
	lines := strings.Split(string(src), "\n")
	for i, line := range lines {
		if m := jsFuncRE.FindStringSubmatch(line); m != nil {
			fs.Defs = append(fs.Defs, Symbol{Name: m[1], Kind: "func", File: path, Line: i + 1, EndLine: i + 1})
		}
		if m := jsClassRE.FindStringSubmatch(line); m != nil {
			fs.Defs = append(fs.Defs, Symbol{Name: m[1], Kind: "class", File: path, Line: i + 1, EndLine: i + 1})
		}
		if m := jsConstRE.FindStringSubmatch(line); m != nil {
			fs.Defs = append(fs.Defs, Symbol{Name: m[1], Kind: "func", File: path, Line: i + 1, EndLine: i + 1})
		}
		if lang == "typescript" {
			if m := jsTypeRE.FindStringSubmatch(line); m != nil {
				kind := "type"
				if strings.Contains(line, "interface") {
					kind = "interface"
				}
				fs.Defs = append(fs.Defs, Symbol{Name: m[1], Kind: kind, File: path, Line: i + 1, EndLine: i + 1})
			}
		}
	}
	defNames := make(map[string]bool)
	for _, d := range fs.Defs {
		defNames[d.Name] = true
	}
	for i, line := range lines {
		for _, m := range jsCallRE.FindAllStringSubmatch(line, -1) {
			name := m[1]
			if isJSKeyword(name) || defNames[name] {
				continue
			}
			fs.Refs = append(fs.Refs, Reference{Name: name, File: path, Line: i + 1})
		}
	}
	return fs, nil
}

func isJSKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "do", "switch", "case", "default",
		"break", "continue", "return", "function", "var", "let", "const",
		"class", "new", "this", "super", "typeof", "instanceof", "in", "of",
		"try", "catch", "finally", "throw", "async", "await", "yield",
		"import", "export", "from", "as", "null", "undefined", "true", "false",
		"void", "delete":
		return true
	}
	return false
}
