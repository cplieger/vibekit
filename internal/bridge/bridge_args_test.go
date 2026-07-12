package bridge

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestBuildACPArgs(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		model  string
		effort string
		want   []string
	}{
		{name: "bare defaults to v3", want: []string{"acp", "--agent-engine", "v3"}},
		{name: "explicit v3 engine", engine: "v3", want: []string{"acp", "--agent-engine", "v3"}},
		{
			name: "model", model: "claude-opus-4.8",
			want: []string{"acp", "--agent-engine", "v3", "--model", "claude-opus-4.8"},
		},
		{name: "model auto is omitted", model: api.ModelAuto, want: []string{"acp", "--agent-engine", "v3"}},
		{
			name: "valid effort appended", model: "m", effort: "high",
			want: []string{"acp", "--agent-engine", "v3", "--model", "m", "--effort", "high"},
		},
		{
			name: "invalid effort dropped", model: "m", effort: "ultra",
			want: []string{"acp", "--agent-engine", "v3", "--model", "m"},
		},
		{
			name: "empty effort dropped", model: "m", effort: "",
			want: []string{"acp", "--agent-engine", "v3", "--model", "m"},
		},
		{
			name: "effort without model", effort: "max",
			want: []string{"acp", "--agent-engine", "v3", "--effort", "max"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildACPArgs(tt.engine, tt.model, tt.effort)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildACPArgs(%q,%q,%q) = %v, want %v",
					tt.engine, tt.model, tt.effort, got, tt.want)
			}
		})
	}
}
