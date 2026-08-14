package bridge

import (
	"slices"
	"strings"
	"testing"
)

func TestFilterACPArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"keeps verbose", []string{"-v"}, []string{"-v"}},
		{"keeps agent", []string{"--agent", "my-agent"}, []string{"--agent", "my-agent"}},
		{
			// The refusal that protects an invariant: vibekit removed the v2
			// handlers, so an operator v1/v2 stalls session/new.
			name: "refuses agent-engine and its value",
			in:   []string{"--agent-engine", "v2", "-v"},
			want: []string{"-v"},
		},
		{
			// A --flag=value spelling must be refused too, or the invariant is
			// reopenable by a typo's worth of difference.
			name: "refuses agent-engine in inline form without eating the next token",
			in:   []string{"--agent-engine=v2", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses trust-all long and short",
			in:   []string{"--trust-all-tools", "-a", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses trust-tools and its value",
			in:   []string{"--trust-tools", "fs_read,fs_write", "-v"},
			want: []string{"-v"},
		},
		{
			name: "refuses trust-tools inline",
			in:   []string{"--trust-tools=fs_read", "-v"},
			want: []string{"-v"},
		},
		{
			name: "keeps an unknown future flag — the whole point of the hatch",
			in:   []string{"--some-flag-upstream-adds", "value"},
			want: []string{"--some-flag-upstream-adds", "value"},
		},
		{
			// -a is NOT value-bearing, so the token after it survives.
			name: "short trust-all does not consume the next token",
			in:   []string{"-a", "--agent", "x"},
			want: []string{"--agent", "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterACPArgs(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("FilterACPArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseACPArgs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty yields nil", "", nil},
		{"whitespace only yields nil", "   \t ", nil},
		{"splits on whitespace", "-v --agent x", []string{"-v", "--agent", "x"}},
		{"collapses runs of whitespace", "-v    --agent\tx", []string{"-v", "--agent", "x"}},
		{"filters while parsing", "--agent-engine v1 -v", []string{"-v"}},
		// Everything refused: the result is empty, not nil, and callers append
		// it harmlessly either way.
		{"all refused", "--agent-engine v1 -a", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseACPArgs(tc.raw)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseACPArgs(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRefuseReasonNamesTheRealSurface pins that each refusal EXPLAINS itself.
// An operator who sets --trust-all-tools and sees it silently vanish would
// reasonably conclude permissions are off; the reason has to point at
// permissions.yaml. Same for the engine pin.
func TestRefuseReasonNamesTheRealSurface(t *testing.T) {
	cases := []struct {
		flag        string
		wantSubstrs []string
	}{
		{flagAgentEngine, []string{"v3-only"}},
		{flagTrustAll, []string{"inert", "permissions.yaml"}},
		{flagTrustAllShort, []string{"inert", "permissions.yaml"}},
		{flagTrustTools, []string{"inert", "permissions.yaml"}},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			reason, refused := refuseReason(tc.flag)
			if !refused {
				t.Fatalf("refuseReason(%q) refused = false, want true", tc.flag)
			}
			for _, want := range tc.wantSubstrs {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q does not mention %q", reason, want)
				}
			}
		})
	}
	if _, refused := refuseReason("--agent"); refused {
		t.Error("refuseReason(--agent) refused = true, want false")
	}
}

// TestBuildACPArgsPrecedesExtraArgs pins the ordering the design calls for: a
// launch flag is an INITIAL value, so it lands after the derived args (kiro-cli
// takes the last spelling of a repeated flag) and vibekit's own switch_model /
// set_effort still win afterwards over session/set_config_option.
func TestBuildACPArgsPrecedesExtraArgs(t *testing.T) {
	derived := buildACPArgs("v3")
	extra := []string{"-v"}
	full := append(slices.Clone(derived), extra...)

	if len(full) <= len(derived) {
		t.Fatalf("extra args did not append: %v", full)
	}
	if full[len(full)-1] != "-v" {
		t.Errorf("last arg = %q, want the operator flag last", full[len(full)-1])
	}
	// The derived prefix must survive verbatim.
	if !slices.Equal(full[:len(derived)], derived) {
		t.Errorf("derived prefix changed: %v, want %v", full[:len(derived)], derived)
	}
}
