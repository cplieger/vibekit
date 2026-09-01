package agent

import (
	"context"

	"github.com/cplieger/vibekit/internal/settings"
)

// The two vibekit settings whose value has to reach the AGENT, resolved into
// StartOpts at every spawn.
//
// These are not the kiro-cli feature flags in Settings → General: measured
// on the stock 2.19.2 KAS bundle, the kiro-cli settings store is unreachable
// from KAS's ACP path (zero occurrences of cli.json, kiro-cli/settings,
// readSettingsFile, loadCliSettings), so `toolSearch.enabled` and
// `chat.enableKnowledge` moved onto the keys KAS does read:
// `_meta.kiro.settings.toolSearch` and the two `knowledge` rows.
//
// Read PER SPAWN, like securityPresets: a bridge factory runs per chat, so a
// value captured at construction would pin every later chat to whatever was
// set when the server booted. Neither setting is live — KAS resolves both at
// session creation and freezes them for that session's life.

// toolSearchEnabled reports whether a session should ship KAS's tool_search
// tool instead of every MCP tool's full description.
//
// Defaults OFF, matching kiro-cli's own default: tool search trades one
// extra round-trip per tool the agent decides to use against the context
// every tool description costs on every turn, earning its keep only at 5+
// MCP servers. An absent key therefore already reads as false, so no
// settings.Default() entry is needed.
func toolSearchEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyToolSearchEnabled, &b) {
		return false
	}
	return b
}

// knowledgeEnabled reports whether a session gets the knowledge feature: the
// index listing in msg0 and KAS's Knowledge tool. Both, because they are two
// thirds of one gate.
//
// Defaults ON: the knowledge index, its REST surface and its UI all predate
// this switch, so reading an absent key as false would silently take the
// knowledge tool away from every existing install on first boot after the
// upgrade. settings.Default() advertises true to match.
//
// It does NOT gate `_kiro/knowledge` — that handler consults neither key, so
// the knowledge panel still lists and edits the store with the switch off.
// The switch decides what the AGENT can reach, not what a person can.
func knowledgeEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyKnowledgeEnabled, &b) {
		return true
	}
	return b
}

// memoryEnabled reports whether a session opts into kiro-cli's memory
// subsystem: the `userMemoryOptIn` row's value and the child environment's
// KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED.
//
// Defaults OFF and the zero value IS the answer here (unlike
// knowledgeEnabled), so no settings.Default() entry is needed. Off is not a
// quiet state though — it still SENDS the veto, because an absent key reads
// as "let the experiment decide" and an AWS-side ramp can flip that
// silently.
//
// Per spawn like its siblings, and doubly not live: the gate is frozen at
// session creation AND the environment is fixed when the subprocess starts.
func memoryEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyMemoryEnabled, &b) {
		return false
	}
	return b
}
