// ---------------------------------------------------------------------------
// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g.
// theme toggle + notification toggle changes into one round-trip).
//
// Fetch calls go through api-client.ts for consistent error handling.
// ---------------------------------------------------------------------------

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { patchAppSettings, loadSettingsAction } from "./actions/settings.js";

export type PermissionMode = "prompt" | "trust-list" | "trust-all";

export interface AppSettings {
  auto_update?: boolean;
  last_model?: string;
  notifications_enabled?: boolean;
  notify_agent_finished?: boolean;
  notify_permission?: boolean;
  permission_mode?: PermissionMode;
  trust_tools?: string[];
  agent_ignore_files?: string[];
  debug_logs?: boolean;
  /** Default Supervised-mode state for new chats. When true, new
   *  chats start with SupervisedMode=true and every file change
   *  the agent makes stages for user review before hitting disk.
   *  Per-chat toggle is on the chat prompt row (Supervised pill). */
  supervised_default?: boolean;
  shell_policy?: "no_commands" | "safe_commands" | "all_commands";
}

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<AppSettings> = {};
let patchSnapshot: Partial<AppSettings> = {};
let patchInputs: HTMLInputElement[] = [];
let patchGen = 0;
let patchResolvers: Array<(r: Record<string, unknown> | null) => void> = [];

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

export function patchSettings(patch: Partial<AppSettings>, ...inputs: HTMLInputElement[]): Promise<Record<string, unknown> | null> {
  // Filter out keys whose value matches the last-sent value. JSON
  // equality is good enough for the AppSettings shape (primitives +
  // arrays of strings); avoids reflecting no-op writes back to the
  // server and prevents the "Saving..." animation from firing on
  // bootstrap subscriptions like onSelectionChange's immediate fire.
  const changed: Partial<AppSettings> = {};
  for (const k of Object.keys(patch) as Array<keyof AppSettings>) {
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
    if (!patchInputs.includes(input)) patchInputs.push(input);
  }
  const p = new Promise<Record<string, unknown> | null>((resolve) => { patchResolvers.push(resolve); });
  if (patchTimer !== undefined) return p;
  showSaving();
  patchTimer = setTimeout(() => {
    patchTimer = undefined;
    const body = patchQueue;
    const allInputs = patchInputs;
    const resolvers = patchResolvers;
    const rollback = patchSnapshot;
    patchQueue = {};
    patchSnapshot = {};
    patchInputs = [];
    patchResolvers = [];
    const gen = ++patchGen;
    void patchAppSettings.dispatch(
      {
        body: body as Record<string, unknown>,
        ...(allInputs.length > 0 ? { inputs: allInputs } : {}),
      },
      { silent: true },
    ).then((r) => {
      try {
        if (r === null) Object.assign(lastSentPatch, rollback);
        if (gen === patchGen) {
          if (r === null) showError(); else showSaved();
        }
      } catch (e) {
        console.error("[persist] indicator callback threw", e);
      } finally {
        for (const resolve of resolvers) resolve(r as Record<string, unknown> | null);
      }
    });
  }, 300);
  return p;
}

export async function loadSettings(): Promise<AppSettings> {
  const s = await loadSettingsAction.dispatch(undefined);
  return (s as AppSettings) ?? {};
}
