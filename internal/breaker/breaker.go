// Package breaker wraps a provider.Generator in a circuit breaker, so a backend
// that is failing stops being called until it has had time to recover.
//
// It exists because failover alone is not enough. A router without a breaker
// pays the failing backend's full timeout on every request before moving on, so
// an outage turns into sustained latency rather than a fast degradation, and the
// struggling backend keeps receiving the traffic that is keeping it down.
//
// A Breaker is itself a Generator, so it composes with the router: the router
// sees an ordinary backend and never learns a breaker is involved.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Go-Santiago-Go/inference-gateway/internal/metrics"
	"github.com/Go-Santiago-Go/inference-gateway/internal/provider"
)

// Compile-time check that Breaker satisfies provider.Generator, which is what
// lets it be substituted for the backend it wraps.
var _ provider.Generator = (*Breaker)(nil)

// ErrOpen is returned when the breaker rejected a call without attempting it.
// Callers match it with errors.Is to tell a refused call from a real upstream
// failure; the router treats both the same way and moves to the next backend.
var ErrOpen = errors.New("breaker: circuit open")

// State is a breaker's position in its lifecycle.
type State int

const (
	// Closed passes calls through and counts consecutive failures.
	Closed State = iota
	// Open rejects calls without attempting them, until the cooldown elapses.
	Open
	// HalfOpen admits a single probe to test whether the backend recovered.
	HalfOpen
)

// String returns the state's lowercase name, suitable for a log field or a
// metric label.
func (s State) String() string {
	switch s {
	case Open:
		return "open"
	case HalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// Breaker is a Generator that stops calling the backend it wraps once that
// backend has failed threshold times in a row. The zero value is not useful;
// construct one with New.
type Breaker struct {
	name      string
	gen       provider.Generator
	threshold int
	cooldown  time.Duration

	// now is a seam for tests, which need to advance past the cooldown without
	// sleeping. Production always uses time.Now.
	now func() time.Time

	mu       sync.Mutex
	state    State
	failures int
	openedAt time.Time
}

// New returns a Breaker around gen that opens after threshold consecutive
// failures and admits a probe once cooldown has elapsed. A threshold below one
// is raised to one, since a breaker that opens on no failures would reject
// every call.
//
// The name labels this backend's metrics and must come from a small fixed set,
// since every distinct value creates its own time series.
func New(name string, gen provider.Generator, threshold int, cooldown time.Duration) *Breaker {
	if threshold < 1 {
		threshold = 1
	}
	b := &Breaker{
		name:      name,
		gen:       gen,
		threshold: threshold,
		cooldown:  cooldown,
		now:       time.Now,
	}
	// Publish the starting state so the gauge exists from boot. Without it the
	// series appears only on the first transition, and a dashboard panel reads
	// "No data" for a healthy backend, which is indistinguishable from a broken
	// scrape.
	metrics.SetCircuitState(name, int(Closed))
	return b
}

// State reports the breaker's current state. It is safe for concurrent use and
// is intended for logging and metrics rather than for control flow, which the
// breaker handles itself.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Generate calls the wrapped backend unless the circuit is open, in which case
// it returns ErrOpen without making the call.
func (b *Breaker) Generate(ctx context.Context, messages []provider.Message) (provider.Completion, error) {
	if err := b.allow(); err != nil {
		metrics.RecordProviderAttempt(b.name, metrics.OutcomeRejected)
		return provider.Completion{}, err
	}
	comp, err := b.gen.Generate(ctx, messages)
	b.record(ctx, err)
	return comp, err
}

// GenerateStream opens a stream on the wrapped backend unless the circuit is
// open, in which case it returns ErrOpen without making the call.
//
// Only the open counts toward the breaker. A stream that dies mid-completion
// surfaces as the channel closing rather than as an error, so the breaker cannot
// observe it, and the same constraint that stops the router failing over
// mid-stream applies here.
func (b *Breaker) GenerateStream(ctx context.Context, messages []provider.Message) (<-chan provider.Chunk, error) {
	if err := b.allow(); err != nil {
		metrics.RecordProviderAttempt(b.name, metrics.OutcomeRejected)
		return nil, err
	}
	ch, err := b.gen.GenerateStream(ctx, messages)
	b.record(ctx, err)
	return ch, err
}

// allow reports whether a call may proceed, moving the breaker from Open to
// HalfOpen when the cooldown has elapsed. The transition happens here rather
// than on a timer so the breaker needs no background goroutine and no shutdown.
func (b *Breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case Open:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return fmt.Errorf("%w: %d consecutive failures", ErrOpen, b.failures)
		}
		// Cooldown elapsed. This caller becomes the probe, and moving to HalfOpen
		// under the lock is what makes it the only one: every other caller now
		// takes the HalfOpen branch and is refused.
		b.setState(HalfOpen)
		return nil

	case HalfOpen:
		// A probe is already in flight. Admitting more would stampede a backend
		// that may still be down, which is the behavior the breaker exists to
		// prevent.
		return fmt.Errorf("%w: probing", ErrOpen)

	default:
		return nil
	}
}

// record folds a call's outcome into the breaker's state.
func (b *Breaker) record(ctx context.Context, err error) {
	// A cancelled context means the caller went away, so the call failed because
	// the gateway abandoned it, not because the backend is unhealthy. Counting
	// these would let a burst of clients hitting Stop trip the breaker and take a
	// working backend out of service.
	if ctx.Err() != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err != nil {
		metrics.RecordProviderAttempt(b.name, metrics.OutcomeError)
		b.failures++
		// A failed probe reopens immediately regardless of the threshold: the
		// cooldown just expired and the backend is still broken, so there is
		// nothing to count toward.
		if b.state == HalfOpen || b.failures >= b.threshold {
			b.setState(Open)
			b.openedAt = b.now()
		}
		return
	}

	metrics.RecordProviderAttempt(b.name, metrics.OutcomeSuccess)
	// Consecutive is the operative word: any success resets the count, so a
	// backend failing one call in ten never trips. The breaker is for sustained
	// failure, and the router already handles the occasional one.
	b.failures = 0
	b.setState(Closed)
}

// setState moves the breaker to s and publishes the change. The caller must hold
// b.mu, which is what keeps the gauge from reporting a state the breaker is not
// actually in.
func (b *Breaker) setState(s State) {
	b.state = s
	metrics.SetCircuitState(b.name, int(s))
}
