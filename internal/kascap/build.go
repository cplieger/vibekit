package kascap

import "maps"

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

// SessionMeta returns the _meta.kiro map for the session door.
//
// It is EMPTY today, because every key vibekit declares rides initialize. It is
// not wired into session/new for that reason: sending an empty _meta.kiro there
// would add bytes to a call that carries none today. The caller that sends it
// arrives with the first row that needs it.
func SessionMeta(s Spawn) map[string]any { return buildDoor(doorSession, s) }

// buildDoor projects the table onto one door.
func buildDoor(d door, s Spawn) map[string]any {
	out := make(map[string]any, len(table))
	settings := make(map[string]any)
	for _, row := range table {
		if row.door != d || !row.send {
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

// cloneValue returns a value the caller can hold without aliasing the table.
// Only a map needs it: a settings row's value object is built once at package
// init and would otherwise be shared by every build, so a caller mutating one
// returned payload would change the next one. One level is enough because every
// value inside those objects is a bool.
func cloneValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		return maps.Clone(m)
	}
	return v
}
