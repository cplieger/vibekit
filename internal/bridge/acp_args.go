// Operator-supplied kiro-cli launch flags: the VIBEKIT_KIRO_ACP_ARGS filter.
//
// kiro-cli acp accepts eight flags. vibekit emits --agent-engine and
// --auth-method, refuses six flag families that conflict with its invariants,
// and leaves --agent, -v, and future flags available to operators.

package bridge

import (
	"log/slog"
	"strings"
)

const (
	flagAgentEngine     = "--agent-engine"
	flagAuthMethod      = "--auth-method"
	flagAuthMethodAlias = "--authMethod"
	flagTrustAll        = "--trust-all-tools"
	flagTrustAllShort   = "-a"
	flagTrustTools      = "--trust-tools"
	flagModel           = "--model"
	flagEffort          = "--effort"
)

var valueBearing = map[string]bool{
	flagAgentEngine:     true,
	flagAuthMethod:      true,
	flagAuthMethodAlias: true,
	flagTrustTools:      true,
	flagModel:           true,
	flagEffort:          true,
}

// ParseACPArgs splits an operator-supplied flag string and filters refused flags.
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

// FilterACPArgs drops flags owned by vibekit's wire and session configuration.
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
			skipValue = valueBearing[name] && !hasInlineValue
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

func refuseReason(name string) (reason string, refused bool) {
	switch name {
	case flagAgentEngine:
		return "vibekit is v3-only on the wire; the v2 handlers were removed, so v1/v2 would stall session/new", true
	case flagAuthMethod, flagAuthMethodAlias:
		return "kiro-cli rejects an invalid auth method and exits before initialize, so it would kill every chat bridge; vibekit fixes relay authentication to cli", true
	case flagModel, flagEffort:
		return "kiro-cli refuses this alongside --agent-engine=v3 and exits before initialize, so it would kill every chat bridge; pick the model and reasoning effort per chat in the composer instead", true
	case flagTrustAll, flagTrustAllShort, flagTrustTools:
		return "inert on v3; tool authorization is kiro-cli's Cedar policy; edit permissions.yaml (Settings → Permissions) instead", true
	default:
		return "", false
	}
}
