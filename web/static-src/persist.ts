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
  shell_policy?: "no_commands" | "safe_commands" | "all_commands";
}

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<AppSettings> = {};
let patchInputs: HTMLInputElement[] = [];
let patchGen = 0;
let patchResolvers: Array<(r: Record<string, unknown> | null) => void> = [];

export function patchSettings(patch: Partial<AppSettings>, ...inputs: HTMLInputElement[]): Promise<Record<string, unknown> | null> {
  Object.assign(patchQueue, patch);
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
    patchQueue = {};
    patchInputs = [];
    patchResolvers = [];
    const gen = ++patchGen;
    void patchAppSettingsAction.dispatch(
      {
        body: body as Record<string, unknown>,
        ...(allInputs.length > 0 ? { inputs: allInputs } : {}),
      },
      { silent: true },
    ).then((r) => {
      if (gen === patchGen) {
        if (r === null) showError(); else showSaved();
      }
      for (const resolve of resolvers) resolve(r as Record<string, unknown> | null);
    });
  }, 300);
  return p;
}

export async function loadSettings(): Promise<AppSettings> {
  const s = await apiGet<AppSettings>("/api/settings");
  return s ?? {};
}
