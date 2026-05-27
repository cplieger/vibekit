// Centralized defaults and the known-keys validation set for
// vibekit-managed `<configDir>/config.json`. The HTTP GET handler
// emits DefaultSettings() when the file is missing; PATCH/PUT
// handlers call WarnUnknownKeys to surface typos and CLI/UI drift
// without rejecting forward-compatible keys (per AUTH/SET design
// discussion in the rewrite-analysis docs: a `knownKeys` warning
// is the right level — full struct validation is too rigid for a
// preference file that gains keys per release).

package settings

import "log/slog"

// DefaultSettings returns the canonical defaults the GET /api/settings
// handler emits when config.json is missing or unreadable. Keep this
// aligned with the frontend's `AppSettings` interface in
// `static-src/persist.ts`; new top-level preferences should land in
// both places (and in KnownKeys below).
//
// Per-key consumer-side defaults still live near their consumers
// (e.g. ignore.go's "[.kiroignore]" default for agent_ignore_files,
// logctl.go's false for debug_logs) because the consumer knows the
// fail-mode policy. This function is for the wire shape the GET
// endpoint returns when no file exists yet, not the in-process
// fallback every consumer applies.
func DefaultSettings() map[string]any {
	return map[string]any{
		"auto_update": true,
	}
}

// KnownKeys is the set of vibekit-managed config.json keys. PATCH
// handlers warn (but do not reject) keys outside this set so a typo
// or stray field surfaces in operator logs without breaking forward
// compatibility with newer frontend versions that introduce new keys
// before this list is updated. Add new keys here when the frontend's
// `AppSettings` interface grows.
//
// Note: kiro-cli's own settings (cleanup.periodDays, chat.enable*,
// etc.) live in a separate file (~/.kiro/settings/config.json) and
// are not part of this set.
var KnownKeys = map[string]struct{}{
	"agent_ignore_files":    {},
	"auto_update":           {},
	"debug_logs":            {},
	"last_model":            {},
	"model_effort":          {},
	"notifications_enabled": {},
	"notify_agent_finished": {},
	"notify_permission":     {},
	"permission_mode":       {},
	"shell_policy":          {},
	"supervised_default":    {},
	"trust_tools":           {},
}

// WarnUnknownKeys logs a warning for each top-level key in keys that
// isn't recognized by KnownKeys. Returns the slice of unknown keys
// for callers that want to surface them in HTTP responses or
// telemetry; the slice is sorted-stable nil when every key is known.
// source identifies the call site for log correlation (e.g. "PATCH
// /api/settings", "PUT /api/settings").
func WarnUnknownKeys(keys []string, source string) []string {
	var unknown []string
	for _, k := range keys {
		if _, ok := KnownKeys[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slog.Warn("settings: unknown keys in write",
			"source", source,
			"keys", unknown)
	}
	return unknown
}
