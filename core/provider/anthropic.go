package provider

import (
	"context"
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
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case RoleAssistant:
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
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
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Temperature > 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic.Complete: %w", err)
	}

	var text string
	for _, b := range msg.Content {
		// Extract text from each content block using the AsText method
		if b.Type == "text" {
			textBlock := b.AsText()
			text += textBlock.Text
		}
	}
	return Response{
		Text:         text,
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
		StopReason:   string(msg.StopReason),
	}, nil
}
