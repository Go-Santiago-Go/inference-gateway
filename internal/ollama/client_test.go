package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Go-Santiago-Go/inference-gateway/internal/provider"
)

// newFakeOllama starts a server that responds to /api/chat with the given body
// and records the decoded request. Testing against a real Ollama install would
// make these tests depend on a model being pulled locally, so CI would either
// skip them or download gigabytes.
func newFakeOllama(t *testing.T, status int, body string, got *chatRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		if got != nil {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decoding request: %v", err)
			}
		}
		w.WriteHeader(status)
		// Flush between lines so the streaming tests observe chunks arriving
		// separately rather than as one buffered write.
		for line := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
			w.Write([]byte(line + "\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// drain collects every chunk from ch, returning the concatenated text and the
// last chunk's token counts, which is exactly what the handler does.
func drain(ch <-chan provider.Chunk) (text string, in, out int) {
	for c := range ch {
		text += c.Text
		if c.TokensIn != 0 || c.TokensOut != 0 {
			in, out = c.TokensIn, c.TokensOut
		}
	}
	return text, in, out
}

func TestGenerateReturnsTextAndTokenCounts(t *testing.T) {
	const body = `{"message":{"role":"assistant","content":"blue sky"},"done":true,` +
		`"prompt_eval_count":61,"eval_count":468}`

	var req chatRequest
	srv := newFakeOllama(t, http.StatusOK, body, &req)

	comp, err := New(srv.URL, "llama3.2").Generate(context.Background(),
		[]provider.Message{{Role: "user", Text: "hi"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if comp.Text != "blue sky" {
		t.Errorf("Text = %q, want %q", comp.Text, "blue sky")
	}
	// The mapping from Ollama's phase-named counters onto the neutral type is
	// the adapter's whole job, and getting them backwards would silently
	// mis-price every request.
	if comp.TokensIn != 61 || comp.TokensOut != 468 {
		t.Errorf("tokens = (%d, %d), want (61, 468)", comp.TokensIn, comp.TokensOut)
	}
	if req.Stream {
		t.Error("Generate sent stream:true, want false")
	}
	if req.Model != "llama3.2" {
		t.Errorf("Model = %q, want llama3.2", req.Model)
	}
}

func TestGenerateStreamRelaysTextThenUsage(t *testing.T) {
	const body = `
{"message":{"role":"assistant","content":"The"},"done":false}
{"message":{"role":"assistant","content":" sky"},"done":false}
{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":61,"eval_count":468}`

	var req chatRequest
	srv := newFakeOllama(t, http.StatusOK, body, &req)

	ch, err := New(srv.URL, "llama3.2").GenerateStream(context.Background(),
		[]provider.Message{{Role: "user", Text: "hi"}})
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}

	text, in, out := drain(ch)
	if text != "The sky" {
		t.Errorf("text = %q, want %q", text, "The sky")
	}
	if in != 61 || out != 468 {
		t.Errorf("tokens = (%d, %d), want (61, 468)", in, out)
	}
	if !req.Stream {
		t.Error("GenerateStream sent stream:false, want true")
	}
}

// The terminal object carries empty content. Relaying it would push an empty
// SSE frame to the browser, so the adapter must drop it.
func TestGenerateStreamDropsEmptyFinalContent(t *testing.T) {
	const body = `
{"message":{"role":"assistant","content":"hi"},"done":false}
{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":1,"eval_count":2}`

	srv := newFakeOllama(t, http.StatusOK, body, nil)

	ch, err := New(srv.URL, "llama3.2").GenerateStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}

	var chunks []provider.Chunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (one text, one usage)", len(chunks))
	}
	if chunks[0].Text != "hi" {
		t.Errorf("chunks[0].Text = %q, want %q", chunks[0].Text, "hi")
	}
	if chunks[1].Text != "" || chunks[1].TokensOut != 2 {
		t.Errorf("chunks[1] = %+v, want empty text with counts", chunks[1])
	}
}

// A cancelled context must end the stream and close the channel, otherwise the
// producer goroutine leaks for every disconnected client.
func TestGenerateStreamStopsOnCancel(t *testing.T) {
	// Never sends a done object, so the stream only ends via cancellation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			if _, err := w.Write([]byte(`{"message":{"content":"x"},"done":false}` + "\n")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := New(srv.URL, "llama3.2").GenerateStream(ctx, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}

	<-ch // one chunk proves the stream is live
	cancel()

	done := make(chan struct{})
	go func() {
		for range ch { //nolint:revive // draining until close is the assertion
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after cancel; producer goroutine leaked")
	}
}

// A failing backend must return an error rather than an empty completion, since
// that error is the signal the router uses to fail over.
func TestGenerateSurfacesHTTPError(t *testing.T) {
	srv := newFakeOllama(t, http.StatusNotFound, `{"error":"model not found"}`, nil)

	_, err := New(srv.URL, "nope").Generate(context.Background(), nil)
	if err == nil {
		t.Fatal("Generate returned nil error for a 404")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error = %q, want it to carry the server's message", err)
	}
}

func TestGenerateStreamSurfacesOpenError(t *testing.T) {
	srv := newFakeOllama(t, http.StatusInternalServerError, `{"error":"boom"}`, nil)

	ch, err := New(srv.URL, "llama3.2").GenerateStream(context.Background(), nil)
	if err == nil {
		t.Fatal("GenerateStream returned nil error for a 500")
	}
	if ch != nil {
		t.Error("GenerateStream returned a non-nil channel alongside an error")
	}
}

func TestToOllamaMessagesCoercesUnknownRole(t *testing.T) {
	got := toOllamaMessages([]provider.Message{
		{Role: "assistant", Text: "a"},
		{Role: "system", Text: "b"},
	})
	if got[0].Role != "assistant" {
		t.Errorf("role[0] = %q, want assistant", got[0].Role)
	}
	if got[1].Role != "user" {
		t.Errorf("role[1] = %q, want user (unknown roles coerce to user)", got[1].Role)
	}
}
