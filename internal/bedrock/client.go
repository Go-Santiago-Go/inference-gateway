package bedrock

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Compile-time check that Client satisfies the Generator interface. If the
// method signature ever drifts, this fails to build instead of at a call site.
var _ Generator = (*Client)(nil)

// Client is the production Generator: it calls AWS Bedrock's Converse API. It
// satisfies the Generator interface, so handlers depend on the interface and
// this concrete type is injected only at wiring in main.
type Client struct {
	api     *bedrockruntime.Client
	modelID string
}

// New loads AWS configuration from the environment (credentials and region) and
// returns a Client that generates with the given Bedrock model ID.
func New(ctx context.Context, modelID string) (*Client, error) {
	// The SDK retries by default (retry.Standard, 3 attempts). Left on, it would
	// nest under withRetry for up to 9 calls per request on two stacked backoff
	// schedules. One attempt here makes withRetry the single source of retry
	// behavior, so maxAttempts means what it says.
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRetryMaxAttempts(1),
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		api:     bedrockruntime.NewFromConfig(cfg),
		modelID: modelID,
	}, nil
}

// toBedrockMessages maps the interface's Message slice onto the SDK's message
// shape: one content block of text per turn, with the role translated. An
// unrecognized role falls back to user, the only role a single-turn caller sends.
func toBedrockMessages(messages []Message) []types.Message {
	out := make([]types.Message, len(messages))
	for i, m := range messages {
		role := types.ConversationRoleUser
		if m.Role == "assistant" {
			role = types.ConversationRoleAssistant
		}
		out[i] = types.Message{
			Role:    role,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Text}},
		}
	}
	return out
}

// Generate sends the conversation to Bedrock's Converse API and returns the
// completion with its token counts. The context flows into the SDK call, so a
// client disconnect cancels the in-flight request instead of paying for a
// dropped response.
func (c *Client) Generate(ctx context.Context, messages []Message) (Completion, error) {
	// out is assigned by the closure rather than returned, because withRetry's fn
	// signature carries only an error.
	var out *bedrockruntime.ConverseOutput
	err := withRetry(ctx, func() error {
		var err error
		out, err = c.api.Converse(ctx, &bedrockruntime.ConverseInput{
			ModelId:  aws.String(c.modelID),
			Messages: toBedrockMessages(messages),
		})
		return err
	})
	if err != nil {
		return Completion{}, err
	}

	// out.Output is a union of possible output shapes. Assert the message
	// variant (the comma-ok form errors instead of panicking on a surprise
	// shape) so we can read its content blocks, which are themselves a union.
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return Completion{}, fmt.Errorf("bedrock: unexpected output type %T", out.Output)
	}

	// Each content block is itself a union (text, image, tool use). Range over
	// them and concatenate the text blocks, skipping any non-text block. A loop
	// rather than Content[0] handles a response split across several blocks.
	var text string
	for _, block := range msg.Value.Content {
		if t, ok := block.(*types.ContentBlockMemberText); ok {
			text += t.Value
		}
	}

	// Token counts are optional (*int32); aws.ToInt32 safely dereferences them,
	// returning 0 for a nil pointer instead of panicking.
	tokensIn := int(aws.ToInt32(out.Usage.InputTokens))
	tokensOut := int(aws.ToInt32(out.Usage.OutputTokens))

	return Completion{
		Text:      text,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
	}, nil
}

// GenerateStream calls Bedrock's ConverseStream and relays the model's output
// over a channel as it is generated. It returns immediately; a background
// goroutine pumps text chunks into the channel and sends a final chunk carrying
// the token counts before closing. The channel closes when the model finishes,
// ctx is cancelled, or the stream errors, so a client disconnect stops the
// upstream call instead of paying for tokens no one will read.
func (c *Client) GenerateStream(ctx context.Context, messages []Message) (<-chan Chunk, error) {
	// Starting the stream can fail synchronously (bad model ID, auth); surface
	// that as an ordinary error before any goroutine exists so the handler can
	// still set a response status. The Messages shape matches Generate exactly.
	//
	// Only the open is retried. Once deltas are flowing the client has already
	// received tokens, and Bedrock cannot resume mid-completion, so a retry would
	// regenerate from scratch and duplicate or contradict what was already sent.
	// Mid-stream failures end the stream instead.
	var out *bedrockruntime.ConverseStreamOutput
	err := withRetry(ctx, func() error {
		var err error
		out, err = c.api.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
			ModelId:  aws.String(c.modelID),
			Messages: toBedrockMessages(messages),
		})
		return err
	})
	if err != nil {
		return nil, err
	}

	// Unbuffered: each send blocks until the handler receives, so the producer
	// cannot race ahead of the consumer. This backpressure is what keeps the
	// relay honest and ties chunk delivery to the handler's flush cadence.
	ch := make(chan Chunk)

	// Read the Bedrock event stream in the background and return ch immediately,
	// so the handler starts relaying and flushing chunks while the model is
	// still generating. Draining the stream here first would collect every token
	// before returning and defeat streaming.
	go func() {
		stream := out.GetStream()
		// LIFO: close(ch) runs first to end the handler's range, then the SDK
		// stream is released. Both are deferred so they fire on every exit path
		// (normal end, cancellation, or a mid-stream error), never leaking.
		defer stream.Close()
		defer close(ch)

		for event := range stream.Events() {
			// Each event is a union; we care about incremental text deltas and
			// the terminal metadata event carrying the token usage.
			switch e := event.(type) {
			case *types.ConverseStreamOutputMemberContentBlockDelta:
				text, ok := e.Value.Delta.(*types.ContentBlockDeltaMemberText)
				if !ok {
					continue // non-text delta (tool use, etc.); nothing to relay
				}
				// select, not a bare send: if the client has disconnected the
				// handler is no longer receiving, and on an unbuffered channel a
				// plain send would block forever. ctx.Done() lets us abandon the
				// stream instead of leaking this goroutine.
				select {
				case ch <- Chunk{Text: text.Value}:
				case <-ctx.Done():
					return
				}
			case *types.ConverseStreamOutputMemberMetadata:
				// The final usage event: emit one chunk with token counts and no
				// text, which the handler turns into the trailing usage frame.
				if u := e.Value.Usage; u != nil {
					select {
					case ch <- Chunk{
						TokensIn:  int(aws.ToInt32(u.InputTokens)),
						TokensOut: int(aws.ToInt32(u.OutputTokens)),
					}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch, nil
}
