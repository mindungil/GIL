package stuck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/event"
	"github.com/stretchr/testify/require"
)

// TestChat_DetectorFiresNoProgressOnChessTraces is the dead-wiring
// guard from the A1b spec (docs/superpowers/specs/2026-05-19-a1b-
// detector-chat-wiring-design.md, Section 5). It reconstructs
// synthetic chat-emit events from the 2026-05-19 chess N=5 trace
// JSONL files (the ones that produced 0/5 PASS, 5/5 premature_stop
// at T=0.3) and asserts Detector.Check returns PatternNoProgress for
// at least 3 of 5 traces.
//
// If this test fails, A1b should NOT be deployed to chess re-
// measurement until threshold tuning or event reconstruction is
// fixed — see [[feedback_check_production_wiring]].
//
// Path is overridden via A1B_CHESS_TRACE_DIR; default
// /tmp/gil-variance-probe-3310234 (the baseline N=5 run).
func TestChat_DetectorFiresNoProgressOnChessTraces(t *testing.T) {
	dir := os.Getenv("A1B_CHESS_TRACE_DIR")
	if dir == "" {
		dir = "/tmp/gil-variance-probe-3310234"
	}
	matches, err := filepath.Glob(filepath.Join(dir, "07-chess-r*.jsonl"))
	require.NoError(t, err)
	if len(matches) == 0 {
		t.Skipf("no chess traces under %s — set A1B_CHESS_TRACE_DIR to a directory with 07-chess-r*.jsonl files", dir)
	}

	det := &Detector{}
	fired := 0
	for _, p := range matches {
		events := reconstructChessEvents(t, p)
		sigs := det.Check(events)
		for _, s := range sigs {
			if s.Pattern == PatternNoProgress {
				fired++
				break
			}
		}
	}
	require.GreaterOrEqual(t, fired, 3,
		"Detector must fire PatternNoProgress on >= 3/5 chess traces (got %d/%d); A1b's chat-path wiring would be dead-wiring otherwise",
		fired, len(matches))
}

// reconstructChessEvents reads a dogfood trace JSONL and emits the
// events the chat-path P67b emit sites would have produced. Each
// per-turn record becomes:
//
//	iteration_start{iter}
//	tool_call{name:"write_file", input:{path:"main.go", content:"x<iter>"}}
//	tool_result{name:"write_file", is_error:false, content:""}
//	verify_run
//	tool_call{name:"verify", input:{}}
//	verify_result{passed: false}      // stop_reason=verify_missing → verify still failing
//	tool_result{name:"verify", is_error:true, content:<verify_tail snippet>}
//
// content of write_file varies per iter so the "files churning" half
// of NoProgress is satisfied. passed=false is hardcoded because every
// turn in the baseline traces was a verify_missing or end_turn with
// FAIL — that's what made the boundary boundary.
func reconstructChessEvents(t *testing.T, path string) []event.Event {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []event.Event
	iter := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var d map[string]any
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			continue
		}
		if _, isSummary := d["summary"]; isSummary {
			continue
		}
		if _, hasTurn := d["turn"]; !hasTurn {
			continue
		}
		iter++
		out = append(out, event.Event{
			Type: "iteration_start",
			Data: mustJSON(map[string]any{"iter": iter}),
		})
		out = append(out, event.Event{
			Type: "tool_call",
			Data: mustJSON(map[string]any{
				"name":  "write_file",
				"input": fmt.Sprintf(`{"path":"main.go","content":"x%d"}`, iter),
			}),
		})
		out = append(out, event.Event{
			Type: "tool_result",
			Data: mustJSON(map[string]any{
				"name": "write_file", "is_error": false, "content": "",
			}),
		})
		out = append(out, event.Event{Type: "verify_run"})
		out = append(out, event.Event{
			Type: "tool_call",
			Data: mustJSON(map[string]any{"name": "verify", "input": "{}"}),
		})
		out = append(out, event.Event{
			Type: "verify_result",
			Data: mustJSON(map[string]any{"passed": false}),
		})
		out = append(out, event.Event{
			Type: "tool_result",
			Data: mustJSON(map[string]any{
				"name": "verify", "is_error": true, "content": "FAIL",
			}),
		})
	}
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
