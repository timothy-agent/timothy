package ledger

import (
	"math"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func TestCost(t *testing.T) {
	t.Parallel()
	prices := &router.ModelPrices{
		InputPerMTok: 3, OutputPerMTok: 15,
		CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75,
	}

	tests := []struct {
		name   string
		prices *router.ModelPrices
		usage  *stream.Usage
		want   *float64
	}{
		{
			name: "nil prices means unknown", usage: &stream.Usage{InputTokens: 100},
			want: nil,
		},
		{
			name: "nil usage means unknown", prices: prices,
			want: nil,
		},
		{
			name: "plain tokens", prices: prices,
			usage: &stream.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			want:  f(18),
		},
		{
			name: "cache tokens priced", prices: prices,
			usage: &stream.Usage{InputTokens: 100_000, OutputTokens: 50_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 200_000},
			want:  f(0.3 + 0.75 + 0.3 + 0.75),
		},
		{
			name: "zero usage costs zero", prices: prices,
			usage: &stream.Usage{},
			want:  f(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Cost(tt.prices, tt.usage)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("Cost() = %v, want nil", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("Cost() = nil, want %v", *tt.want)
			case tt.want != nil && math.Abs(*got-*tt.want) > 1e-9:
				t.Fatalf("Cost() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func f(v float64) *float64 { return &v }
