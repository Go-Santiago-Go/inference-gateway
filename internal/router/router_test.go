package router

import (
	"context"
	"errors"
	"testing"

	"github.com/Go-Santiago-Go/inference-gateway/internal/provider"
)

// fakeGen is a Generator whose outcome the test chooses, and which counts its
// calls so a test can assert a backend was never reached.
type fakeGen struct {
	text  string
	err   error
	calls int
}

func (f *fakeGen) Generate(context.Context, []provider.Message) (provider.Completion, error) {
	f.calls++
	if f.err != nil {
		return provider.Completion{}, f.err
	}
	return provider.Completion{Text: f.text, TokensIn: 1, TokensOut: 2}, nil
}

func (f *fakeGen) GenerateStream(context.Context, []provider.Message) (<-chan provider.Chunk, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Text: f.text}
	close(ch)
	return ch, nil
}

func TestGenerateUsesPrimaryAndSkipsTheRest(t *testing.T) {
	primary := &fakeGen{text: "from bedrock"}
	fallback := &fakeGen{text: "from ollama"}

	comp, err := New(
		Backend{Name: "bedrock", Gen: primary},
		Backend{Name: "ollama", Gen: fallback},
	).Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if comp.Text != "from bedrock" {
		t.Errorf("Text = %q, want the primary's output", comp.Text)
	}
	// A healthy primary must cost nothing on the fallback. Calling both would
	// double every bill and defeat the point of ordering them.
	if fallback.calls != 0 {
		t.Errorf("fallback called %d times, want 0 while the primary is healthy", fallback.calls)
	}
}

func TestGenerateFallsBackWhenPrimaryFails(t *testing.T) {
	primary := &fakeGen{err: errors.New("throttled")}
	fallback := &fakeGen{text: "from ollama"}

	comp, err := New(
		Backend{Name: "bedrock", Gen: primary},
		Backend{Name: "ollama", Gen: fallback},
	).Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if comp.Text != "from ollama" {
		t.Errorf("Text = %q, want the fallback's output", comp.Text)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Errorf("calls = (%d, %d), want (1, 1)", primary.calls, fallback.calls)
	}
}

func TestGenerateJoinsEveryFailure(t *testing.T) {
	errPrimary := errors.New("throttled")
	errFallback := errors.New("connection refused")

	_, err := New(
		Backend{Name: "bedrock", Gen: &fakeGen{err: errPrimary}},
		Backend{Name: "ollama", Gen: &fakeGen{err: errFallback}},
	).Generate(context.Background(), nil)
	if err == nil {
		t.Fatal("Generate returned nil error with every backend failing")
	}

	// Joining rather than keeping only the last failure is what lets a caller
	// match on any backend's error and what puts every provider in the log line.
	if !errors.Is(err, errPrimary) {
		t.Error("joined error lost the primary's cause")
	}
	if !errors.Is(err, errFallback) {
		t.Error("joined error lost the fallback's cause")
	}
}

// A cancelled context means the client is gone, so trying the next backend would
// generate tokens nobody will read.
func TestGenerateDoesNotFailOverOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primary := &fakeGen{err: context.Canceled}
	fallback := &fakeGen{text: "from ollama"}

	_, err := New(
		Backend{Name: "bedrock", Gen: primary},
		Backend{Name: "ollama", Gen: fallback},
	).Generate(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if fallback.calls != 0 {
		t.Errorf("fallback called %d times after cancellation, want 0", fallback.calls)
	}
}

func TestGenerateStreamFallsBackWhenOpenFails(t *testing.T) {
	primary := &fakeGen{err: errors.New("throttled")}
	fallback := &fakeGen{text: "hello"}

	ch, err := New(
		Backend{Name: "bedrock", Gen: primary},
		Backend{Name: "ollama", Gen: fallback},
	).GenerateStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}

	var text string
	for c := range ch {
		text += c.Text
	}
	if text != "hello" {
		t.Errorf("text = %q, want the fallback's output", text)
	}
}

func TestGenerateStreamReturnsNilChannelWhenAllFail(t *testing.T) {
	ch, err := New(
		Backend{Name: "bedrock", Gen: &fakeGen{err: errors.New("throttled")}},
		Backend{Name: "ollama", Gen: &fakeGen{err: errors.New("refused")}},
	).GenerateStream(context.Background(), nil)
	if err == nil {
		t.Fatal("GenerateStream returned nil error with every backend failing")
	}
	// The handler distinguishes an open failure from an empty stream by the
	// error, so returning a usable channel alongside one would be a trap.
	if ch != nil {
		t.Error("GenerateStream returned a non-nil channel alongside an error")
	}
}

// An empty Router is a wiring mistake, and it must surface as an error rather
// than as a zero-value completion that looks like a successful empty answer.
func TestEmptyRouterErrors(t *testing.T) {
	_, err := New().Generate(context.Background(), nil)
	if err == nil {
		t.Fatal("empty Router returned nil error")
	}
}
