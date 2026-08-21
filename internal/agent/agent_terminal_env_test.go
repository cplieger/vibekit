package agent

import (
	"slices"
	"strings"
	"testing"
)

// TestScreenAgentEnv_RefusesExecutionRedirection is the security property: a
// variable that changes what a program EXECUTES is refused, whatever the command
// was approved as.
//
// One case per mechanism rather than one per name, because the mechanisms are what
// a reader has to understand to maintain the list — a name added to the wrong
// group is the mistake that matters, and enumerating all 38 would assert the map
// against itself.
func TestScreenAgentEnv_RefusesExecutionRedirection(t *testing.T) {
	cases := []struct {
		name      string
		vars      []termEnvVar
		wantFirst string
	}{
		{
			"loader injection into any dynamically linked process",
			[]termEnvVar{{Name: "LD_PRELOAD", Value: "/tmp/evil.so"}},
			"LD_PRELOAD",
		},
		{
			"interpreter startup hook",
			[]termEnvVar{{Name: "NODE_OPTIONS", Value: "--require /tmp/evil.js"}},
			"NODE_OPTIONS",
		},
		{
			"git helper that takes a command",
			[]termEnvVar{{Name: "GIT_SSH_COMMAND", Value: "sh -c curl|sh"}},
			"GIT_SSH_COMMAND",
		},
		{
			"helper program spawn",
			[]termEnvVar{{Name: "PAGER", Value: "sh -c whoami"}},
			"PAGER",
		},
		{
			"command resolution",
			[]termEnvVar{{Name: "PATH", Value: "/tmp/bin"}},
			"PATH",
		},
		{
			"shell pre-command hook",
			[]termEnvVar{{Name: "BASH_ENV", Value: "/tmp/evil.sh"}},
			"BASH_ENV",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := screenAgentEnv(c.vars, nil)
			if len(got) != 1 || got[0] != c.wantFirst {
				t.Errorf("screenAgentEnv = %v, want [%s] refused", got, c.wantFirst)
			}
		})
	}
}

// TestScreenAgentEnv_AllowsOrdinaryAndInertValues is the usability half, and it is
// not a formality: a guard that refuses the common case gets switched off.
//
// The inert-value carve-out is the load-bearing part. `GIT_PAGER=cat` and `PAGER=`
// are how anything non-interactive stops git paging, so an agent running git hits
// those constantly — a name-only denylist would refuse them and teach the operator
// to set VIBEKIT_ALLOW_AGENT_ENV to everything.
func TestScreenAgentEnv_AllowsOrdinaryAndInertValues(t *testing.T) {
	ok := []termEnvVar{
		// Ordinary build/config variables: not on the list at all.
		{Name: "CGO_ENABLED", Value: "0"},
		{Name: "GOFLAGS", Value: "-mod=readonly"},
		{Name: "TERM", Value: "dumb"},
		{Name: "LANG", Value: "C.UTF-8"},
		// Dangerous NAMES neutralised by their value.
		{Name: "GIT_PAGER", Value: "cat"},
		{Name: "PAGER", Value: ""},
		{Name: "GIT_ASKPASS", Value: "true"},
		// Case variants are inert on a case-sensitive platform: the loader reads
		// LD_PRELOAD and nothing else, so refusing this would only annoy.
		{Name: "ld_preload", Value: "/tmp/evil.so"},
	}
	if got := screenAgentEnv(ok, nil); len(got) != 0 {
		t.Errorf("screenAgentEnv refused %v, want none refused", got)
	}
}

// TestScreenAgentEnv_ReportsEveryOffenderInOrder pins that the refusal names ALL
// of them, in the order asked. Reporting only the first would send the agent
// through one round trip per variable, and the error text is the only thing that
// tells it what to drop.
func TestScreenAgentEnv_ReportsEveryOffenderInOrder(t *testing.T) {
	got := screenAgentEnv([]termEnvVar{
		{Name: "LD_PRELOAD", Value: "/tmp/a.so"},
		{Name: "CGO_ENABLED", Value: "0"},
		{Name: "PYTHONSTARTUP", Value: "/tmp/b.py"},
	}, nil)
	want := []string{"LD_PRELOAD", "PYTHONSTARTUP"}
	if !slices.Equal(got, want) {
		t.Errorf("screenAgentEnv = %v, want %v", got, want)
	}
}

