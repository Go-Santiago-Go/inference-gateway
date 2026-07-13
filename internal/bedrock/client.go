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
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{
		api:     bedrockruntime.NewFromConfig(cfg),
		modelID: modelID,
	}, nil
}

// Generate sends the prompt to Bedrock's Converse API and returns the completion
// with its token counts. The context flows into the SDK call, so a client
// disconnect cancels the in-flight request instead of paying for a dropped
// response.
func (c *Client) Generate(ctx context.Context, prompt string) (Completion, error) {
	// Converse models a chat turn as a message holding content blocks; send one
	// user message with a single text block.
	out, err := c.api.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(c.modelID),
		Messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: prompt},
				},
			},
		},
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
