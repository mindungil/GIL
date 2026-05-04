package interview

import (
	"encoding/json"
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

func TestExtractAnswerText_PreservesLeadingWhitespaceWhenNothingStripped(t *testing.T) {
	// Bug guard: if no preamble matched, we must NOT silently trim the
	// caller's whitespace — the original is contractually returned.
	in := "  indented question?"
	got := extractAnswerText(in)
	if got != in {
		t.Errorf("got %q; want %q", got, in)
	}
}
