package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic is a Provider backed by the Anthropic Messages API via anthropic-sdk-go.
type Anthropic struct {
	client anthropic.Client
}

// NewAnthropic returns an Anthropic provider configured with the given API key.
// If apiKey is empty, the SDK reads ANTHROPIC_API_KEY from the env automatically.
func NewAnthropic(apiKey string) *Anthropic {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &Anthropic{client: anthropic.NewClient(opts...)}
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "anthropic" }

// Complete sends the request to the Anthropic Messages API and returns the
// assistant's response with usage tokens.
func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	if req.Model == "" {
		return Response{}, errors.New("anthropic.Complete: model required")
	}

	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			// If this message has tool results, build a content array with tool_result blocks
			if len(m.ToolResults) > 0 {
				blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolResults))
				for _, tr := range m.ToolResults {
					blocks = append(blocks, anthropic.NewToolResultBlock(tr.ToolUseID, tr.Content, tr.IsError))
				}
				if m.CacheControl {
					blocks[len(blocks)-1] = withCacheControl(blocks[len(blocks)-1])
				}
				msgs = append(msgs, anthropic.NewUserMessage(blocks...))
			} else if m.Content != "" {
				block := anthropic.NewTextBlock(m.Content)
				if m.CacheControl {
					block = withCacheControl(block)
				}
				msgs = append(msgs, anthropic.NewUserMessage(block))
			} else {
				// Empty user message with no content or tool results; skip
				continue
			}
		case RoleAssistant:
			// If this message has tool calls, build a content array with tool_use blocks
			if len(m.ToolCalls) > 0 {
				blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+1)
				// Add text if present
				if m.Content != "" {
					blocks = append(blocks, anthropic.NewTextBlock(m.Content))
				}
				// Add each tool call
				for _, tc := range m.ToolCalls {
					// Unmarshal the Input to pass as an object
					var inputObj interface{}
					if err := json.Unmarshal(tc.Input, &inputObj); err != nil {
						inputObj = tc.Input
					}
					blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, inputObj, tc.Name))
				}
				if m.CacheControl {
					blocks[len(blocks)-1] = withCacheControl(blocks[len(blocks)-1])
				}
				msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
			} else if m.Content != "" {
				block := anthropic.NewTextBlock(m.Content)
				if m.CacheControl {
					block = withCacheControl(block)
				}
				msgs = append(msgs, anthropic.NewAssistantMessage(block))
			}
		}
		// system is handled separately below
	}

	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 4096
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: maxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		sb := anthropic.TextBlockParam{Text: req.System}
		if req.SystemCacheControl {
			sb.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		params.System = []anthropic.TextBlockParam{sb}
	}
	if req.Temperature > 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	// Add tools to the request if any are provided
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
		for _, td := range req.Tools {
			// Unmarshal schema from json.RawMessage into a map
			var schemaObj map[string]interface{}
			if err := json.Unmarshal(td.Schema, &schemaObj); err != nil {
				return Response{}, fmt.Errorf("anthropic.Complete: invalid tool schema for %s: %w", td.Name, err)
			}

			schema := anthropic.ToolInputSchemaParam{
				Properties: schemaObj,
			}

			toolParam := &anthropic.ToolParam{
				Name:        td.Name,
				Description: anthropic.String(td.Description),
				InputSchema: schema,
			}

			tools = append(tools, anthropic.ToolUnionParam{OfTool: toolParam})
		}
		params.Tools = tools
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic.Complete: %w", err)
	}

	var text string
	var toolCalls []ToolCall

	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			textBlock := b.AsText()
			text += textBlock.Text
		case "tool_use":
			toolUseBlock := b.AsToolUse()
			toolCalls = append(toolCalls, ToolCall{
				ID:    toolUseBlock.ID,
				Name:  toolUseBlock.Name,
				Input: toolUseBlock.Input,
			})
		}
	}

	return Response{
		Text:         text,
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
		ToolCalls:    toolCalls,
		StopReason:   string(msg.StopReason),
	}, nil
}

// withCacheControl returns the content block with an ephemeral cache_control
// marker attached. It handles the known variants that support CacheControl.
func withCacheControl(block anthropic.ContentBlockParamUnion) anthropic.ContentBlockParamUnion {
	cc := anthropic.NewCacheControlEphemeralParam()
	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = cc
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = cc
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = cc
	case block.OfImage != nil:
		block.OfImage.CacheControl = cc
	}
	return block
}
