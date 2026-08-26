// ---------------------------------------------------------------------------
// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g.
// theme toggle + notification toggle changes into one round-trip).
//
// Fetch calls go through api-client.ts for consistent error handling.
// ---------------------------------------------------------------------------

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { patchAppSettings, loadSettings as loadSettingsAction } from "./actions/settings.js";
import { registerCleanup } from "./actions/index.js";

export interface AppSettings {
  last_model?: string;
  /** The reasoning-effort level the user picked last, anywhere. The twin of
   *  last_model and used the same way: a NEW chat opens on it instead of on the
   *  current model's default tier.
   *
   *  A seed, never a store. The level still lives on the chat record
   *  (Session.effort) and this is only consulted when a chat has chosen nothing,
   *  so two chats can still disagree. Reconciled against the current model's own
   *  tier list before it is marked, because a level the new model does not offer
   *  would be a tier the service rejects. */
  last_effort?: string;
  notifications_enabled?: boolean;
  notify_agent_finished?: boolean;
  /** CI on a pull request the connected identity opened turned green or red.
   *  Keyed like agent_finished (both are switchable channels); the poller behind
   *  it is server-side because a client poll cannot fire with the tab closed. */
  notify_pr_status?: boolean;
  // No notify_permission: the permission ask is a floor, not a preference.
  // See the "no notify_permission key" note in internal/settings/defaults.go.
  agent_ignore_files?: string[];
  debug_logs?: boolean;
  /** Default Supervised-mode state for new chats. When true, new
   *  chats start with SupervisedMode=true and every file change
   *  the agent makes stages for user review before hitting disk.
   *  Per-chat toggle is on the chat prompt row (Supervised pill). */
  supervised_default?: boolean;
  /** Approve (rather than refuse) a scheduled run's tool request when the
   *  unattended budget expires. Off by default; see run_unattended.go. */
  scheduled_auto_approve?: boolean;
  // There is no model_effort. Reasoning effort is per-chat, on the chat record
  // beside model, mode and supervised (Session.effort); it used to be one global
  // setting shaped {last_model, effort}, so two chats could not disagree and
  // switching models discarded the previous model's level. last_effort above is
  // not that key returning: it carries only the MEMORY of the last pick, as a
  // bare level with per-chat storage intact.
  /** Chat retention, owned end to end by vibekit (kiro-cli's own
   *  cleanup.periodDays is pinned to 0/never). Encoding: -1 = forever
   *  (close keeps the chat, never purged — "backups"), 0 = off (delete on
   *  close, History hidden — ephemeral), N = keep N days (close keeps the
   *  chat, purged after N). There is no archive directory: "archived" is
   *  computed from the chat's age against the window. */
  chat_retention_days?: number;
  /** Whether the agent gets the knowledge feature: the list of indexed bases in
   *  its system prompt and KAS's own knowledge search tool. Defaults TRUE, which
   *  is why the server sends it in GET /api/settings rather than leaving it
   *  absent — an unset key read as the zero value would render this switch off
   *  while the feature was on. It does not gate the knowledge PANEL, which reads
   *  the store through an RPC that consults neither key. */
  knowledge_enabled?: boolean;
  /** Whether a session ships KAS's tool_search tool instead of every MCP tool's
   *  full description. Defaults false, matching kiro-cli's own default.
   *
   *  Both of these are vibekit settings that reach the AGENT: internal/agent
   *  resolves each at spawn time into the _meta.kiro.settings keys KAS reads.
   *  They were kiro-cli settings until 2026-08, and that door reaches no running
   *  chat. Neither is live — KAS freezes both at session creation. */
  tool_search_enabled?: boolean;
  /** Whether a session opts into kiro-cli's memory subsystem. Defaults false, and
   *  here the zero value IS the answer, unlike knowledge_enabled — memory is a
   *  feature vibekit has never had, so an absent key means nobody asked for it and
   *  the server leaves it out of GET /api/settings.
   *
   *  Off is not a quiet state on the wire. The server still SENDS the
   *  `userMemoryOptIn` veto, because kiro-cli reads an absent key as "no opinion,
   *  let the experiment decide" and only an explicit false refuses; withholding it
   *  is what would let a backend rollout turn memory on with no setting and no
   *  signal. On also contributes an environment variable to the agent process,
   *  which is the only lever that can make the feature eligible at all. */
  memory_enabled?: boolean;
  /** The theme choice: "dark", "light" or "system". Absent means nothing was
   *  chosen, which resolves to the OS preference.
   *
   *  Server-owned since the workspace arrangement was modelled: it used to be a
   *  field in `ui-state.json`, and it is a workspace preference rather than
   *  anything about tabs, so it came here when that document went. "system" is a
   *  real stored CHOICE — the user asked to follow the OS — and dropping it is
   *  what once made Auto unreachable after a single toggle click.
   *
   *  ALSO cached in this browser's localStorage, which is not a second source of
   *  truth: the inline pre-paint snippet has to pick a theme before any fetch
   *  resolves. `settings.ts` owns that cache's policy; the server always wins
   *  after the settings load. */
  theme?: string;
  /** The file browser's last directory. Server-owned for the arrangement's
   *  reason: it is where this WORKSPACE was being browsed, so a second device
   *  should open there too. Empty lists the granted mounts. */
  fb_path?: string;
}

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<AppSettings> = {};
let patchSnapshot: Partial<AppSettings> = {};
let patchInputs: HTMLInputElement[] = [];
let patchGen = 0;
let patchResolvers: ((r: Record<string, unknown> | null) => void)[] = [];

