package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mindungil/gil/core/intent"
	"github.com/mindungil/gil/sdk"
)

// chatVerbResultMsg is the generic envelope for a verb that produces
// one transcript line. Async sdk.Client calls return one of these so
// Update can append the rendered text without knowing per-verb shapes.
//
// `kind` is purely cosmetic (drives the leading glyph in render);
// `text` is already pre-formatted by the cmd factory.
type chatVerbResultMsg struct {
	kind string // "ok" | "err"
	text string
}

// chatNewSessionMsg fires after a successful sdk.CreateSession so the
// chat model can flip activeID and prepend the session into the
// rolling list before announcing the switch.
type chatNewSessionMsg struct {
	session *sdk.Session
	err     string
}

// dispatchVerb routes one router-classified verb. Returns the tea.Cmd
// to run (nil for inline-only verbs). Writes the immediate "→ rationale"
// or guard text directly into m.transcript so the user always sees the
// router's decision before any async network reply lands.
//
// Verbs needing network (sessions/spec/status/diff/run/new) emit the
// arrow note then return a cmd; the cmd posts a chatVerbResultMsg
// (or chatNewSessionMsg) which Update converts into a transcript line.
//
// Verbs without network (help/switch/quit) finish synchronously.
func (m *chatModel) dispatchVerb(cl intent.Classification) tea.Cmd {
	switch cl.Verb {
	case intent.VerbQuit:
		if m.stream.cancel != nil {
			m.stream.cancel()
		}
		return tea.Quit

	case intent.VerbHelp:
		m.transcript = append(m.transcript,
			"   → "+cl.Rationale,
			"   "+helpLine())
		return nil

	case intent.VerbSwitch:
		target := cl.Args["target"]
		if target == "" {
			m.transcript = append(m.transcript,
				"   ?  switch needs a target — id or slug")
			return nil
		}
		m.activeID = target
		// New session ID → reset the inInterview flag so the next
		// prompt opens with StartInterview (daemon will resume if
		// the target already has interview state) instead of
		// jumping straight to ReplyInterview against a possibly-
		// fresh session.
		m.inInterview = false
		// Cancel any in-flight stream so it doesn't bleed into the
		// next session's transcript.
		if m.stream.cancel != nil {
			m.stream.cancel()
			m.stream = chatStreamState{}
		}
		m.transcript = append(m.transcript,
			"   → switched to "+shortChatID(target))
		return nil

	case intent.VerbSessions:
		m.transcript = append(m.transcript, "   → "+cl.Rationale)
		if m.client == nil {
			return nil
		}
		return fetchSessionsCmd(m.client)

	case intent.VerbNew:
		m.transcript = append(m.transcript, "   → "+cl.Rationale)
		if m.client == nil {
			return nil
		}
		return newSessionCmd(m.client)

	case intent.VerbSpec:
		m.transcript = append(m.transcript, "   → "+cl.Rationale)
		if m.client == nil || m.activeID == "" {
			return nil
		}
		return fetchSpecCmd(m.client, m.activeID)

	case intent.VerbStatus:
		m.transcript = append(m.transcript, "   → "+cl.Rationale)
		if m.client == nil || m.activeID == "" {
			return nil
		}
		return fetchStatusCmd(m.client, m.activeID)

	case intent.VerbDiff:
		m.transcript = append(m.transcript, "   → "+cl.Rationale)
		if m.client == nil || m.activeID == "" {
			return nil
		}
		return fetchDiffCmd(m.client, m.activeID)

	case intent.VerbMerge:
		// Confirmation prompt UX in TUI is a separate followup
		// (interactive modal). For now keep the dispatch visible
		// and direct users to the cli surface for the actual merge.
		m.transcript = append(m.transcript,
			"   ?  merge needs a confirmation prompt — not yet wired in TUI; use `gil chat` for now")
		return nil

	case intent.VerbRun:
		if m.phase != ChatPhaseAwaitingConfirm {
			m.transcript = append(m.transcript,
				"   ?  spec is not ready to freeze yet — keep iterating with prompts")
			return nil
		}
		m.transcript = append(m.transcript, "   → "+cl.Rationale)
		if m.client == nil || m.activeID == "" {
			return nil
		}
		return startRunCmd(m.client, m.activeID)
	}
	return nil
}

