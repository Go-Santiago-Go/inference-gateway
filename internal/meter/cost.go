// Package meter turns Bedrock token counts into a per-request dollar cost using
// a static per-model price table. It performs no I/O, so cost accounting is a
// pure function the handler and the streaming path can both reuse.
package meter

// Price is the cost of a model's tokens in US dollars per 1,000 tokens, split
// because Bedrock prices input and output tokens at different rates.
type Price struct {
	In  float64
	Out float64
}

// prices maps a Bedrock model ID to its per-1K-token price. Values are dollars
// per 1,000 tokens.
var prices = map[string]Price{
	"us.anthropic.claude-haiku-4-5-20251001-v1:0": {
		In:  0.001,
		Out: 0.005,
	},
}

// Cost returns the dollar cost of a request given its model and token counts.
// An unknown model yields 0: metering is best-effort observability and must
// never fail the request path over a missing price-table entry.
func Cost(model string, in, out int) float64 {
	p := prices[model] // zero value {0, 0} for an unknown model
	return float64(in)/1000*p.In + float64(out)/1000*p.Out
}
