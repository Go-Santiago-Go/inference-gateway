package breaker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Go-Santiago-Go/inference-gateway/internal/provider"
)

var errUpstream = errors.New("throttled")

// fakeGen fails while err is set and counts how often it was actually reached,
// which is what proves the breaker stopped calling it.
type fakeGen struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeGen) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeGen) Generate(context.Context, []provider.Message) (provider.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return provider.Completion{}, f.err
	}
	return provider.Completion{Text: "ok"}, nil
}

func (f *fakeGen) GenerateStream(context.Context, []provider.Message) (<-chan provider.Chunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan provider.Chunk)
	close(ch)
	return ch, nil
}

func (f *fakeGen) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// clock returns a breaker whose time source the test controls, so cooldown
// behavior is asserted without sleeping.
func clock(b *Breaker) (advance func(time.Duration)) {
	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
}

func TestClosedPassesCallsThrough(t *testing.T) {
	gen := &fakeGen{}
	b := New("test", gen, 3, time.Minute)

	comp, err := b.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if comp.Text != "ok" {
		t.Errorf("Text = %q, want the wrapped backend's output", comp.Text)
	}
	if b.State() != Closed {
		t.Errorf("state = %v, want closed", b.State())
	}
}

func TestOpensAfterThresholdAndStopsCalling(t *testing.T) {
	gen := &fakeGen{err: errUpstream}
	b := New("test", gen, 3, time.Minute)
	clock(b)

	for range 3 {
		if _, err := b.Generate(context.Background(), nil); !errors.Is(err, errUpstream) {
			t.Fatalf("err = %v, want the upstream error while closed", err)
		}
	}
	if b.State() != Open {
		t.Fatalf("state = %v, want open after 3 failures", b.State())
	}

	// The point of the breaker: the next call must not reach the backend at all.
	_, err := b.Generate(context.Background(), nil)
	if !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen", err)
	}
	if gen.callCount() != 3 {
		t.Errorf("backend called %d times, want 3: an open breaker must not call it", gen.callCount())
	}
}

// Consecutive is what matters. A backend failing intermittently must not trip,
// because the router already absorbs the occasional failure.
func TestSuccessResetsTheFailureCount(t *testing.T) {
	gen := &fakeGen{err: errUpstream}
	b := New("test", gen, 3, time.Minute)
	clock(b)

	b.Generate(context.Background(), nil)
	b.Generate(context.Background(), nil)

	gen.setErr(nil)
	b.Generate(context.Background(), nil)

	gen.setErr(errUpstream)
	b.Generate(context.Background(), nil)
	b.Generate(context.Background(), nil)

	if b.State() != Closed {
		t.Errorf("state = %v, want closed: two failures either side of a success is not three in a row", b.State())
	}
}

func TestHalfOpenProbeClosesTheCircuitOnSuccess(t *testing.T) {
	gen := &fakeGen{err: errUpstream}
	b := New("test", gen, 2, 30*time.Second)
	advance := clock(b)

	b.Generate(context.Background(), nil)
	b.Generate(context.Background(), nil)
	if b.State() != Open {
		t.Fatalf("state = %v, want open", b.State())
	}

	// Still inside the cooldown: refused without a call.
	before := gen.callCount()
	if _, err := b.Generate(context.Background(), nil); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen during cooldown", err)
	}
	if gen.callCount() != before {
		t.Error("backend was called during the cooldown")
	}

	advance(30 * time.Second)
	gen.setErr(nil)

	if _, err := b.Generate(context.Background(), nil); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if b.State() != Closed {
		t.Errorf("state = %v, want closed after a successful probe", b.State())
	}
}

func TestHalfOpenProbeReopensOnFailure(t *testing.T) {
	gen := &fakeGen{err: errUpstream}
	b := New("test", gen, 2, 30*time.Second)
	advance := clock(b)

	b.Generate(context.Background(), nil)
	b.Generate(context.Background(), nil)

	advance(30 * time.Second)

	// The probe is admitted and fails, which must reopen immediately rather than
	// counting toward the threshold again.
	if _, err := b.Generate(context.Background(), nil); !errors.Is(err, errUpstream) {
		t.Fatalf("err = %v, want the probe to reach the backend", err)
	}
	if b.State() != Open {
		t.Fatalf("state = %v, want open after a failed probe", b.State())
	}

	// And the cooldown restarts, so the next call is refused again.
	if _, err := b.Generate(context.Background(), nil); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen after the cooldown restarted", err)
	}
}

// Only one caller may probe. The rest must fail fast, or a recovering backend
// receives the full stampede the breaker exists to prevent.
func TestHalfOpenAdmitsOneProbeOnly(t *testing.T) {
	gen := &fakeGen{err: errUpstream}
	b := New("test", gen, 1, 30*time.Second)
	advance := clock(b)

	b.Generate(context.Background(), nil)
	advance(30 * time.Second)

	// Block the probe inside the backend so the breaker stays half-open while a
	// second caller arrives.
	release := make(chan struct{})
	blocking := &blockingGen{inner: gen, release: release, entered: make(chan struct{})}
	b.gen = blocking

	var wg sync.WaitGroup
	wg.Go(func() {
		b.Generate(context.Background(), nil)
	})

	<-blocking.entered
	if _, err := b.Generate(context.Background(), nil); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen while a probe is in flight", err)
	}
	close(release)
	wg.Wait()
}

// blockingGen holds a call open until released, so a test can observe the
// breaker's state while exactly one request is in flight.
type blockingGen struct {
	inner   *fakeGen
	release chan struct{}
	entered chan struct{}
}

func (g *blockingGen) Generate(ctx context.Context, m []provider.Message) (provider.Completion, error) {
	close(g.entered)
	<-g.release
	return g.inner.Generate(ctx, m)
}

func (g *blockingGen) GenerateStream(ctx context.Context, m []provider.Message) (<-chan provider.Chunk, error) {
	return g.inner.GenerateStream(ctx, m)
}

// A client hitting Stop must never trip the breaker: the call failed because the
// gateway abandoned it, not because the backend is unhealthy.
func TestCancelledContextDoesNotTrip(t *testing.T) {
	gen := &fakeGen{err: context.Canceled}
	b := New("test", gen, 2, time.Minute)
	clock(b)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for range 5 {
		b.Generate(ctx, nil)
	}
	if b.State() != Closed {
		t.Errorf("state = %v, want closed: cancellations are not backend failures", b.State())
	}
}

func TestGenerateStreamOpenFailuresTrip(t *testing.T) {
	gen := &fakeGen{err: errUpstream}
	b := New("test", gen, 2, time.Minute)
	clock(b)

	b.GenerateStream(context.Background(), nil)
	b.GenerateStream(context.Background(), nil)

	ch, err := b.GenerateStream(context.Background(), nil)
	if !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen", err)
	}
	// A nil channel alongside the error, since ranging a non-nil one that never
	// closes would hang the handler.
	if ch != nil {
		t.Error("GenerateStream returned a channel alongside ErrOpen")
	}
}

func TestThresholdBelowOneIsRaised(t *testing.T) {
	b := New("test", &fakeGen{}, 0, time.Minute)
	if _, err := b.Generate(context.Background(), nil); err != nil {
		t.Fatalf("a zero threshold must not reject the first call: %v", err)
	}
}

func TestStateString(t *testing.T) {
	for state, want := range map[State]string{Closed: "closed", Open: "open", HalfOpen: "half_open"} {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}
