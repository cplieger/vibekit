package bridge

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestBuildACPArgs(t *testing.T) {
	tests := []struct {
		name   string
		agent  string
		model  string
		effort string
		extra  []string
		want   []string
	}{
		{name: "bare", want: []string{"acp"}},
		{
			name: "agent and model", agent: "kiro_default", model: "claude-opus-4.8",
			want: []string{"acp", "--agent", "kiro_default", "--model", "claude-opus-4.8"},
		},
		{name: "model auto is omitted", model: api.ModelAuto, want: []string{"acp"}},
		{
			name: "valid effort appended", model: "m", effort: "high",
			want: []string{"acp", "--model", "m", "--effort", "high"},
		},
		{
			name: "invalid effort dropped", model: "m", effort: "ultra",
			want: []string{"acp", "--model", "m"},
		},
		{
			name: "empty effort dropped", model: "m", effort: "",
			want: []string{"acp", "--model", "m"},
		},
		{
			name: "extra args precede flags", extra: []string{"--trust-all-tools"}, effort: "max",
			want: []string{"acp", "--trust-all-tools", "--effort", "max"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildACPArgs(tt.agent, tt.model, tt.effort, tt.extra)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildACPArgs(%q,%q,%q,%v) = %v, want %v",
					tt.agent, tt.model, tt.effort, tt.extra, got, tt.want)
			}
		})
	}
}
