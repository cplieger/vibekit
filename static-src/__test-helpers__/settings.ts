import type { EffectiveSettings } from "../wire/types.gen.js";

/** A complete GET /api/settings payload, overridable per field.
 *
 *  Every field of EffectiveSettings is required, because the server resolves
 *  defaults underneath the stored document — so a test cannot build the payload
 *  from the two keys it cares about any more, and before this helper 25 call sites
 *  each listed all fifteen. It also means adding a field to the wire type is one
 *  edit here rather than one per test.
 *
 *  The values mirror settings.EffectiveDefaults() in Go. They are NOT imported
 *  from anywhere: a fixture that derives its expectations from the code under test
 *  asserts nothing, and the point of most callers is to state a value explicitly
 *  and watch the UI follow it. Keep them in step by hand — the Go side is pinned by
 *  its own tests, and a drift here shows up as a test whose stated default stops
 *  matching the one the server sends.
 */
export function settingsPayload(overrides: Partial<EffectiveSettings> = {}): EffectiveSettings {
  return {
    agent_ignore_files: [".gitignore", ".kiroignore"],
    chat_retention_days: 7,
    theme: "",
    fb_path: "",
    last_model: "",
    last_effort: "",
    last_effort_model: "",
    knowledge_enabled: true,
    tool_search_enabled: false,
    memory_enabled: false,
    notifications_enabled: false,
    notify_agent_finished: true,
    notify_pr_status: true,
    supervised_default: false,
    scheduled_auto_approve: false,
    debug_logs: false,
    ...overrides,
  };
}
