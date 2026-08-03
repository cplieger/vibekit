// Operator-supplied kiro-cli launch flags: the VIBEKIT_KIRO_ACP_ARGS filter.
//
// The point of the injector is NOT to unlock a capability. Two things vibekit
// was assumed to be missing, it already had: `resolveAgentEngine` is a one-line
// hard pin to v3, and `buildACPArgs` already emits `--model` and `--effort`. Of
// the seven flags `kiro-cli acp` accepts, three are already emitted and three
// must be refused, which leaves `--agent` and `-v`.
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
)

// valueBearing names the refused flags that consume a following token, so the
// filter drops the value with the flag instead of leaving it behind as a
// stray positional that kiro-cli would reject.
var valueBearing = map[string]bool{
	flagAgentEngine: true,
	flagTrustTools:  true,
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

// FilterACPArgs drops the three flags vibekit's own invariants own, together
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
	default:
		return "", false
	}
}
