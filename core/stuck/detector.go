package stuck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/mindungil/gil/core/event"
)

// Pattern identifies a category of stuck behaviour.
type Pattern int

const (
	PatternUnknown                   Pattern = iota
	PatternRepeatedActionObservation         // same tool_call+tool_result content 4+ times in window
	PatternRepeatedActionError               // same tool_call followed by error tool_result 3+ times
	PatternMonologue                         // 3+ consecutive provider_response with zero tool_calls
	PatternPingPong                          // strict alternation between two distinct tool signatures 6+ events
	PatternContextWindowError                // 2+ run_error events mentioning context/token overflow
	PatternNoProgress                        // K+ iters with verifier stalled and files empty/churning
)

// String returns a human-readable name for p.
func (p Pattern) String() string {
	switch p {
	case PatternRepeatedActionObservation:
		return "RepeatedActionObservation"
	case PatternRepeatedActionError:
		return "RepeatedActionError"
	case PatternMonologue:
		return "Monologue"
	case PatternPingPong:
		return "PingPong"
	case PatternContextWindowError:
		return "ContextWindowError"
	case PatternNoProgress:
		return "NoProgress"
	default:
		return "Unknown"
	}
}

// Signal is a single detected stuck pattern.
type Signal struct {
	Pattern Pattern
	Detail  string // human-readable explanation
	Count   int    // number of contributing occurrences
}

// Detector inspects a sliding window of recent events for stuck patterns.
// It is stateless: all detection happens in Check. Goroutine-safe by construction.
type Detector struct {
	// Window is the maximum number of recent events to inspect.
	// If zero, defaults to 50.
	Window int

	// NoProgressThreshold is the minimum number of consecutive iterations
	// (with at least one verify_run) over which the verifier passing count
	// must remain unchanged AND files-modified must be empty or churning
	// before PatternNoProgress fires. If zero, defaults to 4.
	NoProgressThreshold int

	// NoProgressOscillationCap is the maximum number of distinct edits a
	// single file may receive across the window before "churn" is considered
	// active. If zero, defaults to 2.
	NoProgressOscillationCap int
}

// Check inspects events (newest last) and returns all matching signals.
// Returns an empty slice when no patterns match. Pure function; no I/O.
func (d *Detector) Check(events []event.Event) []Signal {
	window := d.Window
	if window <= 0 {
		window = 50
	}
	if len(events) > window {
		events = events[len(events)-window:]
	}

	var signals []Signal
	signals = append(signals, checkRepeatedActionObservation(events)...)
	signals = append(signals, checkRepeatedActionError(events)...)
	signals = append(signals, checkMonologue(events)...)
	signals = append(signals, checkPingPong(events)...)
	signals = append(signals, checkContextWindowError(events)...)
	signals = append(signals, checkNoProgress(events, d.NoProgressThreshold, d.NoProgressOscillationCap)...)
	return signals
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16]) // 32 hex chars
}

// parseData unmarshals event.Data into a map. Returns nil on failure.
func parseData(e event.Event) map[string]any {
	if len(e.Data) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(e.Data, &m); err != nil {
		return nil
	}
	return m
}

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, _ := m[key].(bool)
	return v
}

func float64Field(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, _ := m[key].(float64) // JSON numbers decode as float64
	return v
}

// pairToolCallsResults walks events linearly. For each tool_call it finds the
// next tool_result with the same tool name and returns them as a pair.
type callResultPair struct {
	name    string
	inputJSON string // raw input bytes as string
	content string
	isError bool
}

func pairToolEvents(events []event.Event) []callResultPair {
	var pairs []callResultPair
	n := len(events)
	for i := 0; i < n; i++ {
		e := events[i]
		if e.Type != "tool_call" {
			continue
		}
		m := parseData(e)
		if m == nil {
			continue
		}
		name := strField(m, "name")
		inputJSON := strField(m, "input")
		if name == "" {
			continue
		}
		// find matching tool_result
		for j := i + 1; j < n; j++ {
			r := events[j]
			if r.Type != "tool_result" {
				continue
			}
			rm := parseData(r)
			if rm == nil {
				continue
			}
			if strField(rm, "name") != name {
				continue
			}
			pairs = append(pairs, callResultPair{
				name:      name,
				inputJSON: inputJSON,
				content:   strField(rm, "content"),
				isError:   boolField(rm, "is_error"),
			})
			i = j // advance outer cursor past consumed result
			break
		}
	}
	return pairs
}

// --------------------------------------------------------------------------
// Pattern checkers
// --------------------------------------------------------------------------

