package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/web-terminal-engine/v4/terminal"
)

// TestRun_delegatesToTheEngineRule pins that the gate's verdict IS the
// engine's verdict, in both directions and at the exclusive boundary. The rule
// itself (and its agreement with the runtime close-4002 floor) is pinned in
// the engine; what matters here is that run() routes that answer without
// inverting or swallowing it.
func TestRun_delegatesToTheEngineRule(t *testing.T) {
	cases := map[string]struct {
		clientRev, clientMinServer int
		wantCompatible             bool
	}{
		"current pairing":                    {terminal.WireProtocolVersion, terminal.MinSupportedClientWireVersion, true},
		"client exactly at the server floor": {terminal.MinSupportedClientWireVersion, terminal.MinSupportedClientWireVersion, true},
		"client below the server floor":      {terminal.MinSupportedClientWireVersion - 1, terminal.MinSupportedClientWireVersion, false},
		"client demands a newer server":      {terminal.WireProtocolVersion, terminal.WireProtocolVersion + 1, false},
		"client demands exactly this server": {terminal.WireProtocolVersion, terminal.WireProtocolVersion, true},
		"future client revision is accepted": {terminal.WireProtocolVersion + 3, terminal.MinSupportedClientWireVersion, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.clientRev, tc.clientMinServer, &stdout, &stderr)
			engineSaysCompatible := terminal.WirePairIncompatibility(
				terminal.WireProtocolVersion, terminal.MinSupportedClientWireVersion,
				tc.clientRev, tc.clientMinServer,
			) == ""
			if engineSaysCompatible != tc.wantCompatible {
				t.Fatalf("engine verdict for (%d,%d) = compatible:%v, test expected %v; the engine's rule changed and this table is stale",
					tc.clientRev, tc.clientMinServer, engineSaysCompatible, tc.wantCompatible)
			}
			if gateCompatible := code == 0; gateCompatible != engineSaysCompatible {
				t.Errorf("run(%d,%d) exit %d (compatible:%v) but the engine says compatible:%v; the gate does not follow the engine's rule",
					tc.clientRev, tc.clientMinServer, code, gateCompatible, engineSaysCompatible)
			}
		})
	}
}

// TestRun_exitCodeContract pins the process exit codes and output streams the
// Dockerfile wire-floor gate branches on: 0 compatible (ok line on stdout),
// 1 floor violated (mismatch on stderr), 2 usage error. A wiring regression
// (inverted check, swapped code, wrong stream) would silently neuter or break
// the image build gate. Client values derive from the engine's constants, so
// the cases track a future floor raise.
func TestRun_exitCodeContract(t *testing.T) {
	cases := []struct {
		name                       string
		clientRev, clientMinServer int
		wantCode                   int
		wantStdout, wantStderr     string
	}{
		{
			name:      "compatible pairing exits 0 and reports ok on stdout",
			clientRev: terminal.WireProtocolVersion, clientMinServer: terminal.WireProtocolVersion,
			wantCode: 0, wantStdout: "wirecheck ok:",
		},
		{
			name:      "violated floor exits 1 with the mismatch on stderr",
			clientRev: terminal.WireProtocolVersion, clientMinServer: terminal.WireProtocolVersion + 1,
			wantCode: 1, wantStderr: "ERROR wire-floor-mismatch:",
		},
		{
			name:      "zero client-rev is a usage error (exit 2)",
			clientRev: 0, clientMinServer: terminal.WireProtocolVersion,
			wantCode: 2, wantStderr: "required positive integers",
		},
		{
			name:      "negative client-min-server is a usage error (exit 2)",
			clientRev: terminal.WireProtocolVersion, clientMinServer: -1,
			wantCode: 2, wantStderr: "required positive integers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.clientRev, tc.clientMinServer, &stdout, &stderr); got != tc.wantCode {
				t.Errorf("run(%d, %d) = %d, want exit code %d (the Dockerfile gate branches on it)",
					tc.clientRev, tc.clientMinServer, got, tc.wantCode)
			}
			if tc.wantStdout == "" {
				if stdout.Len() != 0 {
					t.Errorf("stdout = %q, want empty on a non-zero exit", stdout.String())
				}
			} else if !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr == "" {
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want empty on success", stderr.String())
				}
			} else if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

// TestRun_failureNamesBothPins keeps the remediation actionable: the engine's
// reason says WHICH half is behind, and this repo adds which pin to move.
func TestRun_failureNamesBothPins(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(terminal.WireProtocolVersion, terminal.WireProtocolVersion+1, &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want exit 1 for a violated floor", code)
	}
	out := stderr.String()
	for _, want := range []string{"go.mod", "CPLIEGER_WEB_TERMINAL_ENGINE_VERSION", "static-src/package.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr = %q, want it to name the %q pin", out, want)
		}
	}
}
