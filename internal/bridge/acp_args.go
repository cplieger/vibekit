// Operator-supplied kiro-cli launch flags: the VIBEKIT_KIRO_ACP_ARGS filter.
//
// The point of the injector is NOT to unlock a capability. `resolveAgentEngine`
// is a one-line hard pin to v3, so the engine was never reachable here. Of the
// seven flags `kiro-cli acp` accepts, one is already emitted (`--agent-engine`)
// and five must be refused, which leaves `--agent` and `-v`.
//
// An earlier revision of this comment claimed `buildACPArgs` emits `--model` and
// `--effort`. It does not, and cannot: kiro-cli REFUSES both alongside
// `--agent-engine=v3` and exits before answering initialize (see
// bridge_process.go). That mistake is why they were missing from the refusal list
// below, which made this hatch a way to kill every chat bridge from a compose
// value. Model and effort travel as session config options instead.
//
// So the justification is reach, not power: a flag upstream adds tomorrow becomes
// a compose-value edit instead of a code change and an image rebuild. Worth ~40
// lines on those terms and no more; do not sell it as a v3 switch or a
// permissions lever.
//
// The flag set was re-read off the real binary (`kiro-cli acp --help`, 2.16.0):
// --agent, --model, --effort, -a/--trust-all-tools, --trust-tools,
// --agent-engine, -v/--verbose. Exactly seven, matching the design's accounting.
//
// `-v` is protocol-SAFE, which is the one thing worth checking before handing an
// operator a logging flag on a JSON-RPC pipe: verified against a live spawn,
// stdout stayed pure JSON-RPC and the verbose output went to stderr. A future
// upstream flag that writes to stdout would corrupt the stream, so this filter is
// an allow-everything-unknown list on purpose — if that stops being safe, the
// answer is a specific refusal here, not a general one.

package bridge

import (
	"log/slog"
	"strings"
)

// Refused flags. Each is dropped rather than passed through, and each has a
// different reason — the warnings say which, because an operator who sets one
// and sees nothing happen would otherwise draw the wrong conclusion.
const (
	// flagAgentEngine would reopen a closed decision. vibekit is v3-only ON THE
	// WIRE: the v2 `_kiro.dev/*` handlers were removed, so an operator v1/v2
	// stalls session/new and fails every turn.
	flagAgentEngine = "--agent-engine"
	// flagTrustAll / flagTrustTools are refused because they are INERT, not
	// because they are dangerous. They were live-confirmed no-ops on the v3
	// wire — Cedar owns tool authorization and these never reach it. The hazard
	// is an operator setting one, seeing no error, and believing permissions are
	// off.
	flagTrustAll      = "--trust-all-tools"
	flagTrustAllShort = "-a"
	flagTrustTools    = "--trust-tools"
	// flagModel / flagEffort are refused because they KILL THE PROCESS. kiro-cli
	// rejects both alongside --agent-engine=v3 and exits before answering
	// initialize, so one of these in the compose value takes down every chat
	// bridge in the container with an error the operator never sees on this
	// wire. This is the only refusal here whose reason is fatality rather than
	// a closed decision or an inert no-op, and it is the reason the filter is
	// allow-unknown rather than allow-nothing: the model and the reasoning
	// effort are session config options, set per chat and switchable live.
	flagModel  = "--model"
	flagEffort = "--effort"
)

// valueBearing names the refused flags that consume a following token, so the
// filter drops the value with the flag instead of leaving it behind as a
// stray positional that kiro-cli would reject.
var valueBearing = map[string]bool{
	flagAgentEngine: true,
	flagTrustTools:  true,
	flagModel:       true,
	flagEffort:      true,
}

// ParseACPArgs splits an operator-supplied flag string on whitespace and filters
// it. Empty input yields nil.
//
// Logged by COUNT only, never by value: a compose-expansion mistake or a
// value-bearing flag could otherwise put a secret in the log. That is the
// sibling's rule (KIRO_CLI_CHAT_ARGS) and it carries over unchanged.
func ParseACPArgs(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	kept := FilterACPArgs(fields)
	slog.Info("appending extra kiro-cli acp flags",
		"acp_args_count", len(kept), "refused_count", len(fields)-len(kept))
	return kept
}

// FilterACPArgs drops the five flags vibekit's own invariants own, together
// with the value of any that takes one. Exported so a test can assert the
// refusals directly rather than through a log.
//
// A `--flag=value` spelling is matched on the part before the `=`, so
// `--agent-engine=v2` is refused too — kiro-cli accepts both spellings, and
// filtering only the space-separated one would leave the invariant reopenable by
// a typo's worth of difference.
func FilterACPArgs(fields []string) []string {
	kept := make([]string, 0, len(fields))
	skipValue := false
	for _, f := range fields {
		if skipValue {
			skipValue = false
			continue
		}
		name, _, hasInlineValue := strings.Cut(f, "=")
		if reason, refused := refuseReason(name); refused {
			slog.Warn("refusing kiro-cli acp flag", "flag", name, "reason", reason)
			// Only consume the NEXT token when the value is not already
			// attached with `=`.
			skipValue = valueBearing[name] && !hasInlineValue
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

// refuseReason reports whether a flag is refused and why. The reason strings are
// the operator-facing explanation, so each names the real surface rather than
// only saying no.
func refuseReason(name string) (reason string, refused bool) {
	switch name {
	case flagAgentEngine:
		return "vibekit is v3-only on the wire; the v2 handlers were removed, so v1/v2 would stall session/new", true
	case flagTrustAll, flagTrustAllShort, flagTrustTools:
		return "inert on v3 — tool authorization is kiro-cli's Cedar policy; edit permissions.yaml (Settings → Permissions) instead", true
	case flagModel, flagEffort:
		return "kiro-cli refuses this alongside --agent-engine=v3 and exits before initialize, so it would kill every chat bridge; pick the model and reasoning effort per chat in the composer instead", true
	default:
		return "", false
	}
}
