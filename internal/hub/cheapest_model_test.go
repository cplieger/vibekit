package hub

import (
	"context"
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestCheapestModel(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		models []api.SessionModel
	}{
		{
			name:   "NilCatalog",
			models: nil,
			want:   "",
		},
		{
			name:   "EmptyCatalog",
			models: []api.SessionModel{},
			want:   "",
		},
		{
			name: "SkipsAuto",
			models: []api.SessionModel{
				{ID: "auto"},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "SelectsCheaperOfTwo",
			models: []api.SessionModel{
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
				{ID: "claude-opus-4.7", RateMultiplier: 2.2},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "SkipsEmptyIDs",
			models: []api.SessionModel{
				{ID: ""},
				{ID: "auto"},
				{ID: "real", RateMultiplier: 1.0},
			},
			want: "real",
		},
		{
			name: "SkipsMultipleConsecutiveIneligible",
			models: []api.SessionModel{
				{ID: "auto"},
				{ID: ""},
				{ID: "auto"},
				{ID: ""},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
				{ID: "claude-opus-4.7", RateMultiplier: 2.2},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "AllAutoReturnsEmpty",
			models: []api.SessionModel{
				{ID: "auto"},
				{ID: "auto"},
			},
			want: "",
		},
		{
			name: "SkipsDeprecated",
			models: []api.SessionModel{
				{ID: "old-model", Description: "[Deprecated] Old model", RateMultiplier: 0.1},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "SkipsLegacy",
			models: []api.SessionModel{
				{ID: "legacy-model", Name: "[Legacy] Old", RateMultiplier: 0.1},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "SkipsInternal",
			models: []api.SessionModel{
				{ID: "agi-nova-beta-1m", Description: "[Internal] AGI Nova SWE Beta", RateMultiplier: 0.01},
				{ID: "qwen3-coder-480b", Description: "[Internal] Qwen3 Coder", RateMultiplier: 0.01},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "SkipsExperimental",
			models: []api.SessionModel{
				{ID: "deepseek-3.2", Description: "[Experimental]", RateMultiplier: 0.25},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
			},
			want: "claude-haiku-4.5",
		},
		{
			name: "SelectsCheapestByRate",
			models: []api.SessionModel{
				{ID: "claude-opus-4.6", RateMultiplier: 2.2},
				{ID: "claude-sonnet-4.6", RateMultiplier: 1.3},
				{ID: "claude-haiku-4.5", RateMultiplier: 0.4},
				{ID: "minimax-m2.5", RateMultiplier: 0.25},
			},
			want: "minimax-m2.5",
		},
		{
			name: "FallsBackWhenNoRates",
			models: []api.SessionModel{
				{ID: "auto"},
				{ID: "claude-opus-4.6"},
				{ID: "claude-haiku-4.5"},
			},
			want: "claude-opus-4.6",
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cheapestModel(ctx, tc.models); got != tc.want {
				t.Errorf("cheapestModel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestModelExcluded(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"", false},
		{"Claude Haiku 4.5", false},
		{"[Deprecated] Old model", true},
		{"[Legacy] Sunset model", true},
		{"[Internal] AGI Nova", true},
		{"[Experimental] Beta model", true},
		{"This model is deprecated", false},
		{"experimental preview", false},
		{"Claude Sonnet 4.6 with 1M context", false},
	}
	for _, tc := range cases {
		if got := modelExcluded(tc.text); got != tc.want {
			t.Errorf("modelExcluded(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func BenchmarkCheapestModel(b *testing.B) {
	makeModels := func(n int) []api.SessionModel {
		ms := make([]api.SessionModel, n)
		for i := range ms {
			ms[i] = api.SessionModel{
				ID:             fmt.Sprintf("model-%c%d", 'a'+rune(i%26), i/26),
				RateMultiplier: float64(n-i) * 0.1,
			}
		}
		ms[n-1] = api.SessionModel{ID: "cheapest", RateMultiplier: 0.01}
		return ms
	}

	ctx := context.Background()
	for _, size := range []int{5, 20, 100} {
		catalog := makeModels(size)
		b.Run(fmt.Sprintf("catalog_%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				cheapestModel(ctx, catalog)
			}
		})
	}
}