// helpLine is the one-liner echoed for VerbHelp. Mirrors the cli REPL's
// help text so a user moving between surfaces sees the same vocabulary.
func helpLine() string {
	return "talk in plain language — gil routes it. slash escape-hatch: /sessions /switch /new /spec /status /diff /run /quit /help"
}

// shortChatID truncates an ID to its 10-char ms-precision ULID prefix
// so transcript lines stay scannable. Mirrors cli/internal/chat/repl's
// shortID; duplicated locally to avoid a cross-module import.
func shortChatID(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

// --- async cmd factories -------------------------------------------

const verbCmdTimeout = 5 * time.Second

func fetchSessionsCmd(client *sdk.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verbCmdTimeout)
		defer cancel()
		list, err := client.ListSessions(ctx, 20)
		if err != nil {
			return chatVerbResultMsg{kind: "err", text: "sessions: " + err.Error()}
		}
		if len(list) == 0 {
			return chatVerbResultMsg{kind: "ok", text: "no sessions — describe what you want to build"}
		}
		var b strings.Builder
		for i, s := range list {
			if i > 0 {
				b.WriteString("\n")
			}
			label := s.GoalHint
			if label == "" {
				label = "—"
			}
			fmt.Fprintf(&b, "%d. %s  %s  %s",
				i+1, shortChatID(s.ID), strings.ToLower(s.Status), label)
		}
		return chatVerbResultMsg{kind: "ok", text: b.String()}
	}
}

func newSessionCmd(client *sdk.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verbCmdTimeout)
		defer cancel()
		sess, err := client.CreateSession(ctx, sdk.CreateOptions{})
		if err != nil {
			return chatNewSessionMsg{err: err.Error()}
		}
		return chatNewSessionMsg{session: sess}
	}
}

func fetchSpecCmd(client *sdk.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verbCmdTimeout)
		defer cancel()
		fs, err := client.GetSpec(ctx, sessionID)
		if err != nil {
			return chatVerbResultMsg{kind: "err", text: "spec: " + err.Error()}
		}
		m := protojson.MarshalOptions{Multiline: true, Indent: "  ", EmitUnpopulated: false}
		out, err := m.Marshal(fs)
		if err != nil {
			return chatVerbResultMsg{kind: "err", text: "spec marshal: " + err.Error()}
		}
		return chatVerbResultMsg{kind: "ok", text: string(out)}
	}
}

func fetchStatusCmd(client *sdk.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verbCmdTimeout)
		defer cancel()
		s, err := client.GetSession(ctx, sessionID)
		if err != nil {
			return chatVerbResultMsg{kind: "err", text: "status: " + err.Error()}
		}
		return chatVerbResultMsg{kind: "ok", text: fmt.Sprintf("%s · %s · iter %d · $%.4f",
			shortChatID(s.ID), strings.ToLower(s.Status), s.CurrentIteration, s.TotalCostUSD)}
	}
}

func fetchDiffCmd(client *sdk.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verbCmdTimeout)
		defer cancel()
		d, err := client.Diff(ctx, sessionID)
		if err != nil {
			return chatVerbResultMsg{kind: "err", text: "diff: " + err.Error()}
		}
		if d == nil || strings.TrimSpace(d.UnifiedDiff) == "" {
			return chatVerbResultMsg{kind: "ok", text: "no diff — workspace clean"}
		}
		return chatVerbResultMsg{kind: "ok", text: d.UnifiedDiff}
	}
}

// chatRunStartedMsg fires after a successful StartRun so chatModel
// can kick off the Tail subscription. Carries the sessionID separately
// from the verb-result text so Update knows where to dial.
type chatRunStartedMsg struct{ sessionID string }

// chatRunStartFailedMsg surfaces a StartRun error to the transcript;
// the caller treats this just like chatVerbResultMsg{kind:"err"} but
// the typed shape lets future error UX (retry button, etc.) hook in.
type chatRunStartFailedMsg struct{ err string }

func startRunCmd(client *sdk.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verbCmdTimeout)
		defer cancel()
		_, err := client.StartRun(ctx, sessionID, "", "", false)
		if err != nil {
			return chatRunStartFailedMsg{err: err.Error()}
		}
		return chatRunStartedMsg{sessionID: sessionID}
	}
}

