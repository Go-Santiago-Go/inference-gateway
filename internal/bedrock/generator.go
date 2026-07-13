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
