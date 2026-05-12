package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/memory"
	"github.com/mindungil/gil/core/tool"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

// TestAssembleSystemPrompt_Breakdown_PerSection verifies that every
// non-empty section appears in Breakdown.Sections and Total roughly
// matches the sum (allowing for the per-section truncation rounding).
func TestAssembleSystemPrompt_Breakdown_PerSection(t *testing.T) {
	dir := t.TempDir()
	bank := memory.New(filepath.Join(dir, "memory"))
	require.NoError(t, bank.Init())
	require.NoError(t, bank.Write(memory.FileProgress, "## Done\n- step\n"))

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "test goal"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f x"}},
		},
	}
	tools := []tool.Tool{&tool.Bash{WorkingDir: "/tmp"}, &tool.WriteFile{WorkingDir: "/tmp"}}

	out, bd := assembleSystemPrompt(SystemPromptInputs{
		Spec:                 spec,
		Tools:                tools,
		Bank:                 bank,
		InstructionsRendered: "INSTRUCTIONS BLOCK\n",
		Iteration:            2,
		// Pin Compact so this test exercises the compact branch's
		// "tool_names" section. The verbose branch is exercised by
		// TestAssembleSystemPrompt_VerboseMode_IncludesFormatHints
		// below.
		Options: SystemPromptOptions{Compact: true},
	})

	// Every section we expect should appear. We don't pin exact token
	// counts (the heuristic is len/4 and could change) — just that the
	// section was accounted for.
	names := map[string]bool{}
	for _, s := range bd.Sections {
		names[s.Name] = true
		require.Greater(t, s.Tokens, 0, "section %s should have positive tokens", s.Name)
	}
	for _, expect := range []string{
		"base_instructions",
		"verifier_checks",
		"tool_names",
		"agents_md",
		"memory_bank",
	} {
		require.True(t, names[expect], "expected section %s in breakdown", expect)
	}

	// Total >= sum of sections (compact accounting; out-buffer adds no
	// extra characters beyond concat so equality holds within a couple
	// tokens of rounding).
	var sum int
	for _, s := range bd.Sections {
		sum += s.Tokens
	}
	require.GreaterOrEqual(t, bd.Total, sum-2)

	// Sanity: prompt body contains the goal + the AGENTS.md marker + bank.
	require.Contains(t, out, "test goal")
	require.Contains(t, out, "INSTRUCTIONS BLOCK")
	require.Contains(t, out, "Memory Bank")
}

// TestAssembleSystemPrompt_LazyMemory_FirstIterationSkipsBank verifies
// the diet rule: iter 1 must NOT include the memory bank section, iter
// 2+ must. Iteration 0 (the test-helper sentinel meaning "no run loop")
// also includes it for back-compat with the old buildSystemPrompt
// signature.
func TestAssembleSystemPrompt_LazyMemory_FirstIterationSkipsBank(t *testing.T) {
	dir := t.TempDir()
	bank := memory.New(filepath.Join(dir, "memory"))
	require.NoError(t, bank.Init())
	require.NoError(t, bank.Write(memory.FileProgress, "## Done\n- step\n"))

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "g"},
		Verification: &gilv1.Verification{},
	}

	// iter 1 → no bank
	iter1, bd1 := assembleSystemPrompt(SystemPromptInputs{Spec: spec, Bank: bank, Iteration: 1})
	require.NotContains(t, iter1, "Memory Bank")
	require.NotContains(t, sectionNames(bd1), "memory_bank")

	// iter 2 → bank present
	iter2, bd2 := assembleSystemPrompt(SystemPromptInputs{Spec: spec, Bank: bank, Iteration: 2})
	require.Contains(t, iter2, "Memory Bank")
	require.Contains(t, sectionNames(bd2), "memory_bank")

	// iter 0 (shim path / standalone test) → bank present (back-compat)
	iter0, _ := assembleSystemPrompt(SystemPromptInputs{Spec: spec, Bank: bank, Iteration: 0})
	require.Contains(t, iter0, "Memory Bank")
}

// TestAssembleSystemPrompt_NoMemoryOption_AlwaysSkipsBank confirms the
// SystemPromptOptions.NoMemory knob suppresses the bank even on iter
// 5+ where lazy mode would normally include it.
func TestAssembleSystemPrompt_NoMemoryOption_AlwaysSkipsBank(t *testing.T) {
	bank := memory.New(filepath.Join(t.TempDir(), "memory"))
	require.NoError(t, bank.Init())
	require.NoError(t, bank.Write(memory.FileProgress, "BANK_MARKER\n"))

	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "g"}, Verification: &gilv1.Verification{}}
	out, _ := assembleSystemPrompt(SystemPromptInputs{
		Spec:      spec,
		Bank:      bank,
		Iteration: 5,
		Options:   SystemPromptOptions{NoMemory: true},
	})
	require.NotContains(t, out, "BANK_MARKER")
	require.NotContains(t, out, "Memory Bank")
}

