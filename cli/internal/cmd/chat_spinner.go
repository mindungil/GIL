package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/credstore"
)

// startSpinner prints an in-place Braille spinner with a dim label and
// returns a stop function. Calling stop is idempotent and erases the
// spinner line so the next agent message lands cleanly.
//
// Used by the chat REPL during the LLM round-trip. The aesthetic spec
// (terminal-aesthetic.md §3) specifies Braille as the spinner family —
// distinctive, matches the mission-control aesthetic, low CPU.
//
// Phase 25 S4: addresses the "freezes silently while LLM thinks" UX
// regression where users thought the chat had hung and pressed ctrl-C.
func startSpinner(out io.Writer, p uistyle.Palette, label string) func() {
	// Only run on a TTY — piped/captured stdout has no \r overwrite,
	// so a spinner there just spams every frame line by line.
	if !writerIsTTY(out) {
		return func() {}
	}
	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	stop := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})

	go func() {
		defer close(done)
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				// Erase: \r + spaces + \r so the next print starts fresh.
				fmt.Fprint(out, "\r"+strings.Repeat(" ", 40)+"\r")
				return
			case <-ticker.C:
				fmt.Fprintf(out, "\r  %s %s ",
					p.Info(string(frames[i%len(frames)])),
					p.Dim(label))
				i++
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

// promptModelInline asks the user to pick a default model when their
// credential lacks one (typical for vllm registered before the Phase
// 25 wizard). Reads one line from stdin and returns it; empty input
// returns "" so the caller can show a friendly hint.
func promptModelInline(cmd *cobra.Command, in io.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, providerName string) string {
	hint := "qwen3.6-27b"
	switch providerName {
	case "anthropic":
		hint = "claude-haiku-4-5"
	case "openai":
		hint = "gpt-4o-mini"
	case "openrouter":
		hint = "anthropic/claude-haiku-4-5"
	}
	fmt.Fprintln(out, agentLine(p, g, p.Caution("Your "+providerName+" credential has no default model.")))
	fmt.Fprintln(out, agentLine(p, g, p.Dim("I'll remember the one you pick now.")))
	fmt.Fprintln(out, agentLine(p, g, ""))
	fmt.Fprintf(out, "  %s Model name (e.g. %s): %s ", p.Dim(g.QuoteBar), p.Surface(hint), p.Info("›"))
	r := bufio.NewReader(in)
	line, _ := r.ReadString('\n')
	picked := strings.TrimSpace(line)
	if picked == "" {
		return ""
	}
	fmt.Fprintln(out, agentLine(p, g, p.Surface("Saved.")))
	fmt.Fprintln(out)
	return picked
}

// saveModelToCred reads the current credential, sets Model, and writes
// back. No-op when the credential doesn't exist.
func saveModelToCred(cmd *cobra.Command, providerName, model string) error {
	cred := credentialFor(cmd, credstore.ProviderName(providerName))
	if cred == nil {
		return nil
	}
	cred.Model = model
	store := newStoreFor(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return store.Set(ctx, credstore.ProviderName(providerName), *cred)
}

// writerIsTTY reports whether out is an interactive terminal — the
// in-place \r overwrite trick only works on a TTY. Piped/captured
// stdout (tests, scripts, gild logs) gets a no-op spinner.
func writerIsTTY(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
