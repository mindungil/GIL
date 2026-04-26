package stuck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/jedutools/gil/core/event"
)

// Pattern identifies a category of stuck behaviour.
type Pattern int

const (
	PatternUnknown                   Pattern = iota
	PatternRepeatedActionObservation         // same tool_call+tool_result content 4+ times in window
	PatternRepeatedActionError               // same tool_call followed by error tool_result 3+ times
	PatternMonologue                         // 3+ consecutive provider_response with zero tool_calls
	PatternPingPong                          // strict alternation between two distinct tool signatures 6+ events
	PatternContextWindowError               // 2+ run_error events mentioning context/token overflow
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