// TestAssembleSystemPrompt_MinimalOption_SkipsAgentsMd asserts the
// minimal knob drops the AGENTS.md / CLAUDE.md instructions section
// even when content was discovered + rendered.
func TestAssembleSystemPrompt_MinimalOption_SkipsAgentsMd(t *testing.T) {
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "g"}, Verification: &gilv1.Verification{}}
	out, bd := assembleSystemPrompt(SystemPromptInputs{
		Spec:                 spec,
		InstructionsRendered: "INSTR_MARKER\n",
		Iteration:            2,
		Options:              SystemPromptOptions{Minimal: true},
	})
	require.NotContains(t, out, "INSTR_MARKER")
	require.NotContains(t, sectionNames(bd), "agents_md")
}

// TestAssembleSystemPrompt_DropsToolDescriptions verifies the diet's
// single biggest win: the per-tool description bullets that the old
// buildSystemPrompt embedded are gone. Tool *names* still appear — the
// model needs the menu — but verbose Description() text does not, since
// the provider attaches it on the input_schema separately.
func TestAssembleSystemPrompt_DropsToolDescriptions(t *testing.T) {
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "g"}, Verification: &gilv1.Verification{}}
	tools := []tool.Tool{&tool.Bash{WorkingDir: "/tmp"}, &tool.Edit{WorkingDir: "/tmp"}}
	out, _ := assembleSystemPrompt(SystemPromptInputs{Spec: spec, Tools: tools, Iteration: 2})

	require.Contains(t, out, "bash")
	require.Contains(t, out, "edit")
	// The verbose Bash description contains "Execute a shell command";
	// the diet should NOT echo it.
	require.NotContains(t, out, "Execute a shell command in the project working directory")
	// The verbose Edit description contains "SEARCH/REPLACE blocks"; same.
	require.NotContains(t, out, "SEARCH/REPLACE blocks")
}

// TestAssembleSystemPrompt_TotalUnderTarget loosely guards against
// regressions where someone re-introduces a verbose section. Pre-diet
// the same input ran ~1500+ tokens; the diet target is well under
// 1000. We pin a generous 800-token ceiling so unrelated content
// tweaks don't trigger a flaky re-tune.
func TestAssembleSystemPrompt_TotalUnderTarget(t *testing.T) {
	bank := memory.New(filepath.Join(t.TempDir(), "memory"))
	require.NoError(t, bank.Init())
	require.NoError(t, bank.Write(memory.FileProgress, "## Done\n- a\n- b\n"))

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "ship the diet"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "make build"},
				{Name: "test", Kind: gilv1.CheckKind_SHELL, Command: "make test"},
			},
		},
	}
	tools := []tool.Tool{
		&tool.Bash{WorkingDir: "/tmp"},
		&tool.WriteFile{WorkingDir: "/tmp"},
		&tool.ReadFile{WorkingDir: "/tmp"},
		&tool.Edit{WorkingDir: "/tmp"},
		&tool.Repomap{Root: "/tmp"},
	}
	_, bd := assembleSystemPrompt(SystemPromptInputs{Spec: spec, Tools: tools, Bank: bank, Iteration: 2})
	require.Less(t, bd.Total, 800, "system prompt should fit under 800-token diet target; got %d", bd.Total)
}

// TestDebugLogBreakdown_HonorsEnvVar verifies the debug print is gated
// on GIL_DEBUG_SYSTEM_PROMPT=1 — accidentally printing every run would
// be ugly noise. We capture stderr by swapping os.Stderr around the call.
func TestDebugLogBreakdown_HonorsEnvVar(t *testing.T) {
	bd := Breakdown{
		Sections: []SectionTokens{{Name: "base_instructions", Tokens: 100}},
		Total:    100,
	}

	// Without env var → no output.
	t.Setenv("GIL_DEBUG_SYSTEM_PROMPT", "")
	out := captureStderr(t, func() { debugLogBreakdown(bd) })
	require.Empty(t, out)

	// With env var → header + section + total.
	t.Setenv("GIL_DEBUG_SYSTEM_PROMPT", "1")
	out = captureStderr(t, func() { debugLogBreakdown(bd) })
	require.Contains(t, out, "System prompt breakdown")
	require.Contains(t, out, "base_instructions")
	require.Contains(t, out, "TOTAL")
	require.Contains(t, out, "100")
}

