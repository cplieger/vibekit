// Centralized defaults and the known-keys validation set for
// vibekit-managed `<configDir>/config.json`. The HTTP GET handler
// emits Default() when the file is missing; PATCH/PUT
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
	KeyNotificationsEnabled = "notifications_enabled"

	// KeyLastEffort is the reasoning-effort level the user picked last, anywhere.
	// The twin of KeyLastModel and used the same way: a NEW chat opens on it
	// instead of on the current model's default tier.
	//
	// A seed, never a store. The chat record still owns the level (Chat.Effort),
	// this value is only consulted when a chat has chosen nothing, and it is never
	// written onto the record — a chat that follows the seed has to keep following
	// it, and stamping today's value on would freeze that chat there forever. Two
	// readers, and they have to agree or the pill lies about what the session
	// runs: BridgeCoordinator.effortFor resolves StartOpts.Effort, and the client's
	// effortVocabulary marks the tier.
	//
	// Not the old model_effort key returning. That one was a single global
	// `{last_model, effort}` pair keyed by the LAST model, so two chats could not
	// disagree and switching models discarded the previous model's choice. This is
	// a bare level with per-chat storage intact, reconciled against the current
	// model's own tier list at both readers, so a level the new model does not
	// offer falls through to that model's default rather than being sent.
	KeyLastEffort          = "last_effort"
	KeyNotifyAgentFinished = "notify_agent_finished"
	KeyNotifyPRStatus      = "notify_pr_status"
	KeySupervisedDefault   = "supervised_default"

	// KeySecurityProfile is the named security posture every session vibekit
	// starts opens with: one of policyfile's profile ids, resolved into KAS policy
	// preset ids and sent as _meta.kiro.policyPreset.
	//
	// GLOBAL rather than per chat, and that is a measured limit rather than a
	// simplification. KAS offers no way to change a live session's policy — no
	// set_config_option id and no client-callable setter — so a per-chat level
	// could only take effect on the next session start, and the per-chat control
	// was dropped for that reason (vibekit-acp.md has the enumeration). One
	// instance is also one HOME, one user and one workspace root, so a global
	// setting and a per-workspace one would address the same population anyway.
	//
	// An unset or unrecognised value resolves to policyfile.DefaultProfile with a
	// logged reason rather than to Custom: Custom sends no presets, so a typo
	// would silently remove the fs_read floor from an instance that never chose to
	// and leave the agent asking permission to read a file.
	KeySecurityProfile = "security_profile"

	// KeyScheduledAutoApprove lets a SCHEDULED run's tool requests be approved
	// automatically instead of refused after the unattended budget.
	//
	// Off by default, and deliberately its OWN switch rather than a read of the
	// interactive auto-approve posture: "I approve this while watching" and
	// "approve this unattended at 03:00" are different consents, so the second
	// has to be chosen explicitly. Turning it on is informed; inheriting it
	// would not be.
	KeyScheduledAutoApprove = "scheduled_auto_approve"

	// KeyToolSearchEnabled and KeyKnowledgeEnabled are the two settings whose
	// value has to reach the AGENT rather than only kiro-cli, and they are the
	// reason a vibekit setting can now drive a kascap gate at all.
	//
	// Both used to be written through /api/kiro-settings as `toolSearch.enabled`
	// and `chat.enableKnowledge`. Measured on the stock 2.19.2 KAS bundle, that
	// endpoint cannot change a running chat: KAS's ACP path reads no kiro-cli
	// setting anywhere (zero occurrences of cli.json, kiro-cli/settings,
	// readSettingsFile and loadCliSettings; each chat.* literal appears exactly
	// once, as a @see cross-reference in the settings schema). The keys that DO
	// reach it are `toolSearch` and `knowledge` under _meta.kiro.settings, so the
	// controls now write here and internal/agent resolves each at spawn time into
	// StartOpts, kascap.Spawn and the wire.
	//
	// Resolved PER SPAWN rather than captured at construction, because a bridge
	// factory runs per chat: reading the value once would pin every later chat to
	// whatever was set when the server booted.
	//
	// KeyKnowledgeEnabled defaults TRUE and is therefore in Default(), unlike its
	// sibling. An absent key must not read as off — the knowledge index, its REST
	// surface and its UI all predate this switch, so a zero-value false would
	// silently take the knowledge tool away from every existing install on the
	// first boot after the upgrade.
	KeyToolSearchEnabled = "tool_search_enabled"
	KeyKnowledgeEnabled  = "knowledge_enabled"

	// KeyMemoryEnabled opts INTO kiro-cli's memory subsystem, and it is the one
	// setting here that has to move two levers at once, because neither alone can
	// decide the question.
	//
	// The `userMemoryOptIn` row vetoes; the child environment's
	// KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED is the only thing that can make memory
	// ELIGIBLE. resolveMemoryEnabled reads AB_MEMORY_INTERNAL first and consults
	// the external arm only when the internal one reads "disabled", and
	// AB_MEMORY_INTERNAL is absent from ENV_FEATURE_VARIABLES — so an AWS-side ramp
	// of the internal arm bypasses the variable entirely and only the veto closes
	// it, while a ramp that never comes leaves the variable as the only way to open
	// it. Off therefore still SENDS `{"enabled": false}` rather than going quiet.
	//
	// Defaults OFF, and deliberately not in Default(): the zero value is the safe
	// state here, which is the opposite of KeyKnowledgeEnabled above. This is a
	// feature nothing in vibekit has ever had, so an absent key means nobody asked
	// for it, and the standing verdict is that curation beats automatic capture —
	// see the userMemoryOptIn row in internal/kascap/table.go for why the argument
	// does not expire when upstream fixes a defect.
	//
	// Not live: KAS freezes the gate at session creation and the environment is
	// fixed at spawn, so a flip reaches NEW chats only. The UI hint says so.
	KeyMemoryEnabled = "memory_enabled"

	// KeyTheme and KeyFBPath are the two fields that came here when
	// internal/uistate was deleted: the whole-document arrangement it held is a
	// modelled tab collection now (internal/tabs), and these two are the members
	// that were never about tabs at all — a workspace preference and a workspace
	// path, which is exactly what this file is.
	//
	// KeyTheme is "dark" | "light" | "system", and "system" is a real stored
	// CHOICE rather than the absence of one: it means the user asked to follow the
	// OS. An absent key is the absence, and it resolves to system at the client.
	//
	// The theme is ALSO cached in the browser's localStorage, and that is not a
	// second source of truth: the inline pre-paint snippet in static/index.html
	// has to pick a theme before any fetch can resolve, so the cache is a
	// paint-time hint this value overwrites on every load. It is also the one
	// value the uistate deletion carries across — see settings.ts.
	//
	// KeyFBPath is the file browser's last directory. Server-owned for the reason
	// the arrangement is: it is where this WORKSPACE was being browsed, so a
	// second device should open there too.
	KeyTheme  = "theme"
	KeyFBPath = "fb_path"
)

