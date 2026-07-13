package meter

import (
	"math"
	"testing"
)

func TestCost(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		in, out int
		want    float64
	}{
		{
			name:  "known model",
			model: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
			in:    1500,
			out:   800,
			want:  0.0055,
		},
		{
			name:  "unknown model is free",
			model: "not-a-real-model",
			in:    1000,
			out:   1000,
			want:  0,
		},
		{
			name:  "zero tokens",
			model: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
			in:    0,
			out:   0,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Cost(tt.model, tt.in, tt.out)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Cost(%q, %d, %d) = %v, want %v",
					tt.model, tt.in, tt.out, got, tt.want)
			}
		})
	}
}
