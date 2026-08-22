package kascap

import (
	"maps"
	"slices"

	"github.com/cplieger/envx/v2"
)

// settingsKey is the container every resolverSetting row lands in. KAS reads
// _meta.kiro.settings as one object and resolves each member independently, so
// the container is derived from the rows rather than declared as a row of its
// own: with no settings sent there is no settings key.
const settingsKey = "settings"

// Capabilities returns the _meta.kiro map for the initialize handshake, ready
// to sit under clientCapabilities._meta.kiro.
//
// The returned map is the caller's own: it shares no object with the table, so
// a caller may hold or modify it without reaching back into this package.
func Capabilities(s Spawn) map[string]any { return buildDoor(doorConnection, s) }

// SessionMeta returns the _meta.kiro map for the session door, ready to sit
// under a session/new or session/load request's _meta.kiro.
//
// The caller sends it on BOTH verbs and only when it is NON-EMPTY, so a table
// that declares no session key adds no bytes to a call that carries none. Like
// Capabilities, the returned map is the caller's own.
func SessionMeta(s Spawn) map[string]any { return buildDoor(doorSession, s) }

// buildDoor projects the table onto one door.
func buildDoor(d door, s Spawn) map[string]any {
	out := make(map[string]any, len(table))
	settings := make(map[string]any)
	for i := range table {
		row := &table[i]
		if row.door != d || !sends(row) {
			continue
		}
		value := row.value
		if row.gate != nil {
			gated, present := row.gate(s)
			if !present {
				continue
			}
			value = gated
		}
		if row.resolver == resolverSetting {
			settings[row.key] = cloneValue(value)
			continue
		}
		out[row.key] = cloneValue(value)
	}
	if len(settings) > 0 {
		out[settingsKey] = settings
	}
	return out
}

// sends resolves a row's send at RUNTIME, applying its env override.
//
// A row with no env keeps its compiled send, so the common case reads no
// environment at all. A row that names one hands the decision to envx.Bool with
// the compiled send as the fallback, which is what makes the override
// disable-only in practice: the column may only sit on a send:true row, so the
// operator's reachable states are "still true" and "false".
//
// Read per build rather than once at init: nothing here caches, and a capability
// projection is built once per bridge spawn, so the cost is one os.LookupEnv per
// env-bearing row on a call that already starts a subprocess.
//
// Takes a pointer because decl is well past gocritic's hugeParam threshold.
func sends(row *decl) bool {
	if row.env == "" {
		return row.send
	}
	return envx.Bool(row.env, row.send)
}

// cloneValue returns a value the caller can hold without aliasing the table.
//
// Maps and slices both need it, and for the same reason: such a value is built
// once at package init and would otherwise be shared by every build, so a caller
// mutating one returned payload would change the next one. Bools and strings are
// copied by assignment and fall through.
//
// One level is enough for both shapes as the table stands: every value inside a
// settings object is a bool, and the one slice row holds strings. A row whose
// value nests a container inside a container would need a deeper clone, and
// TestBuildersDoNotAliasTheTable is where that would be caught.
func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return maps.Clone(t)
	case []string:
		return slices.Clone(t)
	default:
		return v
	}
}