// There is deliberately no notify_permission key.
//
// A permission ask BLOCKS the turn: nothing proceeds until it is answered, and
// off-screen there is no other marker for one (a background chat waiting on an
// approval renders identically to one that is working). So a switch that
// silenced the ask was not a preference — it was a way to stall every later
// turn of every chat with no signal, discoverable only by noticing that work
// had stopped. The permission notice is a FLOOR: vibekit.PushKindPermission is
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
// != 0). Seven days is a week, so a chat is still there when someone comes back
// to it after a weekend.
//
// There is no TypeScript mirror of this value: GET /api/settings resolves the
// default underneath the stored document, so the payload always carries a real
// number and the client has nothing to fall back to.
const DefaultChatRetentionDays = 7

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

// Default returns the canonical defaults the GET /api/settings
// handler emits when config.json is missing or unreadable. Every key it emits
// must also be in KnownKeys (enforced by TestDefault_OnlyKnownKeys) so
// a fresh GET response round-tripped back as a PATCH never trips the
// unknown-key warning.
//
// agent_ignore_files carries a real default (DefaultAgentIgnoreFiles) so the
// agent read filter is ON out of the box and the GET-when-missing wire shape
// advertises it. knowledge_enabled is here for the inverse reason: it defaults
// TRUE, so a client that read an absent key as the zero value would render the
// knowledge switch off while the feature was on. Preferences NOT listed here
// apply their default in-process near their consumer (e.g. logctl.go's false for
// debug_logs, and tool_search_enabled's false in internal/agent) because the
// consumer owns the fail-mode policy; those need not ride this wire shape.
func Default() map[string]any {
	return map[string]any{
		KeyAgentIgnoreFiles:  DefaultAgentIgnoreFiles(),
		KeyChatRetentionDays: DefaultChatRetentionDays,
		KeyKnowledgeEnabled:  true,
		// Both empty, and the empty string is a REAL value here rather than a
		// placeholder: it is "nothing has been chosen". An empty theme resolves at
		// the client to the OS preference, and an empty browser path lists the
		// granted mounts. They are in this shape so a client reading the
		// GET-when-missing response finds the keys it is about to write rather
		// than inferring them, which is what the two localStorage fields they
		// replaced never had to do.
		KeyTheme:  "",
		KeyFBPath: "",
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
// etc.) live in a separate file ($KIRO_HOME/settings/cli.json) and
// are not part of this set.
//
// There is deliberately no model_effort key. Reasoning effort was one global
// setting shaped `{last_model, effort}`, so it was keyed by the LAST model rather
// than by the chat: two chats could not disagree about effort, and switching
// models discarded the previous model's choice. It is a field on the chat record
// now (vibekit.Chat.Effort), written by CmdSetEffort and applied at session/new
// through StartOpts.Effort, which is where the other three per-chat composer
// settings already lived. Nothing reads or writes the old key; a config.json that
// still carries it warns as an unknown key on the next write and is otherwise
// inert.
//
// KeyLastEffort is not that key coming back. Per-chat storage is what a new chat
// had no memory to open with, so the level was per-chat and NOTHING remembered
// the last pick — the model had getLastModel and effort had no equivalent, so
// every new chat silently reopened on the model default. KeyLastEffort restores
// only the memory, as a bare level with a fallback rung at each reader; see its
// own comment for why that avoids each of the three defects above.
var KnownKeys = map[string]struct{}{
	KeyAgentIgnoreFiles:     {},
	KeyChatRetentionDays:    {},
	KeyDebugLogs:            {},
	KeyFBPath:               {},
	KeyKnowledgeEnabled:     {},
	KeyLastEffort:           {},
	KeyLastModel:            {},
	KeyMemoryEnabled:        {},
	KeyNotificationsEnabled: {},
	KeyNotifyAgentFinished:  {},
	KeyNotifyPRStatus:       {},
	KeySupervisedDefault:    {},
	KeyScheduledAutoApprove: {},
	KeySecurityProfile:      {},
	KeyTheme:                {},
	KeyToolSearchEnabled:    {},
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
// PUT of the settings object: agent_ignore_files, written by the
// Settings→Permissions UI via PATCH, plus theme and fb_path, which arrived here
// when internal/uistate's whole-document arrangement was retired and are
// likewise PATCH-only (the theme toggle and the file browser's navigation). A
// PUT whose body omits one must not silently wipe it, so handleSettingsWrite
// carries any omitted managed key over from the existing file. Kept beside
// KnownKeys so the managed-key set stays in the settings domain and references
// the same key constants (no drift).
//
// The theme is the member where a wipe would be VISIBLE — a reader would see the
// wrong colour on the next load — which is the same reason it is the one value
// the uistate deletion carries across at all.
//
// model_effort used to be a member; effort is per-chat now (see the note above
// KnownKeys).
func ServerManagedKeys() []string {
	return []string{KeyAgentIgnoreFiles, KeyTheme, KeyFBPath}
}
