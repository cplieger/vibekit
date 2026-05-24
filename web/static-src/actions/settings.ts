// ---------------------------------------------------------------------------
// Settings actions: user-initiated mutations on the settings page.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError } from "./index.js";
import { showSaved } from "../save-indicator.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

// --- Steering save ---

export const saveSteeringAction = apiAction<{ content: string }, unknown>({
  name: "settings.save_steering",
  request: ({ content }) => ({
    method: "PUT",
    path: "/api/steering",
    body: { content },
  }),
  success: () => { showSaved(); return ""; },
  error: "Couldn't save steering",
});

// --- Logout ---

export const logoutAction = defineAction<{ emailEl: HTMLElement; stAuthEl: HTMLElement }, unknown>({
  name: "settings.logout",
  optimistic: ({ emailEl, stAuthEl }) => {
    const prev = emailEl.textContent ?? "";
    emailEl.textContent = "";
    stAuthEl.textContent = "not signed in";
    return prev;
  },
  run: async (_args, signal) => {
    const r = await fetch("/api/logout", { method: "POST", signal: withTimeout(signal, API_TIMEOUT_MS) });
    if (!r.ok) {
      const body = await r.text().catch(() => "");
      let msg = `HTTP ${String(r.status)}`;
      try { const j = JSON.parse(body) as { error?: string }; if (j.error) msg = j.error; } catch { /* */ }
      throw new ActionError(msg, { status: r.status });
    }
    return {};
  },
  rollback: ({ emailEl, stAuthEl }, op) => {
    const prev = op as string;
    emailEl.textContent = prev;
    stAuthEl.textContent = prev !== "" ? "signed in" : "not signed in";
  },
  error: "Couldn't log out",
});

// --- Kiro settings toggle (experimental flags + compaction) ---

interface KiroSettingArgs {
  key: string;
  value: string;
  input: HTMLInputElement;
  isBool: boolean;
}

export const setKiroSettingAction = apiAction<KiroSettingArgs, unknown>({
  name: "settings.toggle_flag",
  request: ({ key, value }) => ({
    method: "PUT",
    path: "/api/kiro-settings",
    body: { key, value },
  }),
  optimistic: () => undefined,
  rollback: ({ input, isBool, value }) => {
    // Flip the toggle back to its previous state
    if (isBool || input.type === "checkbox") {
      input.checked = !input.checked;
    } else {
      // For text/number inputs, revert to opposite of what was sent
      input.value = value === "true" ? "false" : value === "false" ? "true" : "";
    }
  },
  success: () => { showSaved(); return ""; },
  error: "Couldn't save setting",
});

// --- Patch app settings (debug_logs, etc.) ---

export const patchAppSettingsAction = apiAction<{ body: Record<string, unknown>; input?: HTMLInputElement }, unknown>({
  name: "settings.patch",
  request: ({ body }) => ({
    method: "PATCH",
    path: "/api/settings",
    body,
  }),
  optimistic: () => undefined,
  rollback: ({ input }) => {
    if (input !== undefined) input.checked = !input.checked;
  },
  success: () => { showSaved(); return ""; },
  error: "Couldn't save setting",
});
