package bridge

import (
	"slices"
	"testing"
)

func TestBuildACPArgs(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		want   []string
	}{
		{name: "bare defaults to v3", want: []string{"acp", "--agent-engine", "v3"}},
		{name: "explicit v3 engine", engine: "v3", want: []string{"acp", "--agent-engine", "v3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildACPArgs(tt.engine)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildACPArgs(%q) = %v, want %v", tt.engine, got, tt.want)
			}
		})
	}
}

// kiro-cli REFUSES --model and --effort alongside --agent-engine=v3 and exits
// before answering initialize:
//
//	error: the following arguments are not supported with --agent-engine=v3: --model, --effort
//
// So emitting either does not merely fail to take effect, it kills the bridge —
// which is what made every model switch fall back to a restart that also died.
// The initial values go over session/set_config_option instead
// (applyInitialModel / applyInitialEffort). Measured against kiro-cli 2.17.0
// and 2.18.0; `-v` is the only other flag v3 accepts.
func TestBuildACPArgs_OmitsFlagsV3Refuses(t *testing.T) {
	refused := []string{"--model", "--effort", "--agent", "--trust-all-tools", "--trust-tools"}
	for _, engine := range []string{"", "v3"} {
		args := buildACPArgs(engine)
		for _, flag := range refused {
			if slices.Contains(args, flag) {
				t.Errorf("buildACPArgs(%q) = %v, must not carry %s", engine, args, flag)
			}
		}
	}
}