/** Last-known value per settings key. Seeded by initSettingsTracking()
 *  on app boot from /api/settings; updated by patchSettings() as we
 *  send writes. Used to filter no-op writes (same-value PATCHes that
 *  trigger the saving animation for nothing — e.g. the bootstrap
 *  fire of repo-picker's onSelectionChange persisting the already-
 *  saved git_repo on every page load). */
let lastSentPatch: Partial<AppSettings> = {};

/** Seed the dedup tracker from the loaded settings. Called once at
 *  app boot before any patchSettings() can fire. Without this, the
 *  first patch for any key after page load is treated as a change
 *  even when the value matches the server. */
export function initSettingsTracking(s: AppSettings): void {
  lastSentPatch = { ...s };
}

/** @internal Reset module state for tests. */
export function __testResetTracking(): void {
  lastSentPatch = {};
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
  }
}

/** Flush any pending debounced PATCH immediately (fire-and-forget). */
function flushPendingPatch(): void {
  if (Object.keys(patchQueue).length === 0) {
    return;
  }
  executePatch();
}

/** Shared dispatch body: drains the queue, dispatches the PATCH, and
 *  resolves all pending promises. Used by both flushPendingPatch (sync
 *  flush on beforeunload) and the debounce timer callback. */
function executePatch(): void {
  const body = patchQueue;
  const allInputs = patchInputs;
  const resolvers = patchResolvers;
  const rollback = patchSnapshot;
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  const gen = ++patchGen;
  let result: Record<string, unknown> | null = null;
  void patchAppSettings.dispatch(
    {
      body: body,
      ...(allInputs.length > 0 ? { inputs: allInputs } : {}),
    },
    {
      silent: true,
      onSuccess: (r) => {
        result = r as Record<string, unknown>;
        if (gen === patchGen) {
          showSaved();
        }
      },
      onError: () => {
        Object.assign(lastSentPatch, rollback);
        if (gen === patchGen) {
          showError();
        }
      },
      onSettled: () => {
        for (const resolve of resolvers) {
          resolve(result);
        }
      },
    },
  );
}

registerCleanup(() => {
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
    flushPendingPatch();
  }
});

export function patchSettings(
  patch: Partial<AppSettings>,
  ...inputs: HTMLInputElement[]
): Promise<Record<string, unknown> | null> {
  // Filter out keys whose value matches the last-sent value. JSON
  // equality is good enough for the AppSettings shape (primitives +
  // arrays of strings); avoids reflecting no-op writes back to the
  // server and prevents the "Saving..." animation from firing on
  // bootstrap subscriptions like onSelectionChange's immediate fire.
  const changed: Partial<AppSettings> = {};
  for (const k of Object.keys(patch) as (keyof AppSettings)[]) {
    if (JSON.stringify(patch[k]) !== JSON.stringify(lastSentPatch[k])) {
      if (!(k in patchSnapshot)) {
        Object.assign(patchSnapshot, { [k]: lastSentPatch[k] });
      }
      Object.assign(changed, { [k]: patch[k] });
      Object.assign(lastSentPatch, { [k]: patch[k] });
    }
  }
  if (Object.keys(changed).length === 0) {
    // No-op: nothing to send. Resolve immediately with null so callers
    // that await the promise don't hang.
    return Promise.resolve(null);
  }
  Object.assign(patchQueue, changed);
  for (const input of inputs) {
    if (!patchInputs.includes(input)) {
      patchInputs.push(input);
    }
  }
  const p = new Promise<Record<string, unknown> | null>((resolve) => {
    patchResolvers.push(resolve);
  });
  if (patchTimer !== undefined) {
    return p;
  }
  showSaving();
  patchTimer = setTimeout(() => {
    patchTimer = undefined;
    executePatch();
  }, 300);
  return p;
}

export async function loadSettings(): Promise<AppSettings> {
  const s = await loadSettingsAction.dispatch(undefined);
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  return (s as AppSettings) ?? {};
}
