package command

import "vibekit/internal/api"

// JSON protocol key constants used across command response maps and
// pending-permission payloads. These are wire-format identifiers
// documented in vibekit.md / vibekit-acp.md; centralising them keeps
// the goconst linter happy and makes renames visible in one place.
const (
	keyError    = "error"
	keyName     = "name"
	keyOptions  = "options"
	keyResolved = "resolved"
	keySessions = "sessions"
	keyText     = "text"
	keyType     = "type"
)

// keySessionID references the canonical api.KeySessionID constant.
const keySessionID = api.KeySessionID



// ellipsis is the truncation suffix for display strings (session
// titles, prompt previews, shell command labels). Kept as a constant
// so the same visual indicator is used everywhere.
const ellipsis = "..."

// responseOK is the standard success response for commands that have
// no meaningful return value. Shared across all command handlers to
// avoid allocating a new map on every call — the map is never mutated.
var responseOK = map[string]bool{"ok": true}
