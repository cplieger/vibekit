package command

import "maps"

import "github.com/cplieger/vibekit/internal/api"

// JSON protocol key constants used across command response maps and
// pending-permission payloads. These are wire-format identifiers
// documented in vibekit.md / vibekit-acp.md; centralising them keeps
// the goconst linter happy and makes renames visible in one place.
const (
	keyError    = "error"
	keyName     = api.JSONKeyName
	keyOptions  = "options"
	keyResolved = "resolved"
	keySessions = "sessions"
	keyText     = api.ContentKeyText
	keyType     = api.ContentKeyType
)

// keySessionID references the canonical api.KeySessionID constant.
const keySessionID = api.KeySessionID

// ellipsis is the truncation suffix for display strings (session
// titles, prompt previews, shell command labels). Kept as a constant
// so the same visual indicator is used everywhere.
const ellipsis = "..."

// resolvedResponse is the typed wire shape for commands that report
// how many pending changes were resolved. Replaces ad-hoc
// map[string]any{"ok": true, "resolved": N} literals.
type resolvedResponse struct {
	OK       bool `json:"ok"`
	Resolved int  `json:"resolved"`
}

// responseOK is the standard success response for commands that have
// no meaningful return value. Shared across all command handlers to
// avoid allocating a new map on every call — the map is never mutated.
var responseOK = map[string]bool{"ok": true}

// responseWith returns a success response map with the given extra
// fields merged in. Every response includes "ok": true; callers supply
// only the command-specific payload fields.
func responseWith(extra map[string]any) map[string]any {
	m := make(map[string]any, len(extra)+1)
	m["ok"] = true
	maps.Copy(m, extra)
	return m
}
