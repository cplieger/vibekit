package command

// JSON protocol key constants used across command response maps and
// pending-permission payloads. These are wire-format identifiers
// documented in vibekit.md / vibekit-acp.md; centralising them keeps
// the goconst linter happy and makes renames visible in one place.
const (
	keyError     = "error"
	keyName      = "name"
	keyOptions   = "options"
	keyResolved  = "resolved"
	keySessionID = "sessionId"
	keySessions  = "sessions"
	keyText      = "text"
	keyType      = "type"
)

// contentTypeText is the ACP content-type tag used as the VALUE of the
// `type` field inside a message content block (e.g. `{"type": "text",
// "text": "..."}`). Distinct from keyText (which is the field key) even
// though both happen to be the string "text".
const contentTypeText = "text"

// ellipsis is the truncation suffix for display strings (session
// titles, prompt previews, shell command labels). Kept as a constant
// so the same visual indicator is used everywhere.
const ellipsis = "..."