func checkRepeatedActionObservation(events []event.Event) []Signal {
	pairs := pairToolEvents(events)
	counts := map[string]int{}
	nameFor := map[string]string{}

	for _, p := range pairs {
		if p.isError {
			continue // only non-error pairs for this pattern
		}
		key := p.name + "|" + shortHash(p.inputJSON) + "|" + shortHash(p.content)
		counts[key]++
		nameFor[key] = p.name
	}

	var signals []Signal
	for key, cnt := range counts {
		if cnt >= 4 {
			signals = append(signals, Signal{
				Pattern: PatternRepeatedActionObservation,
				Detail:  "tool '" + nameFor[key] + "' repeated identical action+observation " + itoa(cnt) + " times",
				Count:   cnt,
			})
		}
	}
	return signals
}

func checkRepeatedActionError(events []event.Event) []Signal {
	pairs := pairToolEvents(events)
	counts := map[string]int{}
	nameFor := map[string]string{}

	for _, p := range pairs {
		if !p.isError {
			continue
		}
		key := p.name + "|" + shortHash(p.inputJSON)
		counts[key]++
		nameFor[key] = p.name
	}

	var signals []Signal
	for key, cnt := range counts {
		if cnt >= 3 {
			signals = append(signals, Signal{
				Pattern: PatternRepeatedActionError,
				Detail:  "tool '" + nameFor[key] + "' failed identically " + itoa(cnt) + " times",
				Count:   cnt,
			})
		}
	}
	return signals
}

func checkMonologue(events []event.Event) []Signal {
	maxRun := 0
	cur := 0

	for _, e := range events {
		switch e.Type {
		case "provider_response":
			m := parseData(e)
			if m == nil {
				cur = 0
				continue
			}
			toolCalls := float64Field(m, "tool_calls")
			if toolCalls == 0 {
				cur++
				if cur > maxRun {
					maxRun = cur
				}
			} else {
				cur = 0
			}
		case "tool_result":
			// an actual tool result interrupts the monologue
			cur = 0
		default:
			// user events would also break the run; detect by source is harder
			// since we only have Type here, so we rely on tool_result resets.
		}
	}

	if maxRun >= 3 {
		return []Signal{{
			Pattern: PatternMonologue,
			Detail:  "agent monologued " + itoa(maxRun) + " turns without action",
			Count:   maxRun,
		}}
	}
	return nil
}

func checkPingPong(events []event.Event) []Signal {
	// collect ordered tool_call signatures
	var sigs []string
	for _, e := range events {
		if e.Type != "tool_call" {
			continue
		}
		m := parseData(e)
		if m == nil {
			continue
		}
		name := strField(m, "name")
		input := strField(m, "input")
		sigs = append(sigs, name+"|"+shortHash(input))
	}

	if len(sigs) < 6 {
		return nil
	}

	// check if the last 6+ entries strictly alternate between two distinct sigs
	// We try every possible tail length ≥ 6.
	best := 0
	var bestA, bestB string

	for start := 0; start <= len(sigs)-6; start++ {
		a := sigs[start]
		b := sigs[start+1]
		if a == b {
			continue
		}
		run := 2
		for k := start + 2; k < len(sigs); k++ {
			expected := a
			if (k-start)%2 == 1 {
				expected = b
			}
			if sigs[k] != expected {
				break
			}
			run++
		}
		if run >= 6 && run > best {
			best = run
			bestA = a
			bestB = b
		}
	}

	if best >= 6 {
		// extract short names for display (before first |)
		nameA := strings.SplitN(bestA, "|", 2)[0]
		nameB := strings.SplitN(bestB, "|", 2)[0]
		return []Signal{{
			Pattern: PatternPingPong,
			Detail:  "alternating between '" + nameA + "' and '" + nameB + "' for " + itoa(best) + " turns",
			Count:   best,
		}}
	}
	return nil
}

func checkContextWindowError(events []event.Event) []Signal {
	count := 0
	overflow := []string{
		"context window",
		"context length",
		"token limit",
		"too many tokens",
	}
	for _, e := range events {
		if e.Type != "run_error" {
			continue
		}
		m := parseData(e)
		if m == nil {
			continue
		}
		errStr := strings.ToLower(strField(m, "err"))
		for _, kw := range overflow {
			if strings.Contains(errStr, kw) {
				count++
				break
			}
		}
	}
	if count >= 2 {
		return []Signal{{
			Pattern: PatternContextWindowError,
			Detail:  "context overflow occurred " + itoa(count) + " times",
			Count:   count,
		}}
	}
	return nil
}

