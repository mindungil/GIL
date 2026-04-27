// Package runner — system_prompt.go owns assembly of the per-iteration
// system prompt sent to the LLM. Lives in its own file (separate from
// runner.go) so the diet experiments — schema-compaction, lazy memory,
// per-spec knobs — can evolve without touching the loop wiring.
//
// History context (Phase 19 Track B): self-dogfood measured ~17k tokens
// per turn, dominated by the system prompt. The original buildSystemPrompt
// duplicated tool descriptions in a markdown bullet list AND let the
// provider attach the same descriptions on the tool-use definitions —
// double cost on the first (un-cached) call. AGENTS.md was injected
// verbatim with chatty BEGIN/END delimiters. Memory bank was prepended
// every iteration even on iter 1 when there's nothing useful in it yet.
//
// This file fixes those, and exposes a Breakdown for diagnostic logging
// (GIL_DEBUG_SYSTEM_PROMPT=1).

package runner

import (
	"fmt"
	"os"
	"strings"

	"github.com/mindungil/gil/core/memory"
	"github.com/mindungil/gil/core/tool"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// SystemPromptOptions exposes the knobs the spec's run.system_prompt
// table maps to. Both default false (historical behaviour preserved).
type SystemPromptOptions struct {
	Minimal  bool // drop AGENTS.md / CLAUDE.md project instructions section
	NoMemory bool // never prepend memory bank section
}

// SystemPromptInputs is what assembleSystemPrompt consumes. Bundled into
// a struct so the call site (runner.go) doesn't need a 7-positional
// signature that grows every time we add a section.
type SystemPromptInputs struct {
	Spec                 *gilv1.FrozenSpec
	Tools                []tool.Tool
	Bank                 *memory.Bank
	InstructionsRendered string // already-rendered AGENTS.md / CLAUDE.md / cursor block
	Iteration            int    // 1-indexed; iter==1 skips memory bank (lazy)
	Options              SystemPromptOptions
}

// SectionTokens is one row of the breakdown — section name + estimated
// tokens. We keep the actual rendered string out of the breakdown so the
// debug print stays compact (the prompt itself is multi-KB).
type SectionTokens struct {
	Name   string
	Tokens int
}

// Breakdown is the per-section estimate produced alongside the assembled
// prompt. Total is the sum across Sections and is what the loop charges
// against the input-token budget on the first call (provider returns
// the precise count thereafter).
type Breakdown struct {
	Sections []SectionTokens
	Total    int
}

// assembleSystemPrompt is the one entry point. Returns the full prompt
// plus a per-section breakdown. The breakdown is cheap (string-length
// arithmetic) so we always compute it; printing is gated on the env
// var. Splitting compute from print means tests can assert on Breakdown
// without scraping stdout.
func assembleSystemPrompt(in SystemPromptInputs) (string, Breakdown) {
	var bd Breakdown
	addSection := func(name, body string) string {
		if body == "" {
			return ""
		}
		bd.Sections = append(bd.Sections, SectionTokens{
			Name:   name,
			Tokens: estimateTokens(body),
		})
		return body
	}

	base := addSection("base_instructions", renderBase(in.Spec))
	verifier := addSection("verifier_checks", renderVerifierChecks(in.Spec))
	toolList := addSection("tool_names", renderToolNames(in.Tools))
	instructions := ""
	if !in.Options.Minimal {
		instructions = addSection("agents_md", renderInstructions(in.InstructionsRendered))
	}
	memBank := ""
	// Lazy memory bank: skip on iter 1 (model has nothing yet to need it
	// for) and when the spec's no_memory knob is set. Iter 0 = "called
	// from a test outside Run()"; treat as iter 1+ for back-compat with
	// existing prompt-shape tests that pass no iteration.
	if !in.Options.NoMemory && in.Bank != nil && (in.Iteration == 0 || in.Iteration > 1) {
		section, err := buildMemoryBankSection(in.Bank)
		if err == nil && section != "" {
			memBank = addSection("memory_bank", "\n"+section)
		}
	}

	out := base + verifier + toolList + instructions + memBank
	bd.Total = estimateTokens(out)
	return out, bd
}

// renderBase is the static "you are an autonomous coding agent" header
// plus the goal line. Compacted from the original ~200-token version
// (multi-paragraph strategy bullet list) into a tight 4-line summary;
// the strategy bullets duplicated information already implicit in the
// agentic loop semantics (model sees verifier feedback, model emits tool
// calls, etc.).
func renderBase(spec *gilv1.FrozenSpec) string {
	goal := "(no goal specified)"
	if spec != nil && spec.Goal != nil && spec.Goal.OneLiner != "" {
		goal = spec.Goal.OneLiner
	}
	return fmt.Sprintf(`You are an autonomous coding agent. Make the verification checks pass. Use tools; stop calling tools when you believe the checks will pass. Make reasonable assumptions — the user is not present.

Goal: %s

`, goal)
}

// renderVerifierChecks renders the "Verification checks:" block. Empty
// checks produce a one-line note rather than the original verbose
// fallback paragraph.
func renderVerifierChecks(spec *gilv1.FrozenSpec) string {
	if spec == nil || spec.Verification == nil || len(spec.Verification.Checks) == 0 {
		return "Verification: (no checks — any non-tool response is treated as done)\n\n"
	}
	var sb strings.Builder
	sb.WriteString("Verification checks (all must pass):\n")
	for _, c := range spec.Verification.Checks {
		sb.WriteString("- ")
		sb.WriteString(c.Name)
		sb.WriteString(": `")
		sb.WriteString(c.Command)
		sb.WriteString("`\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// renderToolNames lists tool *names only* — descriptions live on the
// tool-use definitions the provider attaches separately, and duplicating
// them in the system prompt was the single biggest waste in the old
// assembly. The list still serves a purpose: it gives the model a quick
// "these are the verbs available" overview without forcing it to
// re-read every input_schema.
//
// One-line "Tools: bash, edit, ..." instead of a per-tool bullet
// stanza saves ~30-50 tokens per tool, ~600+ tokens for the full 18-tool
// surface.
func renderToolNames(tools []tool.Tool) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return "Tools: " + strings.Join(names, ", ") + " (call by name; full schemas attached separately)\n\n"
}

// renderInstructions takes the AGENTS.md / CLAUDE.md / cursor-rules
// block produced by core/instructions.Render and wraps it in a header.
// Empty input returns empty (caller controls whether to skip entirely
// based on Options.Minimal).
func renderInstructions(rendered string) string {
	if rendered == "" {
		return ""
	}
	return "## Project Instructions\n\nDurable conventions discovered from AGENTS.md / CLAUDE.md / .cursor/rules. Treat as user-supplied persona signals.\n\n" + rendered + "\n\n"
}

// estimateTokens uses the same 4-chars-per-token heuristic as
// core/compact.estimateTokens. We keep our own copy here rather than
// importing core/compact to avoid an import cycle (compact imports
// runner symbols transitively in some test helpers).
func estimateTokens(s string) int {
	return len(s) / 4
}

// MeasureSystemPrompt is an exported facade around assembleSystemPrompt
// for the measure_diet utility (and any future external benchmark).
// Production code uses the unexported assembler directly; this exists
// only so the comparison binary in measure_diet/ — which lives outside
// the runner package — can call in.
func MeasureSystemPrompt(spec *gilv1.FrozenSpec, tools []tool.Tool, bank *memory.Bank, instructions string, iter int, opts SystemPromptOptions) (string, Breakdown) {
	return assembleSystemPrompt(SystemPromptInputs{
		Spec:                 spec,
		Tools:                tools,
		Bank:                 bank,
		InstructionsRendered: instructions,
		Iteration:            iter,
		Options:              opts,
	})
}

// debugLogBreakdown prints the breakdown to stderr when
// GIL_DEBUG_SYSTEM_PROMPT=1. Called once per Run() (first iteration
// only) — the per-iteration cost stays the same after iter 1, so
// re-printing would just be noise.
//
// Format mirrors the spec in the issue:
//
//	=== System prompt breakdown (estimate) ===
//	  base_instructions:    412 tokens
//	  ...
//	  TOTAL:             11,509 tokens
//
// We pad numbers right-aligned and section names left-aligned with a
// fixed column so the columns line up regardless of section name
// length. Numbers use comma grouping for readability at 5+ digits.
func debugLogBreakdown(bd Breakdown) {
	if os.Getenv("GIL_DEBUG_SYSTEM_PROMPT") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "=== System prompt breakdown (estimate) ===")
	maxName := len("TOTAL")
	for _, s := range bd.Sections {
		if len(s.Name) > maxName {
			maxName = len(s.Name)
		}
	}
	for _, s := range bd.Sections {
		fmt.Fprintf(os.Stderr, "  %-*s  %s tokens\n", maxName, s.Name+":", commaGroup(s.Tokens))
	}
	fmt.Fprintf(os.Stderr, "  %-*s  %s tokens\n", maxName, "TOTAL:", commaGroup(bd.Total))
}

// commaGroup is the same logic strconv would give us via locale-aware
// printing, except Go's stdlib doesn't ship it. Inlined to keep the
// debug-print self-contained.
func commaGroup(n int) string {
	s := fmt.Sprintf("%d", n)
	// Insert commas every three digits from the right. Negative numbers
	// don't appear in token counts but we handle the sign defensively
	// anyway so a future caller can't trip on it.
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	// Right-align to 8 chars so the printed columns stay tidy when
	// totals span 3 to 6 digits across runs.
	return fmt.Sprintf("%8s", s)
}