// TestResolveSystemPromptOptions_SpecAndFieldMerge confirms the tri-state
// resolution: AgentLoop field OR spec.run.system_prompt → effective
// options. Either side flipping a flag wins.
func TestResolveSystemPromptOptions_SpecAndFieldMerge(t *testing.T) {
	// Spec sets minimal, field sets no_memory → both true.
	a := &AgentLoop{
		SystemPromptOpts: SystemPromptOptions{NoMemory: true},
		Spec: &gilv1.FrozenSpec{
			Run: &gilv1.RunOptions{
				SystemPrompt: &gilv1.SystemPromptOptions{Minimal: true},
			},
		},
	}
	got := a.resolveSystemPromptOptions()
	require.True(t, got.Minimal)
	require.True(t, got.NoMemory)

	// No spec, no field → both false.
	a = &AgentLoop{}
	got = a.resolveSystemPromptOptions()
	require.False(t, got.Minimal)
	require.False(t, got.NoMemory)

	// Spec only.
	a = &AgentLoop{
		Spec: &gilv1.FrozenSpec{
			Run: &gilv1.RunOptions{
				SystemPrompt: &gilv1.SystemPromptOptions{Minimal: true, NoMemory: true},
			},
		},
	}
	got = a.resolveSystemPromptOptions()
	require.True(t, got.Minimal)
	require.True(t, got.NoMemory)
}

// TestPickVerbosity_Matrix walks every supported provider name and
// confirms the per-provider Compact default. Phase 20.B regression
// guard: this matrix is the contract that decides "qwen via vllm gets
// the verbose tool block, claude via anthropic stays compact". Adding
// a new provider here without thinking about this default is a
// review-time decision, not an implementation accident.
func TestPickVerbosity_Matrix(t *testing.T) {
	cases := []struct {
		provider string
		want     SystemPromptOptions
	}{
		{"anthropic", SystemPromptOptions{Compact: true}},
		{"openai", SystemPromptOptions{Compact: true}},
		{"openrouter", SystemPromptOptions{Compact: true}},
		{"vllm", SystemPromptOptions{Compact: false}},
		{"local", SystemPromptOptions{Compact: false}},
		{"mock", SystemPromptOptions{Compact: true}},
		{"", SystemPromptOptions{Compact: false}},               // unknown → verbose (safe)
		{"some-future-provider", SystemPromptOptions{Compact: false}}, // same
	}
	for _, c := range cases {
		got := pickVerbosity(c.provider, nil)
		require.Equal(t, c.want, got, "provider=%q", c.provider)
	}
}

// TestPickVerbosity_OverrideWins confirms that a non-nil override beats
// the per-provider default — user explicit > automatic. Both directions
// (force-compact-on-vllm, force-verbose-on-anthropic) work.
func TestPickVerbosity_OverrideWins(t *testing.T) {
	// Force compact on vllm (a user who knows their qwen variant
	// handles compact prompts).
	override := &SystemPromptOptions{Compact: true}
	got := pickVerbosity("vllm", override)
	require.True(t, got.Compact, "override should force compact on vllm")

	// Force verbose on anthropic (a user debugging a Claude format
	// regression).
	override = &SystemPromptOptions{Compact: false}
	got = pickVerbosity("anthropic", override)
	require.False(t, got.Compact, "override should force verbose on anthropic")

	// Nil override → fall through to default.
	got = pickVerbosity("vllm", nil)
	require.False(t, got.Compact)
	got = pickVerbosity("anthropic", nil)
	require.True(t, got.Compact)
}

// TestAssembleSystemPrompt_VerboseMode_IncludesFormatHints verifies the
// verbose branch includes the per-tool format-hint block for the
// trickiest tools (edit, apply_patch). Phase 20.B regression guard:
// the dogfood Run 3 failure was the agent missing these hints; this
// test ensures we don't accidentally regress them out.
func TestAssembleSystemPrompt_VerboseMode_IncludesFormatHints(t *testing.T) {
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "g"}, Verification: &gilv1.Verification{}}
	tools := []tool.Tool{
		&tool.Edit{WorkingDir: "/tmp"},
		&tool.ApplyPatch{WorkspaceDir: "/tmp"},
		&tool.Bash{WorkingDir: "/tmp"},
	}
	out, bd := assembleSystemPrompt(SystemPromptInputs{
		Spec:      spec,
		Tools:     tools,
		Iteration: 2,
		// Compact: false (default) → verbose branch.
	})

	// Section name pins which branch ran. (We can't use NotContains
	// for "tool_names" since "tool_names_verbose" contains it as a
	// prefix — instead, scan the section list for an exact "tool_names"
	// entry.)
	require.Contains(t, sectionNames(bd), "tool_names_verbose")
	require.False(t, hasExactSection(bd, "tool_names"), "verbose mode should not emit the compact tool_names section")

	// "Available tools:" header marks the verbose block start.
	require.Contains(t, out, "Available tools:")

	// Edit block format hint — the qwen-killer details.
	require.Contains(t, out, "<<<<<<< SEARCH")
	require.Contains(t, out, ">>>>>>> REPLACE")
	require.Contains(t, out, "OWN line BEFORE the SEARCH marker")

	// Apply_patch block format hint — header layout + body line
	// prefixes.
	require.Contains(t, out, "*** Begin Patch")
	require.Contains(t, out, "*** Update File:")
	require.Contains(t, out, "*** End Patch")
	require.Contains(t, out, "@@ <optional description>")
	require.Contains(t, out, "' ' (one space, context)")
}

