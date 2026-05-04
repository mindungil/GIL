package interview

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractJSON_PassThroughBareObject(t *testing.T) {
	in := `{"a":1,"b":"two"}`
	got := extractJSON(in)
	if got != in {
		t.Errorf("clean JSON should pass through unchanged; got %q", got)
	}
}

func TestExtractJSON_StripsAnthropicThinkingBlock(t *testing.T) {
	in := `<thinking>The user wants a CLI tool. I should ask about the
language preference. Let me draft the JSON.</thinking>{"updates":[]}`
	got := extractJSON(in)
	if got != `{"updates":[]}` {
		t.Errorf("got %q; want %q", got, `{"updates":[]}`)
	}
}

func TestExtractJSON_StripsThinkingBlock_LowerCase(t *testing.T) {
	in := `<think>reasoning here</think>
{"ready":true,"reason":"all slots filled"}`
	got := extractJSON(in)
	var parsed struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (got=%q)", err, got)
	}
	if !parsed.Ready || parsed.Reason != "all slots filled" {
		t.Errorf("parsed wrong: %+v", parsed)
	}
}

func TestExtractJSON_StripsQwenStylePreamble(t *testing.T) {
	in := `Thinking Process:

The user said they want a Go CLI. The domain is software development tools.
Confidence is high.

{"domain":"cli-tooling","domain_confidence":0.9}`
	got := extractJSON(in)
	if got != `{"domain":"cli-tooling","domain_confidence":0.9}` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_StripsMarkdownCodeFence(t *testing.T) {
	in := "```json\n{\"updates\":[]}\n```"
	got := extractJSON(in)
	if got != `{"updates":[]}` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_StripsBareCodeFence(t *testing.T) {
	in := "```\n{\"x\":1}\n```"
	got := extractJSON(in)
	if got != `{"x":1}` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_HandlesArray(t *testing.T) {
	in := `Thinking...

[{"severity":"blocker","msg":"X"}]`
	got := extractJSON(in)
	if got != `[{"severity":"blocker","msg":"X"}]` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_BalancesBracketsWithStringContainingBrace(t *testing.T) {
	// The closing `}` inside the string MUST NOT close the outer object.
	in := `{"reason":"saw } in payload","ready":false}`
	got := extractJSON(in)
	if got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestExtractJSON_DropsTrailingProse(t *testing.T) {
	in := `{"x":1}

That's my answer. Let me know if anything is wrong.`
	got := extractJSON(in)
	if got != `{"x":1}` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_EscapedQuoteInString(t *testing.T) {
	in := `prelude {"msg":"she said \"hi\" "}` + ` trailing`
	got := extractJSON(in)
	if got != `{"msg":"she said \"hi\" "}` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_NoJSONReturnsAsIs(t *testing.T) {
	in := `I cannot help with that.`
	got := extractJSON(in)
	if got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestExtractJSON_UnbalancedReturnsTail(t *testing.T) {
	// json.Unmarshal will then produce a clean "unexpected end" diagnostic.
	in := `prose {"x":1`
	got := extractJSON(in)
	if got != `{"x":1` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_NestedObjects(t *testing.T) {
	in := `<thinking>...</thinking>{"outer":{"inner":{"deep":"value"}}}`
	got := extractJSON(in)
	if got != `{"outer":{"inner":{"deep":"value"}}}` {
		t.Errorf("got %q", got)
	}
}

// Regression: a reasoning model emits a short array literal as part of
// its prose ("Value: ["a","b"]") BEFORE delivering the real answer.
// Earlier extractJSON returned the prose array because it picked the
// first bracket; the largest-balanced rule must pick the actual
// answer instead. Captured live during P26.6 dogfood.
func TestExtractJSON_PrefersLargestWhenProseEmbedsShortArray(t *testing.T) {
	in := `The user is giving constraints. Map them to slots.

Value for goal.success_criteria_natural:
["Return -1 for negative inputs", "Assume the slice is pre-sorted"]

Now the actual JSON:

{"updates":[{"field":"goal.success_criteria_natural","value":["Return -1 for negative inputs","Assume the slice is pre-sorted"]}]}

Wait, is there a better fit?`
	got := extractJSON(in)
	var parsed struct {
		Updates []struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		} `json:"updates"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("expected the largest balanced object to parse cleanly; got=%q err=%v", got, err)
	}
	if len(parsed.Updates) != 1 || parsed.Updates[0].Field != "goal.success_criteria_natural" {
		t.Errorf("parsed wrong shape: %+v", parsed)
	}
}

// Same scenario but with the JSON answer EARLIER than the trailing
// prose-embedded examples. Confirms "largest" beats "first" both ways.
func TestExtractJSON_PrefersLargestEvenWhenLastIsSmaller(t *testing.T) {
	in := `Here is my answer:

{"updates":[{"field":"goal.one_liner","value":"Build a fast key-value store with TTL support"}]}

For reference, an empty update would be: {"updates":[]}`
	got := extractJSON(in)
	if !strings.Contains(got, "key-value store") {
		t.Errorf("got=%q; expected the larger object containing the actual goal", got)
	}
}

// Pure-array shape (adversary returns []Finding) must still work after
// the largest-wins refactor.
func TestExtractJSON_PureArrayResponseStillExtracted(t *testing.T) {
	in := `<think>let me critique</think>
[{"severity":"blocker","category":"missing","msg":"no error case"},
 {"severity":"warn","category":"vague","msg":"timeout undefined"}]`
	got := extractJSON(in)
	var parsed []struct {
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("array response broke after largest-wins: got=%q err=%v", got, err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 findings, got %d", len(parsed))
	}
}

func TestExtractJSON_StripsDeepSeekThinkBlock(t *testing.T) {
	in := `<think>I need to think about the domain.</think>
{"domain":"web","domain_confidence":0.7}`
	got := extractJSON(in)
	if got != `{"domain":"web","domain_confidence":0.7}` {
		t.Errorf("got %q", got)
	}
}

// --- extractAnswerText ---------------------------------------------

func TestExtractAnswerText_PassThroughCleanQuestion(t *testing.T) {
	in := "What programming language do you prefer?"
	got := extractAnswerText(in)
	if got != in {
		t.Errorf("clean text should pass through; got %q", got)
	}
}

func TestExtractAnswerText_StripsThinkBlock(t *testing.T) {
	in := `<think>The user mentioned Go but didn't specify a build system. I should
ask about that next.</think>What build system would you like to use?`
	got := extractAnswerText(in)
	want := "What build system would you like to use?"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestExtractAnswerText_StripsAnthropicThinkingBlock(t *testing.T) {
	in := `<thinking>Let me reason about what to ask.

The user is building a CLI. The most useful question now is about deployment.</thinking>

How will the CLI be distributed to end users?`
	got := extractAnswerText(in)
	want := "How will the CLI be distributed to end users?"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestExtractAnswerText_StripsQwenStylePreamble(t *testing.T) {
	in := `Thinking Process:

The user said they want a Go CLI. I should narrow scope by asking about the
target audience.

Who is the primary user of this CLI?`
	got := extractAnswerText(in)
	want := "Who is the primary user of this CLI?"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestExtractAnswerText_StripsThoughtPreamble(t *testing.T) {
	in := "Thought: This is a follow-up about persistence.\n\nWill the data need to survive process restarts?"
	got := extractAnswerText(in)
	want := "Will the data need to survive process restarts?"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestExtractAnswerText_PreservesMultiParagraphAnswer(t *testing.T) {
	// No preamble, no <think> block — must pass through verbatim
	// (whitespace included), since interviewers sometimes ask a
	// two-sentence question with formatting.
	in := "Question 1: What's the timeline?\nQuestion 2: What's the budget?"
	got := extractAnswerText(in)
	if got != in {
		t.Errorf("multi-paragraph clean text should pass through; got %q", got)
	}
}

func TestExtractAnswerText_HeaderWithoutBlankLineKept(t *testing.T) {
	// "Reasoning:" followed immediately by the answer (no blank line
	// gap). We strip the header but keep what follows as the answer.
	in := "Reasoning: weighing tradeoffs. What database do you prefer?"
	got := extractAnswerText(in)
	want := "weighing tradeoffs. What database do you prefer?"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestExtractAnswerText_EmptyAfterStrip(t *testing.T) {
	// Pathological case: only thinking, no answer.
	in := "<think>pondering</think>"
	got := extractAnswerText(in)
	if got != "" {
		t.Errorf("got %q; want empty string", got)
	}
}

// Regression: reasoning model emits chain-of-thought as plain prose
// (no <think> tag, no "Thinking Process:" header) and delivers the
// final answer as the last line wrapped in literal quotes. Captured
// live during P26.6 dogfood — second-turn NextQuestion from Qwen.
func TestExtractAnswerText_LiftsQuotedFinalLine(t *testing.T) {
	in := `Wait, "math/search.go" might imply ` + "`float64`" + `. Binary search on floats is weird.
    "What data type should the input slice and target be, and does the input strictly require ascending order?"`
	got := extractAnswerText(in)
	want := "What data type should the input slice and target be, and does the input strictly require ascending order?"
	if got != want {
		t.Errorf("got %q;\nwant %q", got, want)
	}
}

// Reasoning model emits MULTIPLE quoted candidate questions while
// self-critiquing, ending with prose that may include a truncated
// quoted fragment. The last COMPLETE quoted line is the chosen
// answer. Captured live during P26.6 dogfood.
func TestExtractAnswerText_LiftsLastCompleteQuotedAmongCandidates(t *testing.T) {
	in := `Wait, the user said "tiny http server".
    "Does 'tiny' imply a bare-bones implementation, or are there extra features needed?"
    This is good.
    But maybe simpler: "What features does the server need?"
    This is less presumptuous about the definition of "`
	got := extractAnswerText(in)
	want := "What features does the server need?"
	if got != want {
		t.Errorf("got %q;\nwant %q", got, want)
	}
}

// The tail-quoted heuristic must NOT eat single-line quoted
// responses (those are likely intentional formatting like a
// section title — passing through is the safer default).
func TestExtractAnswerText_SingleQuotedLine_PassesThrough(t *testing.T) {
	in := `"What is your goal?"`
	got := extractAnswerText(in)
	if got != in {
		t.Errorf("single quoted line should pass through; got %q", got)
	}
}

// And it must NOT eat a multi-line answer where the last line isn't
// quoted (the standard 1–2 sentence question format).
func TestExtractAnswerText_MultiLine_NoQuoted_PassesThrough(t *testing.T) {
	in := "First context line.\nWhat would you like next?"
	got := extractAnswerText(in)
	if got != in {
		t.Errorf("got %q; want pass-through", got)
	}
}

func TestExtractAnswerText_PreservesLeadingWhitespaceWhenNothingStripped(t *testing.T) {
	// Bug guard: if no preamble matched, we must NOT silently trim the
	// caller's whitespace — the original is contractually returned.
	in := "  indented question?"
	got := extractAnswerText(in)
	if got != in {
		t.Errorf("got %q; want %q", got, in)
	}
}
