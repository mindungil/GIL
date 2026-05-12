package compact

import (
	"strings"

	"github.com/mindungil/gil/core/provider"
)

// BuildSummaryPrompt returns the LLM prompt used to compress a middle slice of
// messages into a structured markdown summary. The model is instructed to
// produce ONLY the markdown (no preamble) so the output can be inserted
// directly as a synthetic message body.
//
// Template structure (OpenCode pattern):
//
//	## Goal
//	- <single-sentence task summary>
//
//	## Constraints & Preferences
//	- ...
//
//	## Progress
//	### Done
//	- ...
//	### In Progress
//	- ...
//	### Blocked
//	- ...
func BuildSummaryPrompt(middle []provider.Message) string {
	var sb strings.Builder
	sb.WriteString(promptHeader)
	sb.WriteString("\n\nConversation segment:\n")
	sb.WriteString(formatMessages(middle))
	return sb.String()
}

const promptHeader = `You are summarizing the middle of a long autonomous coding session so the assistant can continue without context loss.

Produce ONLY this exact markdown structure (no preamble, no commentary):

## Goal
- <single-sentence task summary inferred from the segment>

## Constraints & Preferences
- <each constraint as one bullet>

## Progress
### Done
- <each completed step as one bullet>
### In Progress
- <current activity as one bullet>
### Blocked
- <each blocker as one bullet, or "(none)" if there are none>

Rules:
- Keep bullets terse — facts only, no commentary
- Preserve file names, function names, error strings verbatim
- If a section has no content, write "- (none)"
- Output the markdown immediately; no "Here is..." prefix`

func formatMessages(msgs []provider.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString("[")
		sb.WriteString(string(m.Role))
		sb.WriteString("] ")
		// Replace newlines so the prompt stays single-line per message;
		// segment separators in the LLM input would otherwise look like new turns.
		sb.WriteString(strings.ReplaceAll(m.Content, "\n", " "))
		sb.WriteString("\n")
		// Tool calls / results are surfaced as compact descriptors so the
		// summary can include them.
		for _, tc := range m.ToolCalls {
			sb.WriteString("  → tool_call ")
			sb.WriteString(tc.Name)
			sb.WriteString(" ")
			sb.Write(tc.Input)
			sb.WriteString("\n")
		}
		for _, tr := range m.ToolResults {
			sb.WriteString("  ← tool_result ")
			if tr.IsError {
				sb.WriteString("ERROR ")
			}
			sb.WriteString(strings.ReplaceAll(tr.Content, "\n", " "))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
