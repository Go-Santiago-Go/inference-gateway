package handler

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Go-Santiago-Go/inference-gateway/internal/bedrock"
)

// fakeGenerator satisfies bedrock.Generator without any AWS call. It returns the
// completion and error it was constructed with, so a test controls exactly what
// "Bedrock" returns.
type fakeGenerator struct {
	comp bedrock.Completion
	err  error
}

func (f fakeGenerator) Generate(ctx context.Context, prompt string) (bedrock.Completion, error) {
	return f.comp, f.err
}

// GenerateStream mimics the streaming producer with no AWS call: it emits the
// fake completion's text as one chunk, then a usage chunk carrying the token
// counts, then closes. This lets the streaming handler be tested offline. It
// honors f.err so a test can drive the start-of-stream error path.
func (f fakeGenerator) GenerateStream(ctx context.Context, prompt string) (<-chan bedrock.Chunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan bedrock.Chunk)
	go func() {
		defer close(ch)
		ch <- bedrock.Chunk{Text: f.comp.Text}
		ch <- bedrock.Chunk{TokensIn: f.comp.TokensIn, TokensOut: f.comp.TokensOut}
	}()
	return ch, nil
}

func TestChat(t *testing.T) {
	gen := fakeGenerator{comp: bedrock.Completion{Text: "hello", TokensIn: 1500, TokensOut: 800}}
	h := New(gen, "us.anthropic.claude-haiku-4-5-20251001-v1:0")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"prompt":"hi"}`))
	rec := httptest.NewRecorder()

	h.Chat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp chatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello")
	}
	if math.Abs(resp.CostUSD-0.0055) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", resp.CostUSD, 0.0055)
	}
}

// TestChatGeneratorError drives the path a caller hits when Bedrock stays
// throttled and withRetry exhausts its attempts: the error must surface as a
// 502, naming the upstream as the failure, not the gateway.
func TestChatGeneratorError(t *testing.T) {
	gen := fakeGenerator{err: errors.New("bedrock: throttled after 3 attempts")}
	h := New(gen, "us.anthropic.claude-haiku-4-5-20251001-v1:0")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"prompt":"hi"}`))
	rec := httptest.NewRecorder()

	h.Chat(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	// The upstream error must not leak to the caller: it can name models,
	// account IDs, or internals the client has no business seeing.
	if body := rec.Body.String(); strings.Contains(body, "throttled after 3 attempts") {
		t.Errorf("body leaks the upstream error: %q", body)
	}
}

// TestChatStreamGeneratorError asserts the stream can still fail honestly. The
// SSE headers are set before the generator is called, so this pins the ordering
// that keeps them uncommitted: if a frame or flush ever moves above the call,
// the status locks to 200 and the error becomes unreportable.
func TestChatStreamGeneratorError(t *testing.T) {
	gen := fakeGenerator{err: errors.New("bedrock: throttled after 3 attempts")}
	h := New(gen, "us.anthropic.claude-haiku-4-5-20251001-v1:0")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"prompt":"hi"}`))
	rec := httptest.NewRecorder()

	h.ChatStream(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	// A failed open is a plain error response, not a stream: the client must not
	// be told to parse SSE from a body that will never carry frames.
	if ct := rec.Header().Get("Content-Type"); ct == "text/event-stream" {
		t.Errorf("Content-Type = %q, want a plain error response", ct)
	}
}

// TestChatStream exercises the SSE path end to end against the fake: the body
// must carry the token text as a data frame and end with a named usage frame
// whose metered fields match the fake's token counts. No AWS is involved.
func TestChatStream(t *testing.T) {
	gen := fakeGenerator{comp: bedrock.Completion{Text: "hello", TokensIn: 1500, TokensOut: 800}}
	h := New(gen, "us.anthropic.claude-haiku-4-5-20251001-v1:0")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"prompt":"hi"}`))
	rec := httptest.NewRecorder()

	h.ChatStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// The SSE content type is the wire contract; without it a client would not
	// treat the response as a stream.
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}

	body := rec.Body.String()

	// The token text must arrive as its own data frame, terminated by the blank
	// line that ends an SSE event.
	if !strings.Contains(body, "data: hello\n\n") {
		t.Errorf("body missing token frame; got:\n%s", body)
	}

	// A named usage frame must follow the tokens. Split on the event marker and
	// decode its JSON payload so the metered fields can be asserted, not just
	// pattern-matched.
	const marker = "event: usage\ndata: "
	_, payload, found := strings.Cut(body, marker)
	if !found {
		t.Fatalf("body missing usage frame; got:\n%s", body)
	}
	payload, _, _ = strings.Cut(payload, "\n\n") // trim the frame's blank-line terminator

	var usage chatResponse
	if err := json.Unmarshal([]byte(payload), &usage); err != nil {
		t.Fatalf("decoding usage frame %q: %v", payload, err)
	}
	if usage.TokensIn != 1500 || usage.TokensOut != 800 {
		t.Errorf("tokens = (%d, %d), want (1500, 800)", usage.TokensIn, usage.TokensOut)
	}
	// Same price table as TestChat: 1500/1000*0.001 + 800/1000*0.005 = 0.0055.
	if math.Abs(usage.CostUSD-0.0055) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", usage.CostUSD, 0.0055)
	}
}
