package tool

import (
	"context"
	"encoding/json"
)

// CompactRequester is implemented by anything that can schedule a compaction
// (the AgentLoop). Defined locally to avoid an import cycle.
type CompactRequester interface {
	RequestCompact()
}

// CompactNow is an agent-callable tool that asks the runner to compact the
// conversation at the next iteration boundary. Returns immediately.
type CompactNow struct {
	Requester CompactRequester
}

const compactSchema = `{"type":"object","properties":{"reason":{"type":"string","description":"why you want to compact"}}}`

func (c *CompactNow) Name() string { return "compact_now" }

func (c *CompactNow) Description() string {
	return "Request that the conversation history be compacted before the next iteration. Use when context is filling up but you still have work to do."
}

func (c *CompactNow) Schema() json.RawMessage { return json.RawMessage(compactSchema) }

func (c *CompactNow) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	if c.Requester != nil {
		c.Requester.RequestCompact()
	}
	return Result{Content: "compaction requested; will run before next iteration"}, nil
}
