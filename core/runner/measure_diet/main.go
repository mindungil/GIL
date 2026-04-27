// One-off measurement utility — emits before/after token estimates for
// the system prompt diet (Phase 19 Track B). NOT a build target;
// run via `go run ./runner/measure_diet` from core/.
package main

import (
	"fmt"
	"strings"

	"github.com/mindungil/gil/core/memory"
	"github.com/mindungil/gil/core/runner"
	"github.com/mindungil/gil/core/tool"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// oldBuildSystemPrompt replicates the pre-diet assembly verbatim so we
// can emit a fair comparison alongside the new helper.
func oldBuildSystemPrompt(spec *gilv1.FrozenSpec, tools []tool.Tool, instructionsSection, bankSection string) string {
	goal := "(no goal specified)"
	if spec != nil && spec.Goal != nil {
		goal = spec.Goal.OneLiner
	}
	var toolList string
	for _, t := range tools {
		toolList += fmt.Sprintf("- %s: %s\n", t.Name(), t.Description())
	}
	var checkList string
	if spec != nil && spec.Verification != nil {
		for _, c := range spec.Verification.Checks {
			checkList += fmt.Sprintf("- %s: `%s`\n", c.Name, c.Command)
		}
	}
	if checkList == "" {
		checkList = "(no checks defined — any non-tool response will be considered done)\n"
	}
	base := fmt.Sprintf(`You are an autonomous coding agent. Your job is to make all verification checks pass.

Goal: %s

Verification checks (all must pass):
%s
Available tools:
%s
Strategy:
1. Use tools to inspect, write, or run code.
2. Verify your work matches the check commands above before stopping.
3. When you believe all checks will pass, stop calling tools — the system will run the checks.
4. If any check fails, you'll receive the output and should fix the issue.

Be decisive. Don't ask the user — they're not present. Make reasonable assumptions and act.`, goal, checkList, toolList)
	out := base
	if instructionsSection != "" {
		out += "\n\n## Project Instructions\n\nThe following content was discovered from AGENTS.md, CLAUDE.md, and/or .cursor/rules/*.mdc files in this workspace and its ancestors. Treat it as durable project conventions and persona signals from the user.\n\n" + instructionsSection
	}
	if bankSection != "" {
		out += "\n\n" + bankSection
	}
	return out
}

func main() {
	tools := []tool.Tool{
		&tool.Bash{WorkingDir: "/tmp"},
		&tool.WriteFile{WorkingDir: "/tmp"},
		&tool.ReadFile{WorkingDir: "/tmp"},
		&tool.Edit{WorkingDir: "/tmp"},
		&tool.ApplyPatch{WorkspaceDir: "/tmp"},
		&tool.Repomap{Root: "/tmp"},
		&tool.WebFetch{},
		&tool.WebSearch{},
		&tool.MemoryUpdate{},
		&tool.MemoryLoad{},
		&tool.Plan{},
		&tool.CompactNow{},
		&tool.Clarify{},
	}
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "implement a feature with reasonable complexity"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{
			{Name: "build", Command: "make build"},
			{Name: "test", Command: "make test"},
		}},
	}
	// Plausible AGENTS.md (~1500 chars).
	instr := strings.Repeat("Use tabs not spaces. Prefer testify. Avoid panics. Document why, not what.\n", 20)
	// Bank with progress + brief.
	bank := memory.New("/tmp/measure-bank")
	_ = bank.Init()
	_ = bank.Write(memory.FileProjectBrief, "Build a CLI for autonomous coding agents.\n")
	_ = bank.Write(memory.FileProgress, "## Done\n- step a\n- step b\n## In progress\n- step c\n")

	// Tool schemas are sent on input_schema (provider.ToolDef) regardless
	// of system-prompt diet. Print separately so the dogfood numbers stay
	// honest.
	schemaTok := 0
	for _, t := range tools {
		schemaTok += (len(t.Name()) + len(t.Description()) + len(t.Schema())) / 4
	}

	// OLD assembly (what dogfood actually measured).
	bankOld, _ := buildOldBank(bank)
	old := oldBuildSystemPrompt(spec, tools, instr, bankOld)
	oldTokens := len(old) / 4

	// NEW assembly via the runner package's exported entrypoints.
	// We call into core/runner via the test helper (assembleSystemPrompt
	// is unexported). To keep this binary outside the package, we
	// emulate it by composing the same pieces — except we can't reach
	// the unexported func. So just import the helper externally:
	// runner provides BuildSystemPromptForMeasurement (added in this
	// file's sibling) — see the export below.
	out, bd := runner.MeasureSystemPrompt(spec, tools, bank, instr, 2 /* iter */, runner.SystemPromptOptions{})
	_ = out

	fmt.Println("=== Old (Phase 18) prompt ===")
	fmt.Printf("  system prompt:   ~%d tokens (%d chars)\n", oldTokens, len(old))
	fmt.Printf("  tool schemas:    ~%d tokens (sent on input_schema, separate channel)\n", schemaTok)
	fmt.Printf("  per-call total:  ~%d tokens (system + schemas, before history)\n", oldTokens+schemaTok)
	fmt.Println()
	fmt.Println("=== New (Phase 19 Track B) prompt — iter 2, bank prepended ===")
	for _, s := range bd.Sections {
		fmt.Printf("  %-22s %5d tokens\n", s.Name+":", s.Tokens)
	}
	fmt.Printf("  %-22s %5d tokens\n", "TOTAL:", bd.Total)
	fmt.Println()
	fmt.Printf("Reduction: %d tokens (%.0f%%)\n", oldTokens-bd.Total, float64(oldTokens-bd.Total)*100/float64(oldTokens))

	// First-iteration variant (lazy memory).
	_, bdIter1 := runner.MeasureSystemPrompt(spec, tools, bank, instr, 1 /* iter */, runner.SystemPromptOptions{})
	fmt.Println()
	fmt.Println("=== New (Phase 19 Track B) prompt — iter 1 (lazy memory skips bank) ===")
	for _, s := range bdIter1.Sections {
		fmt.Printf("  %-22s %5d tokens\n", s.Name+":", s.Tokens)
	}
	fmt.Printf("  %-22s %5d tokens\n", "TOTAL:", bdIter1.Total)

	// Minimal mode.
	_, bdMin := runner.MeasureSystemPrompt(spec, tools, bank, instr, 2 /* iter */, runner.SystemPromptOptions{Minimal: true, NoMemory: true})
	fmt.Println()
	fmt.Println("=== New + minimal=true + no_memory=true ===")
	for _, s := range bdMin.Sections {
		fmt.Printf("  %-22s %5d tokens\n", s.Name+":", s.Tokens)
	}
	fmt.Printf("  %-22s %5d tokens\n", "TOTAL:", bdMin.Total)
}

// buildOldBank reproduces the pre-diet bank section (full when small,
// progress-only when large) so the comparison stays apples-to-apples.
func buildOldBank(bank *memory.Bank) (string, error) {
	tokens, err := bank.EstimateTokens()
	if err != nil {
		return "", err
	}
	snap, err := bank.Snapshot()
	if err != nil {
		return "", err
	}
	if len(snap) == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("## Memory Bank\n\nThese files in the session memory directory persist state across compactions and iterations. Update them via the memory_update tool when you complete meaningful work.\n\n")
	if tokens <= 4000 {
		for _, name := range memory.AllFiles {
			content, ok := snap[name]
			if !ok {
				continue
			}
			sb.WriteString("### ")
			sb.WriteString(name)
			sb.WriteString("\n\n")
			sb.WriteString(content)
			if !strings.HasSuffix(content, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
	return sb.String(), nil
}
