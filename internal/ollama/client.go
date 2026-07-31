// Package ollama implements the provider.Generator interface against a local
// Ollama server's /api/chat endpoint. It is the gateway's fallback backend, so
// a Bedrock outage degrades to a local model instead of to an error.
//
// It uses net/http and encoding/json directly rather than Ollama's client
// library: the wire format is one POST and a JSON decode loop, so a dependency
// would cost more in version churn than it saves in code. The Bedrock adapter
// takes the opposite decision for the opposite reason, since request signing is
// real complexity worth delegating.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Go-Santiago-Go/inference-gateway/internal/provider"
)

// Compile-time check that Client satisfies provider.Generator, so a signature
// drift fails the build here rather than at the wiring call site in main.
var _ provider.Generator = (*Client)(nil)

// Client is the Ollama provider: it calls a local Ollama server's /api/chat
// endpoint. It satisfies provider.Generator, so the router and the handlers
// treat it interchangeably with the Bedrock client.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// New returns a Client that generates with the given model against the Ollama
// server at baseURL, for example "http://localhost:11434".
func New(baseURL, model string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		// No Timeout: it bounds the entire exchange including reading the
		// response body, so any value would sever a long completion mid-stream.
		// Cancellation comes from the caller's context instead, which stops the
		// read the moment the client disconnects.
		http: &http.Client{},
	}
}

// chatMessage is one turn in Ollama's request and response shape.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the body of POST /api/chat. Stream selects between a single
// response object and a sequence of them.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// chatResponse is one object from an /api/chat response. Streaming emits a
// sequence of these: intermediate objects carry a content fragment with
// Done false, and exactly one final object carries empty content, Done true,
// and the token counts. Non-streaming returns a single object with the whole
// completion and the counts together, so one struct decodes both modes.
type chatResponse struct {
	Message chatMessage `json:"message"`
	Done    bool        `json:"done"`

	// Ollama names its token counts after the phases that produce them:
	// prompt evaluation is the input, and generation ("eval") is the output.
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

// toOllamaMessages maps the interface's Message slice onto Ollama's message
// shape. An unrecognized role falls back to user, matching the Bedrock adapter
// so both backends coerce malformed input identically.
func toOllamaMessages(messages []provider.Message) []chatMessage {
	out := make([]chatMessage, len(messages))
	for i, m := range messages {
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		out[i] = chatMessage{Role: role, Content: m.Text}
	}
	return out
}

// post sends a chat request and returns the response with its body unread, so
// callers choose whether to decode one object or stream a sequence. A non-200
// is converted to an error here, so neither caller has to check status.
func (c *Client) post(ctx context.Context, body chatRequest) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: encode request: %w", err)
	}

	// NewRequestWithContext, not NewRequest: the context is what cancels the
	// in-flight call when the gateway's client disconnects.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Read a bounded prefix of the error body for the log line, then close:
		// an unclosed body leaks the connection instead of returning it to the
		// pool, and the limit keeps a hostile response from filling memory.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return resp, nil
}

// Generate sends the conversation to Ollama and returns the completion with its
// token counts. The context flows into the HTTP call, so a client disconnect
// cancels the in-flight generation.
//
// Unlike the Bedrock adapter there is no retry here. Ollama is the fallback, and
// the router already moves on when a backend fails, so retrying inside the
// adapter would only delay that decision.
func (c *Client) Generate(ctx context.Context, messages []provider.Message) (provider.Completion, error) {
	resp, err := c.post(ctx, chatRequest{
		Model:    c.model,
		Messages: toOllamaMessages(messages),
		Stream:   false,
	})
	if err != nil {
		return provider.Completion{}, err
	}
	defer resp.Body.Close()

	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return provider.Completion{}, fmt.Errorf("ollama: decode response: %w", err)
	}

	return provider.Completion{
		Text:      out.Message.Content,
		TokensIn:  out.PromptEvalCount,
		TokensOut: out.EvalCount,
		Model:     c.model,
	}, nil
}

// GenerateStream calls Ollama with streaming enabled and relays the model's
// output over a channel as it is generated. It returns immediately; a background
// goroutine decodes the response body and sends a final chunk carrying the token
// counts before closing. The channel closes when the model finishes, ctx is
// cancelled, or the stream errors, so a client disconnect stops the upstream
// call instead of generating tokens no one will read.
func (c *Client) GenerateStream(ctx context.Context, messages []provider.Message) (<-chan provider.Chunk, error) {
	// Opening the stream can fail synchronously (server down, unknown model);
	// surface that as an ordinary error before any goroutine exists so the
	// router can fail over and the handler can still set a response status.
	resp, err := c.post(ctx, chatRequest{
		Model:    c.model,
		Messages: toOllamaMessages(messages),
		Stream:   true,
	})
	if err != nil {
		return nil, err
	}

	// Unbuffered, matching the Bedrock adapter: each send blocks until the
	// handler receives, so the producer cannot race ahead of the consumer and
	// chunk delivery stays tied to the handler's flush cadence.
	ch := make(chan provider.Chunk)

	go func() {
		// LIFO: close(ch) runs first to end the handler's range, then the body
		// is released. Both are deferred so they fire on every exit path
		// (normal end, cancellation, or a decode error), never leaking.
		defer resp.Body.Close()
		defer close(ch)

		// Ollama frames the stream as JSON objects separated by newlines.
		// json.Decoder reads one value at a time from an io.Reader, so a single
		// decoder consumes the whole sequence. This is why it is preferred over
		// scanning lines by hand: bufio.Scanner caps how long one line may be
		// and would fail on an unusually large object.
		dec := json.NewDecoder(resp.Body)
		for {
			var out chatResponse
			if err := dec.Decode(&out); err != nil {
				// io.EOF at a clean end of stream, or a read error, or the
				// context being cancelled under the connection. All three mean
				// the same thing to the consumer: no more chunks.
				return
			}

			// The terminal object carries empty content, so guarding here keeps
			// an empty SSE frame from reaching the client.
			if out.Message.Content != "" {
				// select, not a bare send: if the client has disconnected the
				// handler is no longer receiving, and on an unbuffered channel a
				// plain send would block forever. ctx.Done() lets us abandon the
				// stream instead of leaking this goroutine.
				select {
				case ch <- provider.Chunk{Text: out.Message.Content, Model: c.model}:
				case <-ctx.Done():
					return
				}
			}

			if out.Done {
				// One chunk with token counts and no text, which the handler
				// turns into the trailing usage frame.
				select {
				case ch <- provider.Chunk{
					TokensIn:  out.PromptEvalCount,
					TokensOut: out.EvalCount,
					Model:     c.model,
				}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return ch, nil
}
