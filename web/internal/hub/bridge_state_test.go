package hub

import (
	"fmt"
	"testing"
)

func TestBridgeState_Transitions(t *testing.T) {
	cases := []struct {
		state bridgeState
		want  string
	}{
		{bridgeIdle, "idle"},
		{bridgeStarting, "starting"},
		{bridgePrompting, "prompting"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.state.String(); got != tc.want {
				t.Errorf("bridgeState(%d).String() = %q, want %q", int(tc.state), got, tc.want)
			}
		})
	}

	// Out-of-range state returns "bridgeState(N)" format.
	outOfRange := bridgeState(99)
	want := fmt.Sprintf("bridgeState(%d)", 99)
	if got := outOfRange.String(); got != want {
		t.Errorf("out-of-range String() = %q, want %q", got, want)
	}
}
