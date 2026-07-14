// Package bedrock wraps AWS Bedrock inference behind a small Generator
// interface. Handlers depend on the interface, so they test against a fake with
// no AWS calls and models or providers swap without touching handler code. The
// AWS-specific request and response unpacking lives here and nowhere else.
package bedrock

import "context"

// Generator produces a completion for a prompt. It is the seam between the
// handler and Bedrock: the handler holds a Generator, not a concrete client, so
// tests can substitute a fake and production can substitute the real client.
type Generator interface {
	Generate(ctx context.Context, prompt string) (Completion, error)

	// GenerateStream streams a completion as it is producted. It returns a
	// receive-only channel of chunks the caller ranges over until it closes.
	// The producer owns the channel and closes it when generation ends or ctx
	// is cancelled, so a client disconnect stops the upstream call.
	GenerateStream(ctx context.Context, prompt string) (<-chan Chunk, error)
}

// Completion is the result of one generation. It carries the generated
// text plus the input and output token counts read from the Bedrock response,
// which the meter turns into a per-request cost. It deliberately exposes only
// what callers need, insulating them from the AWS SDK's response shape.
type Completion struct {
	Text      string
	TokensIn  int
	TokensOut int
}

// Chunk is one increment of a streamed completion. A text chunk carries the
// next piece of generated output in Txt. The final chunk carries the token
// counts (Text empty), which the meter turns into a per-request cost. Splitting
// them this way lets the handler relay text immediately and emit one usage
// summary after the stream ends.
type Chunk struct {
	Text      string
	TokensIn  int
	TokensOut int
}
