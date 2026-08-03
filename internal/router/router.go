// Package router fans a generation request across ordered backends and returns
// the first success. It satisfies provider.Generator itself, so the handler
// holds a single Generator whether the gateway fronts one backend or several,
// and adding a backend is a wiring change in main rather than a handler change.
//
// It names no concrete backend. Bedrock and Ollama are both just Generators
// here, which is what keeps a third provider from ever touching this file.
package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
	"github.com/Go-Santiago-Go/inference-gateway/internal/provider"
)

// Compile-time check that Router satisfies provider.Generator. This is the
// composite: a Generator whose members are themselves Generators.
var _ provider.Generator = (*Router)(nil)

// Backend pairs a Generator with a short name. The name exists so a failover is
// attributable to a specific provider in the logs rather than to "upstream".
type Backend struct {
	Name string
	Gen  provider.Generator
}

// Router tries its backends in order and serves the first that succeeds. The
// zero value is not useful; construct one with New.
type Router struct {
	backends []Backend
}

// New returns a Router that tries backends in the order given. The first is the
// primary; each later one is attempted only after every earlier one has failed.
func New(backends ...Backend) *Router {
	return &Router{backends: backends}
}

// Generate returns the first backend's completion that succeeds, trying each in
// order. Every backend sees the same context, so a client disconnect stops the
// attempt in flight.
func (r *Router) Generate(ctx context.Context, messages []provider.Message) (provider.Completion, error) {
	var errs []error
	for i, b := range r.backends {
		comp, err := b.Gen.Generate(ctx, messages)
		if err == nil {
			logFailover(ctx, b.Name, i, errs)
			return comp, nil
		}
		if ctx.Err() != nil {
			// The caller went away, so this failure is the cancellation itself.
			// Advancing to the next backend would start a fresh generation for a
			// client that is no longer listening, which is the token burn the
			// context propagation exists to prevent.
			return provider.Completion{}, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", b.Name, err))
	}
	return provider.Completion{}, exhausted(errs)
}

// GenerateStream returns the first backend's stream that opens successfully,
// trying each in order.
//
// Failover covers the open only. Once a stream is returned its chunks are
// relayed straight to the client, so a mid-completion failure surfaces as the
// channel closing early rather than as an error, and it is deliberately not
// retried: a second generation would not continue the first, so the client would
// see one answer contradict another.
func (r *Router) GenerateStream(ctx context.Context, messages []provider.Message) (<-chan provider.Chunk, error) {
	var errs []error
	for i, b := range r.backends {
		ch, err := b.Gen.GenerateStream(ctx, messages)
		if err == nil {
			logFailover(ctx, b.Name, i, errs)
			return ch, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", b.Name, err))
	}
	return nil, exhausted(errs)
}

// logFailover records that a non-primary backend served the request. It is
// silent for the primary, so the log line appears only when something actually
// degraded and is therefore worth alerting on.
func logFailover(ctx context.Context, name string, index int, errs []error) {
	if index == 0 {
		return
	}
	middleware.LoggerFromContext(ctx).Warn("provider failover",
		"served_by", name,
		"failed", errors.Join(errs...),
	)
}

// exhausted turns the collected per-backend failures into one error. It joins
// rather than returning only the last, so errors.Is and errors.As still match
// against any backend's error and the log line names every provider that failed.
func exhausted(errs []error) error {
	if len(errs) == 0 {
		return errors.New("router: no backends configured")
	}
	return fmt.Errorf("router: all backends failed: %w", errors.Join(errs...))
}
