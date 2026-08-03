// Package provider defines the seam between the gateway and the model backends
// it can call. It holds the Generator interface and the neutral request and
// response types, so each backend package and each consumer depends on this
// package rather than on one another.
package provider

import "context"

// Message is one turn of a conversation passed to the model: a role
// ("user" or "assistant") and its text. A single-turn request is a slice of one
// user Message; a multi-turn request carries the alternating history, which the
// stateless gateway resends in full each turn because no backend holds a session.
type Message struct {
	Role string
	Text string
}

// Generator produces a completion for a conversation. It is the seam between the
// handler and a model backend: the handler holds a Generator, not a concrete
// client, so tests substitute a fake, production substitutes a real backend, and
// a router substitutes several.
type Generator interface {
	Generate(ctx context.Context, messages []Message) (Completion, error)

	// GenerateStream streams a completion as it is produced. It returns a
	// receive-only channel of chunks the caller ranges over until it closes.
	// The producer owns the channel and closes it when generation ends or ctx
	// is cancelled, so a client disconnect stops the upstream call.
	GenerateStream(ctx context.Context, messages []Message) (<-chan Chunk, error)
}

// Completion is the result of one generation. It carries the generated text plus
// the input and output token counts reported by the backend, which the meter
// turns into a per-request cost. It deliberately exposes only what callers need,
// insulating them from any one backend's response shape.
type Completion struct {
	Text      string
	TokensIn  int
	TokensOut int

	// Model names the model that actually produced this completion. A router may
	// serve a request from a fallback backend, so which model answered is a
	// runtime fact rather than configuration, and pricing depends on it. Callers
	// treat an empty value as "the model I configured".
	Model string
}

// Chunk is one increment of a streamed completion. A text chunk carries the next
// piece of generated output in Text. The final chunk carries the token counts
// (Text empty), which the meter turns into a per-request cost. Splitting them
// this way lets the handler relay text immediately and emit one usage summary
// after the stream ends.
type Chunk struct {
	Text      string
	TokensIn  int
	TokensOut int

	// Model names the model producing this stream, repeated on every chunk so a
	// consumer can attribute the first token without waiting for the stream to
	// end. See Completion.Model for why it cannot be taken from configuration.
	Model string
}
