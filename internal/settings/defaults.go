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

// Exported constants for settings key names. All consumers should
// reference these instead of bare string literals to prevent drift.
const (
	KeyAgentIgnoreFiles     = "agent_ignore_files"
	KeyChatRetentionDays    = "chat_retention_days"
	KeyDebugLogs            = "debug_logs"
	KeyLastModel            = "last_model"
	KeyModelEffort          = "model_effort"
	KeyNotificationsEnabled = "notifications_enabled"
	KeyNotifyAgentFinished  = "notify_agent_finished"
	KeySupervisedDefault    = "supervised_default"

	// KeyScheduledAutoApprove lets a SCHEDULED run's tool requests be approved
	// automatically instead of refused after the unattended budget.
	//
	// Off by default, and deliberately its OWN switch rather than a read of the
	// interactive auto-approve posture: "I approve this while watching" and
	// "approve this unattended at 03:00" are different consents, so the second
	// has to be chosen explicitly. Turning it on is informed; inheriting it
	// would not be.
	KeyScheduledAutoApprove = "scheduled_auto_approve"
)

// There is deliberately no notify_permission key.
//
// A permission ask BLOCKS the turn: nothing proceeds until it is answered, and
// off-screen there is no other marker for one (a background chat waiting on an
// approval renders identically to one that is working). So a switch that
// silenced the ask was not a preference — it was a way to stall every later
// turn of every chat with no signal, discoverable only by noticing that work
// had stopped. The permission notice is a FLOOR: api.PushKindPermission is
// registered with no settings key, so no value in config.json can turn it off
// (pinned by push.TestPermissionKindHasNoSettingsKey).
//
// What IS relaxable is the permission SYSTEM rather than the notice about it:
// the Settings -> Permissions workspace relaxation (policyfile.RelaxCapabilities)
// writes broad allow rules, so the asks stop happening instead of happening
// silently. The master notifications_enabled switch still turns everything off
// together, which is a deliberate and comprehensive choice rather than one
// channel quietly going dark while the others keep arriving.

// DefaultChatRetentionDays is the seeded default for chat_retention_days.
//
// vibekit owns chat retention end to end (kiro-cli's own cleanup.periodDays
// is pinned to 0/never so the two systems never both purge). The value is a
// day count with two sentinels:
//
//	-1 = forever  — closing a tab archives to History; never purged ("backups").
//	 0 = off      — closing a tab deletes the chat (ephemeral); History hidden.
//	 N = keep N days — archive on close; the purge scheduler removes after N days.
//
// The server purge scheduler treats <= 0 as "no purge" (off AND forever); the
// client decides archive-vs-delete on close from the same value (enabled when
// != 0). Default 1 preserves the prior 1-day behavior.
const DefaultChatRetentionDays = 1

// DefaultAgentIgnoreFiles is the seeded default for the agent_ignore_files
// setting: the ignore-file basenames the agent read filter (internal/ignore,
// wired into the fs/read_text_file A→C path) applies, each resolved against
// the workspace root. A fresh install — no config.json, or a config.json that
// predates the key — uses this list so gitignored secrets (.env.dec, anything
// under a gitignored secrets/ dir) are refused from agent reads out of the box
// instead of the filter being opt-in.
//
// The names track the Kiro IDE's recognized ignore-file set (its context
// walker keys on .gitignore/.continueignore/.kiroignore/.cursorignore). We
// seed the two relevant to this stack — .gitignore (the universal exclude
// file) and .kiroignore (Kiro's own) — and skip the competitor-tool files.
// This is deliberately MORE protective than the IDE's own out-of-box behavior:
// the IDE's kiroAgent.agentIgnoreFiles setting is undeclared and read as
// `get("agentIgnoreFiles") ?? []`, so the IDE filters agent reads only through
// the always-on user-global ~/.kiro/settings/kiroignore + git global excludes,
// not workspace .gitignore. Turning the workspace filter ON by default is the
// settled vibekit decision.
//
// Returns a fresh slice so callers can resolve/append without mutating the
// shared default. An explicit [] in config.json is honoured as an opt-out
// (see internal/ignore).
func DefaultAgentIgnoreFiles() []string {
	return []string{".gitignore", ".kiroignore"}
}

// DefaultSettings returns the canonical defaults the GET /api/settings
// handler emits when config.json is missing or unreadable. Every key it emits
// must also be in KnownKeys (enforced by TestDefaultSettings_OnlyKnownKeys) so
// a fresh GET response round-tripped back as a PATCH never trips the
// unknown-key warning.
//
// agent_ignore_files carries a real default (DefaultAgentIgnoreFiles) so the
// agent read filter is ON out of the box and the GET-when-missing wire shape
// advertises it. Preferences NOT listed here apply their default in-process
// near their consumer (e.g. logctl.go's false for debug_logs) because the
// consumer owns the fail-mode policy; those need not ride this wire shape.
func DefaultSettings() map[string]any {
	return map[string]any{
		KeyAgentIgnoreFiles:  DefaultAgentIgnoreFiles(),
		KeyChatRetentionDays: DefaultChatRetentionDays,
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
// etc.) live in a separate file ($KIRO_HOME/settings/settings.json) and
// are not part of this set.
var KnownKeys = map[string]struct{}{
	KeyAgentIgnoreFiles:     {},
	KeyChatRetentionDays:    {},
	KeyDebugLogs:            {},
	KeyLastModel:            {},
	KeyModelEffort:          {},
	KeyNotificationsEnabled: {},
	KeyNotifyAgentFinished:  {},
	KeySupervisedDefault:    {},
	KeyScheduledAutoApprove: {},
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

// ServerManagedKeys are settings keys owned by flows other than a full-file
// PUT of the settings object: agent_ignore_files (written by the
// Settings→Permissions UI) and model_effort (written by the model switcher),
// both via PATCH. A PUT whose body omits them must not silently wipe them, so
// handleSettingsWrite carries any omitted managed key over from the existing
// file. Kept beside KnownKeys so the managed-key set stays in the settings
// domain and references the same key constants (no drift).
func ServerManagedKeys() []string {
	return []string{KeyAgentIgnoreFiles, KeyModelEffort}
}