// TestScreenAgentEnv_OperatorAllowlist covers the escape hatch. A dev box has
// legitimate uses (a preload-based profiler, a vendored NODE_PATH), and a guard
// with no way past it gets worked around rather than tuned.
//
// Deterministic because the allowlist is a parameter: an earlier version drove it
// through the environment, and sync.OnceValue resolves at most once per process, so
// it passed or skipped depending on which sibling test ran first — gating nothing
// either way.
func TestScreenAgentEnv_OperatorAllowlist(t *testing.T) {
	allowed := parseAllowedEnv(" LD_PRELOAD , NODE_PATH ")

	if got := screenAgentEnv([]termEnvVar{{Name: "LD_PRELOAD", Value: "/opt/profiler.so"}}, allowed); len(got) != 0 {
		t.Errorf("an allowed name was still refused: %v", got)
	}
	// Per NAME, never a blanket off switch.
	if got := screenAgentEnv([]termEnvVar{{Name: "BASH_ENV", Value: "/tmp/evil.sh"}}, allowed); len(got) != 1 {
		t.Errorf("allowing LD_PRELOAD also allowed BASH_ENV: %v", got)
	}
}

// TestParseAllowedEnv covers the operator-facing parse, including the shapes a
// hand-edited compose file actually produces.
func TestParseAllowedEnv(t *testing.T) {
	if got := parseAllowedEnv(""); got != nil {
		t.Errorf("empty = %v, want nil (no allowlist at all)", got)
	}
	if got := parseAllowedEnv("   "); got != nil {
		t.Errorf("blank = %v, want nil: whitespace is not an allowlist of one", got)
	}
	got := parseAllowedEnv("LD_PRELOAD, NODE_PATH ,, ")
	if len(got) != 2 {
		t.Fatalf("parseAllowedEnv = %v, want 2 names (empty entries dropped)", got)
	}
	for _, want := range []string{"LD_PRELOAD", "NODE_PATH"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q missing from %v: surrounding spaces must be trimmed", want, got)
		}
	}
}

// TestDangerousAgentEnv_MatchesUpstream pins the list against kiro-cli's own
// `dangerous_env_vars`, which is where it came from.
//
// The point is DIVERGENCE, not the contents: the same agent should behave the same
// through the TUI and through vibekit, so a name upstream adds (2.18.1 added two
// tar FLAGS to the sibling list, so this one moves too) should show up here as a
// deliberate edit rather than drift nobody noticed. Hardcoded rather than read off
// the binary because the binary is not present in CI.
func TestDangerousAgentEnv_MatchesUpstream(t *testing.T) {
	upstream := strings.Fields(`
		PAGER EDITOR VISUAL BROWSER MANPAGER GIT_PAGER LESS LESSOPEN LESSCLOSE
		LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH
		PYTHONWARNINGS PYTHONSTARTUP PYTHONPATH PYTHONHOME
		PERL5OPT PERL5LIB RUBYOPT RUBYLIB NODE_OPTIONS NODE_PATH
		IFS PATH HOME SHELL PROMPT_COMMAND BASH_ENV ENV
		GIT_EDITOR GIT_SEQUENCE_EDITOR GIT_ASKPASS GIT_EXTERNAL_DIFF
		GIT_SSH GIT_SSH_COMMAND GIT_PROXY_COMMAND GIT_EXEC_PATH GIT_TEMPLATE_DIR`)

	for _, name := range upstream {
		if _, ok := dangerousAgentEnv[name]; !ok {
			t.Errorf("upstream screens %q and this list does not", name)
		}
	}
	if len(dangerousAgentEnv) != len(upstream) {
		t.Errorf("list has %d names, upstream has %d: reconcile the difference deliberately",
			len(dangerousAgentEnv), len(upstream))
	}
	// The inert values are upstream's too, and the guard's usability rests on them.
	for _, v := range []string{"", "true", "cat"} {
		if _, ok := safeAgentEnvValues[v]; !ok {
			t.Errorf("upstream treats %q as an inert value and this does not", v)
		}
	}
}
