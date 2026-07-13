package handler

import (
	"context"
	"encoding/json"
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
