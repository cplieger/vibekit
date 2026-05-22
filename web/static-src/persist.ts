// ---------------------------------------------------------------------------
// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g.
// theme toggle + notification toggle changes into one round-trip).
//
// Fetch calls go through api-client.ts for consistent error handling.
// ---------------------------------------------------------------------------

import { apiGet, apiPatch } from "./api-client.js";
import { showSaving, showSaved } from "./save-indicator.js";

export type PermissionMode = "prompt" | "trust-list" | "trust-all";

export interface AppSettings {
  auto_update?: boolean;
  last_model?: string;
  git_repo?: string;
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
}

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<AppSettings> = {};

export function patchSettings(patch: Partial<AppSettings>): void {
  Object.assign(patchQueue, patch);
  if (patchTimer !== undefined) return;
  showSaving();
  patchTimer = setTimeout(() => {
    patchTimer = undefined;
    const body = patchQueue;
    patchQueue = {};
    void apiPatch("/api/settings", body).then(() => showSaved());
  }, 300);
}

export async function loadSettings(): Promise<AppSettings> {
  const s = await apiGet<AppSettings>("/api/settings");
  return s ?? {};
}