// --------------------------------------------------------------------------
// PatternNoProgress
// --------------------------------------------------------------------------
//
// NoProgress fires when an agent is varying its actions but making no
// measurable progress. Existing patterns (RepeatedAction*, PingPong,
// Monologue) all require some form of REPETITION; varied-but-futile work
// passes through them. Self-dogfood Run 8 demonstrated this gap: agent
// burned 12 iterations on an impossible task with 0 stuck events.
//
// Trigger logic (all must hold):
//   - At least Threshold (default 4) distinct iterations are visible in the
//     window. Iterations are bounded by "iteration_start" events.
//   - At least one "verify_run" event has fired within the window. If none,
//     we abstain — we have no signal of progress, so we cannot claim its
//     absence. This keeps NoProgress quiet on early iters.
//   - The verifier passing count (number of "verify_result" events with
//     passed=true between successive verify_run events) is monotonically
//     non-improving across the K most recent iterations that contain a
//     verify_run.
//   - Files-modified set across those K iters is EITHER empty OR oscillating
//     (some single file edited >= OscillationCap times, default 2).
//
// "Files modified" is derived from successful tool_call/tool_result pairs
// for "edit", "write_file", and "apply_patch" tools. The path is extracted
// from the tool_call input (best-effort: the edit tool embeds paths inside
// a "blocks" string DSL, which we scan for filenames; write_file has a
// top-level "path" field; apply_patch embeds paths inside a "patch" DSL).

// noProgressIterStat aggregates per-iteration stats while walking events.
type noProgressIterStat struct {
	iter             int
	hasVerifyRun     bool
	verifyPassing    int
	files            map[string]int // path → successful edits this iter
}

func checkNoProgress(events []event.Event, threshold, oscillationCap int) []Signal {
	if threshold <= 0 {
		threshold = 4
	}
	if oscillationCap <= 0 {
		oscillationCap = 2
	}

	stats := collectNoProgressStats(events)
	if len(stats) < threshold {
		return nil
	}

	// Take the most recent K iterations (ordered by their event order, which
	// matches iteration order since iteration_start events come in sequence).
	last := stats[len(stats)-threshold:]

	// Require at least one verify_run signal across the K iters; otherwise
	// abstain — we have no progress signal and cannot claim its absence.
	anyVerify := false
	for _, s := range last {
		if s.hasVerifyRun {
			anyVerify = true
			break
		}
	}
	if !anyVerify {
		return nil
	}

	// Verifier passing count must be non-improving (i.e., never strictly
	// increases) across iters that ran verify. If any iter strictly improves
	// over the previous verify-bearing iter, progress was made.
	var verifyTrace []int
	for _, s := range last {
		if s.hasVerifyRun {
			verifyTrace = append(verifyTrace, s.verifyPassing)
		}
	}
	for i := 1; i < len(verifyTrace); i++ {
		if verifyTrace[i] > verifyTrace[i-1] {
			return nil // strict improvement → not stuck
		}
	}
	stalledAt := 0
	if len(verifyTrace) > 0 {
		stalledAt = verifyTrace[len(verifyTrace)-1]
	}

	// Merge files across the K iters.
	merged := map[string]int{}
	for _, s := range last {
		for f, n := range s.files {
			merged[f] += n
		}
	}

	churn := false
	churnFile := ""
	churnCount := 0
	for f, n := range merged {
		if n >= oscillationCap {
			if n > churnCount {
				churnCount = n
				churnFile = f
			}
			churn = true
		}
	}

	if !(len(merged) == 0 || churn) {
		return nil
	}

	// Build a stable, terse detail string.
	var sb strings.Builder
	sb.WriteString("verifier stalled at ")
	sb.WriteString(itoa(stalledAt))
	sb.WriteString(" passing over ")
	sb.WriteString(itoa(threshold))
	sb.WriteString(" iters; ")
	if len(merged) == 0 {
		sb.WriteString("no files modified")
	} else {
		// Sort merged keys for deterministic output.
		keys := make([]string, 0, len(merged))
		for f := range merged {
			keys = append(keys, f)
		}
		sort.Strings(keys)
		sb.WriteString("file churn: ")
		for i, f := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(f)
			sb.WriteString("×")
			sb.WriteString(itoa(merged[f]))
		}
		if churn && churnFile != "" {
			sb.WriteString(" (top: ")
			sb.WriteString(churnFile)
			sb.WriteString("×")
			sb.WriteString(itoa(churnCount))
			sb.WriteString(")")
		}
	}

	return []Signal{{
		Pattern: PatternNoProgress,
		Detail:  sb.String(),
		Count:   threshold,
	}}
}

