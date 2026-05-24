// ---------------------------------------------------------------------------
// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g.
// theme toggle + notification toggle changes into one round-trip).
//
// Fetch calls go through api-client.ts for consistent error handling.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { showSaving, showSaved, showError } from "./save-indicator.js";
import { patchAppSettingsAction } from "./actions/settings.js";

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
}

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<AppSettings> = {};
let patchInputs: HTMLInputElement[] = [];

export function patchSettings(patch: Partial<AppSettings>, input?: HTMLInputElement): void {
  Object.assign(patchQueue, patch);
  // Collect all inputs that contributed to this patch so the rollback
  // can flip every toggle back on failure (multi-key patches like
  // notifications_enabled + notify_agent_finished + notify_permission).
  if (input !== undefined && !patchInputs.includes(input)) {
    patchInputs.push(input);
  }
  if (patchTimer !== undefined) return;
  patchTimer = setTimeout(() => {
    patchTimer = undefined;
    const body = patchQueue;
    const inputs = patchInputs;
    patchQueue = {};
    patchInputs = [];
    showSaving();  // moved into the dispatch tick so it doesn't orphan
    void patchAppSettingsAction.dispatch(
      {
        body: body as Record<string, unknown>,
        ...(inputs.length > 0 ? { inputs } : {}),
      },
      { silent: true },
    ).then((r) => { if (r === null) showError(); else showSaved(); });
  }, 300);
}

export async function loadSettings(): Promise<AppSettings> {
  const s = await apiGet<AppSettings>("/api/settings");
  return s ?? {};
}
