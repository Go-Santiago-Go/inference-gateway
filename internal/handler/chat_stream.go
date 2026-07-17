package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Go-Santiago-Go/inference-gateway/internal/meter"
	"github.com/Go-Santiago-Go/inference-gateway/internal/middleware"
)

// ChatStream handles POST /v1/chat as a Server-Sent Events stream: it relays
// each token as a data frame the instant Bedrock produces it, then emits one
// final `event: usage` frame with the request's token counts, cost, and
// latency. It passes the request context to the generator so a client
// disconnect cancels the upstream Bedrock call instead of paying for unread
// tokens.
func (h *Handler) ChatStream(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	messages, ok := decodeChat(r, &req)
	if !ok {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Assert Flusher before sending any header: once a 200 text/event-stream
	// status is on the wire we can no longer report that streaming is impossible.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// The SSE wire contract: event-stream content type, plus no-cache so an
	// intermediary proxy relays frames live instead of buffering the response.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	key := middleware.KeyFromContext(r.Context())

	start := time.Now()
	stream, err := h.gen.GenerateStream(r.Context(), messages)
	if err != nil {
		// Must return: on error stream is nil, and ranging a nil channel blocks
		// forever. This can still set a status because no frame has been sent.
		http.Error(w, "generation failed", http.StatusBadGateway)
		return
	}

	// Relay each text chunk as its own SSE frame, flushing so it leaves
	// immediately; the terminal chunk carries token counts and no text.
	var tokensIn, tokensOut int
	for chunk := range stream {
		if chunk.Text != "" {
			// A blank line terminates an SSE frame, so text containing newlines
			// cannot be written as a single data line without truncating the
			// frame at the model's first paragraph break. The spec's encoding is
			// one data line per line of payload, which the client rejoins with
			// "\n".
			for line := range strings.SplitSeq(chunk.Text, "\n") {
				fmt.Fprintf(w, "data: %s\n", line)
			}
			fmt.Fprint(w, "\n")
			flusher.Flush()
		}
		if chunk.TokensIn != 0 || chunk.TokensOut != 0 {
			tokensIn, tokensOut = chunk.TokensIn, chunk.TokensOut
		}
	}

	latency := time.Since(start)
	cost := meter.Cost(h.model, tokensIn, tokensOut)

	// One structured line per request, the same fields as the non-streaming
	// path; stream:true distinguishes the two endpoints in the logs.
	slog.Info("generation",
		"key", key,
		"model", h.model,
		"tokens_in", tokensIn,
		"tokens_out", tokensOut,
		"cost_usd", cost,
		"latency_ms", latency.Milliseconds(),
		"stream", true,
	)

	// Same metered fields as the non-streaming path, delivered as a named frame
	// so the client can tell the usage summary from a token frame.
	usage := chatResponse{TokensIn: tokensIn, TokensOut: tokensOut, CostUSD: cost, LatencyMs: latency.Milliseconds()}
	payload, _ := json.Marshal(usage)
	fmt.Fprintf(w, "event: usage\ndata: %s\n\n", payload)
	flusher.Flush()
}
