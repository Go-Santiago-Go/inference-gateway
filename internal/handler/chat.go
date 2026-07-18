// Package handler contains the HTTP handlers for the gateway. Handlers stay
// thin: they parse the request, delegate generation to a bedrock.Generator, and
// write the response. Cross-cutting concerns live in internal/middleware.
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Go-Santiago-Go/inference-gateway/internal/bedrock"
	"github.com/Go-Santiago-Go/inference-gateway/internal/meter"
	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
)

// Handler serves the chat endpoint. It depends on the bedrock.Generator
// interface, not a concrete client, so it can be tested against a fake and the
// model can be swapped without changing handler code.
type Handler struct {
	gen   bedrock.Generator
	model string // Bedrock model ID this handler fronts; used to price and log each request.
}

// New returns a Handler that delegates generation to gen. The model string names
// the Bedrock model the handler fronts and is used to price and label every
// request it serves.
func New(gen bedrock.Generator, model string) *Handler {
	return &Handler{gen: gen, model: model}
}

// chatMessage is one turn in the request: a role ("user" or "assistant") and its
// text. The client resends the whole history each turn because the gateway is
// stateless.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the JSON body accepted by POST /v1/chat.
type chatRequest struct {
	Messages []chatMessage `json:"messages"`
}

// toMessages validates the request's conversation and maps it onto the bedrock
// message type. It returns false when the conversation is unusable: empty, a turn
// with no content, or a final turn that is not the user's (there would be nothing
// new to answer). Validating here keeps a malformed body from reaching Bedrock.
func (req chatRequest) toMessages() ([]bedrock.Message, bool) {
	if len(req.Messages) == 0 {
		return nil, false
	}
	msgs := make([]bedrock.Message, len(req.Messages))
	for i, m := range req.Messages {
		if m.Content == "" || (m.Role != "user" && m.Role != "assistant") {
			return nil, false
		}
		msgs[i] = bedrock.Message{Role: m.Role, Text: m.Content}
	}
	if req.Messages[len(req.Messages)-1].Role != "user" {
		return nil, false
	}
	return msgs, true
}

// chatResponse is the successful JSON response: the completion text plus the
// per-request usage the client's metrics footer renders. Text is omitempty so
// the streaming path can reuse this struct for its final usage frame, which
// carries only the metered fields and no text.
type chatResponse struct {
	Text      string  `json:"text,omitempty"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	LatencyMs int64   `json:"latency_ms"`
}

// Chat handles POST /v1/chat: it decodes the prompt, calls the generator with
// the request context so a client disconnect cancels the upstream Bedrock call,
// meters the cost, logs the usage, and writes the completion as JSON.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	messages, ok := decodeChat(r, &req)
	if !ok {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	key := middleware.KeyFromContext(r.Context())

	start := time.Now()
	comp, err := h.gen.Generate(r.Context(), messages) // cancellation propagates
	if err != nil {
		// Log the upstream error but keep the client's message generic: the caller
		// cannot act on a Bedrock fault, and the detail (model IDs, IAM failures)
		// is operator information. Without this line a 502 is unattributable.
		middleware.LoggerFromContext(r.Context()).Error("generation failed",
			"err", err,
			"model", h.model,
		)
		http.Error(w, "generation failed", http.StatusBadGateway)
		return
	}
	latency := time.Since(start)

	cost := meter.Cost(h.model, comp.TokensIn, comp.TokensOut)

	middleware.LoggerFromContext(r.Context()).Info("generation",
		"key", key,
		"model", h.model,
		"tokens_in", comp.TokensIn,
		"tokens_out", comp.TokensOut,
		"cost_usd", cost,
		"latency_ms", latency.Milliseconds(),
	)

	writeJSON(w, chatResponse{
		Text:      comp.Text,
		TokensIn:  comp.TokensIn,
		TokensOut: comp.TokensOut,
		CostUSD:   cost,
		LatencyMs: latency.Milliseconds(),
	})
}

// decodeChat decodes and validates a chat request body, returning the mapped
// messages and false if the body is malformed. Shared by the streaming and
// non-streaming handlers so both reject the same bad input identically.
func decodeChat(r *http.Request, req *chatRequest) ([]bedrock.Message, bool) {
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		return nil, false
	}
	return req.toMessages()
}

// writeJSON sets the JSON content type and encodes v directly to the response
// body. The encode error is intentionally dropped: the status line is already
// sent by the time encoding could fail, so there is nothing useful to do with it.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