// collectNoProgressStats walks events in order and aggregates per-iteration
// verify + edit stats. Returns one entry per iteration_start (in order).
// Events before the first iteration_start are dropped on the floor — the
// detector only fires after iter boundaries are in the window, which is
// what we want for "stuck" semantics.
func collectNoProgressStats(events []event.Event) []noProgressIterStat {
	var stats []noProgressIterStat
	var cur *noProgressIterStat
	// inVerify tracks whether we're currently between a verify_run and the
	// next non-verify event. While true, verify_result events accumulate.
	inVerify := false

	closeIter := func() {
		if cur != nil {
			stats = append(stats, *cur)
			cur = nil
		}
		inVerify = false
	}

	for i := 0; i < len(events); i++ {
		e := events[i]
		switch e.Type {
		case "iteration_start":
			closeIter()
			m := parseData(e)
			iter := int(float64Field(m, "iter"))
			cur = &noProgressIterStat{iter: iter, files: map[string]int{}}
		case "verify_run":
			if cur != nil {
				cur.hasVerifyRun = true
				inVerify = true
			}
		case "verify_result":
			if cur != nil && inVerify {
				m := parseData(e)
				if boolField(m, "passed") {
					cur.verifyPassing++
				}
			}
		case "tool_call":
			inVerify = false
			if cur == nil {
				continue
			}
			m := parseData(e)
			name := strField(m, "name")
			if !isEditTool(name) {
				continue
			}
			// Pair this tool_call with the next matching tool_result to know
			// whether the edit succeeded. Only count successful edits.
			path := extractEditedPath(name, strField(m, "input"))
			if path == "" {
				continue
			}
			// Look ahead for tool_result with same name.
			succeeded := false
			for j := i + 1; j < len(events); j++ {
				r := events[j]
				if r.Type != "tool_result" {
					continue
				}
				rm := parseData(r)
				if strField(rm, "name") != name {
					continue
				}
				succeeded = !boolField(rm, "is_error")
				break
			}
			if succeeded {
				cur.files[path]++
			}
		default:
			// Any other event closes the verify-result accumulation window
			// (we only count verify_result events that immediately follow a
			// verify_run, before any other event type).
			if e.Type != "verify_run" && e.Type != "verify_result" {
				inVerify = false
			}
		}
	}
	closeIter()
	return stats
}

// isEditTool reports whether name corresponds to a file-mutating tool.
func isEditTool(name string) bool {
	switch name {
	case "edit", "write_file", "apply_patch":
		return true
	}
	return false
}

// extractEditedPath returns a representative file path from a tool_call
// input JSON. Best-effort; returns "" when no path can be derived. For
// tools with multi-file inputs (edit, apply_patch) we return the FIRST
// path seen — sufficient for churn detection on the typical "agent keeps
// hammering one file" pattern; multi-file edits are rare in stuck loops.
func extractEditedPath(toolName, inputJSON string) string {
	if inputJSON == "" {
		return ""
	}
	switch toolName {
	case "write_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(inputJSON), &args); err == nil && args.Path != "" {
			return args.Path
		}
	case "edit":
		var args struct {
			Blocks string `json:"blocks"`
		}
		if err := json.Unmarshal([]byte(inputJSON), &args); err == nil && args.Blocks != "" {
			return firstFilenameFromEditBlocks(args.Blocks)
		}
	case "apply_patch":
		var args struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal([]byte(inputJSON), &args); err == nil && args.Patch != "" {
			return firstFilenameFromPatch(args.Patch)
		}
	}
	return ""
}

// firstFilenameFromEditBlocks scans an edit-tool "blocks" string for the
// first filename (the line immediately preceding "<<<<<<< SEARCH"). Strips
// an optional "path: " prefix for codex-compat.
func firstFilenameFromEditBlocks(blocks string) string {
	lines := strings.Split(blocks, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "<<<<<<< SEARCH") && i > 0 {
			cand := strings.TrimSpace(lines[i-1])
			cand = strings.TrimPrefix(cand, "path: ")
			cand = strings.TrimPrefix(cand, "path:")
			cand = strings.TrimSpace(cand)
			if cand != "" {
				return cand
			}
		}
	}
	return ""
}

// firstFilenameFromPatch scans an apply_patch DSL string for the first
// "*** Add File: ", "*** Update File: ", or "*** Delete File: " marker.
func firstFilenameFromPatch(patch string) string {
	for _, ln := range strings.Split(patch, "\n") {
		t := strings.TrimSpace(ln)
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
			if strings.HasPrefix(t, prefix) {
				name := strings.TrimSpace(strings.TrimPrefix(t, prefix))
				// Strip trailing "*** Move to: ..." that can appear on the
				// same line in some malformed inputs (defensive).
				if i := strings.Index(name, "*** Move to:"); i >= 0 {
					name = strings.TrimSpace(name[:i])
				}
				if name != "" {
					return name
				}
			}
		}
	}
	return ""
}

// itoa is a minimal integer-to-string helper to avoid importing fmt/strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