// TestAssembleSystemPrompt_CompactMode_DropsVerboseToolBlock verifies
// the compact branch strips the verbose "Available tools:" block. Tool
// *names* still appear (the one-line "Tools: bash, edit, ..." menu).
// Format hints do NOT — strong models read the input_schema.
func TestAssembleSystemPrompt_CompactMode_DropsVerboseToolBlock(t *testing.T) {
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "g"}, Verification: &gilv1.Verification{}}
	tools := []tool.Tool{
		&tool.Edit{WorkingDir: "/tmp"},
		&tool.ApplyPatch{WorkspaceDir: "/tmp"},
		&tool.Bash{WorkingDir: "/tmp"},
	}
	out, bd := assembleSystemPrompt(SystemPromptInputs{
		Spec:      spec,
		Tools:     tools,
		Iteration: 2,
		Options:   SystemPromptOptions{Compact: true},
	})

	// Section name pins which branch ran.
	require.True(t, hasExactSection(bd, "tool_names"), "compact mode should emit the compact tool_names section")
	require.False(t, hasExactSection(bd, "tool_names_verbose"))

	// Compact: drops the verbose header.
	require.NotContains(t, out, "Available tools:")
	// Compact: drops the format hints.
	require.NotContains(t, out, "OWN line BEFORE the SEARCH marker")
	require.NotContains(t, out, "@@ <optional description>")
	// Compact: keeps the one-line tool menu.
	require.Contains(t, out, "Tools: ")
	require.Contains(t, out, "edit")
	require.Contains(t, out, "apply_patch")
}

// TestResolveSystemPromptOptions_PerProviderDefault confirms the
// resolution chain: pickVerbosity baseline → SystemPromptOpts override
// → spec override. The test walks one case per provider and
// independently verifies each layer.
func TestResolveSystemPromptOptions_PerProviderDefault(t *testing.T) {
	// vllm baseline: Compact=false.
	a := &AgentLoop{ProviderName: "vllm"}
	require.False(t, a.resolveSystemPromptOptions().Compact)

	// anthropic baseline: Compact=true.
	a = &AgentLoop{ProviderName: "anthropic"}
	require.True(t, a.resolveSystemPromptOptions().Compact)

	// vllm + AgentLoop field forces Compact: true.
	a = &AgentLoop{
		ProviderName:     "vllm",
		SystemPromptOpts: SystemPromptOptions{Compact: true},
	}
	require.True(t, a.resolveSystemPromptOptions().Compact)

	// Empty ProviderName falls through to "unknown" → Compact=false.
	a = &AgentLoop{}
	require.False(t, a.resolveSystemPromptOptions().Compact)
}

// captureStderr swaps os.Stderr around fn and returns whatever was
// written. Closes the pipe before returning so the goroutine reading
// it can finish.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })

	doneCh := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		doneCh <- buf.Bytes()
	}()

	fn()
	_ = w.Close()
	out := <-doneCh
	return string(out)
}

// sectionNames returns just the names of the breakdown sections joined
// into a single string, suitable for require.Contains.
func sectionNames(bd Breakdown) string {
	names := make([]string, 0, len(bd.Sections))
	for _, s := range bd.Sections {
		names = append(names, s.Name)
	}
	return strings.Join(names, ",")
}

// hasExactSection scans bd for an exact section-name match. Useful when
// require.NotContains can't be used because a name is a prefix of
// another (e.g., "tool_names" inside "tool_names_verbose").
func hasExactSection(bd Breakdown, name string) bool {
	for _, s := range bd.Sections {
		if s.Name == name {
			return true
		}
	}
	return false
}
