// ---------------------------------------------------------------------------
// Settings actions: user-initiated mutations on the settings page.
//
// Note on save-indicator wiring: this module does NOT call showSaved()/
// showError() from the action's `success`/`error` fields, because every
// callsite dispatches with { silent: true } (the indicator IS the
// feedback, not a toast) and the action framework's success-toast
// branch short-circuits on silent. Callsites pair showSaving() before
// dispatch with showSaved()/showError() based on the dispatch result.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError } from "./index.js";
import { withTimeout, API_TIMEOUT_MS } from "../api-client.js";

// --- Steering save ---

export const saveSteeringAction = apiAction<{ content: string }, unknown>({
  name: "settings.save_steering",
  request: ({ content }) => ({
    method: "PUT",
    path: "/api/steering",
    body: { content },
  }),
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
  // run() intentionally ignores args — DOM refs are only for optimistic/rollback.
  // The framework passes the full args object; we destructure to nothing.
  run: async (_, signal) => {
    const r = await fetch("/api/logout", { method: "POST", signal: withTimeout(signal, API_TIMEOUT_MS) });
    if (!r.ok) {
      const body = await r.text().catch(() => "");
      let msg = `HTTP ${String(r.status)}`;
      try { const j = JSON.parse(body) as { error?: string }; if (j.error !== undefined && j.error !== "") msg = j.error; } catch { /* */ }
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
  /** For non-checkbox inputs, the value captured BEFORE the change event
   *  fired (via a focus-time snapshot). This ensures rollback restores
   *  the true previous value, not the rejected one. */
  previousValue?: string;
}

interface KiroSettingOp {
  prevChecked?: boolean;
  prevValue?: string;
}

export const setKiroSettingAction = apiAction<KiroSettingArgs, unknown>({
  name: "settings.set_kiro_setting",
  request: ({ key, value }) => ({
    method: "PUT",
    path: "/api/kiro-settings",
    body: { key, value },
  }),
  optimistic: ({ input, previousValue }): KiroSettingOp => {
    if (input.type === "checkbox") {
      return { prevChecked: !input.checked }; // user just toggled, so prev is opposite
    }
    // Use the focus-time snapshot if available; otherwise fall back to current value
    return { prevValue: previousValue ?? input.value };
  },
  rollback: ({ input }, op) => {
    const o = op as KiroSettingOp | undefined;
    if (o === undefined) return;
    if (o.prevChecked !== undefined) {
      input.checked = o.prevChecked;
    } else if (o.prevValue !== undefined) {
      input.value = o.prevValue;
    }
  },
  error: "Couldn't save setting",
});

// --- Patch app settings (debug_logs, etc.) ---

interface PatchAppArgs {
  body: Record<string, unknown>;
  /** Optional input(s) for rollback. Multi-key patches can pass an
   *  array of inputs all of whose checked-state should flip back on
   *  failure. */
  inputs?: readonly HTMLInputElement[];
}

interface PatchAppOp {
  inputs: { el: HTMLInputElement; prevChecked: boolean; prevValue: string }[];
}

export const patchAppSettingsAction = apiAction<PatchAppArgs, unknown>({
  name: "settings.patch",
  request: ({ body }) => ({
    method: "PATCH",
    path: "/api/settings",
    body,
  }),
  optimistic: ({ inputs }): PatchAppOp => {
    const list = inputs ?? [];
    return {
      inputs: list.map((el) => ({
        el,
        prevChecked: !el.checked, // user just changed, so prev is opposite of current
        prevValue: el.value,
      })),
    };
  },
  rollback: (_args, op) => {
    const o = op as PatchAppOp | undefined;
    if (o === undefined) return;
    for (const { el, prevChecked, prevValue } of o.inputs) {
      if (el.type === "checkbox") {
        el.checked = prevChecked;
      } else {
        el.value = prevValue;
      }
    }
  },
  error: "Couldn't save setting",
});
