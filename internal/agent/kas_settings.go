package agent

import (
	"context"

	"github.com/cplieger/vibekit/internal/settings"
)

// The two vibekit settings whose value has to reach the AGENT, resolved into
// StartOpts at every spawn.
//
// They are here rather than beside their UI because of what they are NOT: they
// look like the kiro-cli feature flags in Settings → General and they are a
// different mechanism entirely. Measured on the stock 2.19.2 KAS bundle, the
// kiro-cli settings store is unreachable from KAS's ACP path — zero occurrences
// of cli.json, kiro-cli/settings, readSettingsFile and loadCliSettings, and each
// chat.* literal appearing exactly once as a @see cross-reference inside the
// settings schema rather than as a read. So `toolSearch.enabled` and
// `chat.enableKnowledge` could never change a running chat however they were
// written, and both controls moved onto the keys KAS does read:
// `_meta.kiro.settings.toolSearch` and the two `knowledge` rows.
//
// Read PER SPAWN, like securityPresets and for the same reason. A bridge factory
// runs per chat, so a value captured at construction would pin every later chat
// to whatever was set when the server booted, and the flip a user just made would
// never arrive. Neither setting is live: KAS resolves both at session creation
// and freezes them for that session's life, so a change reaches a chat when its
// session next starts or loads, which is what the UI hint has to say.
//
// Each default is stated HERE rather than inferred from a zero value, because the
// two defaults disagree and only one of them is the zero.

// toolSearchEnabled reports whether a session should ship KAS's tool_search tool
// instead of every MCP tool's full description.
//
// Defaults OFF, matching kiro-cli's own default. Tool search trades one extra
// round-trip per tool the agent decides to use against the context every tool
// description costs on every turn, so it earns its keep at 5+ MCP servers and
// costs latency below that. An absent key is therefore the same answer as an
// explicit false, which is why this one needs no entry in settings.Default().
func toolSearchEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyToolSearchEnabled, &b) {
		return false
	}
	return b
}

// knowledgeEnabled reports whether a session gets the knowledge feature: the
// index listing in msg0 (the `knowledge` capability) and KAS's Knowledge tool
// (the `knowledge` setting). Both, because they are two thirds of one gate.
//
// Defaults ON, and the polarity is the whole reason this function exists rather
// than a bare FieldInto at each call site. The knowledge index, its REST surface
// and its UI all predate this switch, so reading an absent key as the zero value
// would take the knowledge tool away from every existing install on the first
// boot after the upgrade — silently, in exactly the way the third knowledge key
// was added to fix. settings.Default() advertises the same true so the client's
// checkbox renders the state in force rather than an unset-means-off guess.
//
// It does NOT gate `_kiro/knowledge`, whose handler consults neither key, so the
// knowledge panel still lists and edits the store with the switch off. That is
// deliberate: the switch decides what the AGENT can reach, not what a person can.
func knowledgeEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyKnowledgeEnabled, &b) {
		return true
	}
	return b
}
