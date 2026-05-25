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

import { apiAction } from "./index.js";
import { RETRY_STANDARD } from "./types.js";

// --- Steering save ---

export const saveSteering = apiAction<{ content: string }, unknown>({
  name: "settings.save_steering",
  retryable: "network",
  retry: RETRY_STANDARD,
  scope: "settings",
  request: ({ content }) => ({
    method: "PUT",
    path: "/api/steering",
    body: { content },
  }),
  error: "Couldn't save steering",
});

// --- Logout ---

export const logout = apiAction<{ emailEl: HTMLElement; stAuthEl: HTMLElement }, unknown, string>({
  name: "settings.logout",
  retryable: "network",
  request: () => ({ method: "POST", path: "/api/logout" }),
  optimistic: ({ emailEl, stAuthEl }) => {
    const prev = emailEl.textContent ?? "";
    emailEl.textContent = "";
    stAuthEl.textContent = "not signed in";
    return prev;
  },
  rollback: ({ emailEl, stAuthEl }, op) => {
    if (op === undefined) return;
    emailEl.textContent = op;
    stAuthEl.textContent = op !== "" ? "signed in" : "not signed in";
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

export const setKiroSetting = apiAction<KiroSettingArgs, unknown, KiroSettingOp>({
  name: "settings.set_kiro_setting",
  scope: "settings",
  // Not retryable: args contain DOM refs that become stale on retry, and
  // settings saves are user-initiated (cheap to redo manually).
  request: ({ key, value }) => ({
    method: "PUT",
    path: "/api/kiro-settings",
    body: { key, value },
  }),
  optimistic: ({ input, previousValue }) => {
    if (input.type === "checkbox") {
      return { prevChecked: !input.checked }; // user just toggled, so prev is opposite
    }
    // Use the focus-time snapshot if available; otherwise fall back to
    // defaultValue (the original HTML attribute value) — input.value would
    // be the NEW value since the change event already updated it.
    return { prevValue: previousValue ?? input.defaultValue };
  },
  rollback: ({ input }, op) => {
    if (op === undefined) return;
    if (op.prevChecked !== undefined) {
      input.checked = op.prevChecked;
    } else if (op.prevValue !== undefined) {
      input.value = op.prevValue;
    }
  },
  error: "Couldn't save setting",
});

// --- Load settings (deduped fetch for SSE-triggered reconcile) ---

export const loadSettings = apiAction<void, Record<string, unknown>>({
  name: "settings.load",
  dedupe: true,
  request: () => ({ method: "GET", path: "/api/settings" }),
  error: false,
  success: false,
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

export const patchAppSettings = apiAction<PatchAppArgs, unknown, PatchAppOp>({
  name: "settings.patch",
  scope: "settings",
  // Not retryable: args contain DOM refs that become stale on retry, and
  // settings saves are user-initiated (cheap to redo manually).
  request: ({ body }) => ({
    method: "PATCH",
    path: "/api/settings",
    body,
  }),
  optimistic: ({ inputs }) => {
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
    if (op === undefined) return;
    for (const { el, prevChecked, prevValue } of op.inputs) {
      if (el.type === "checkbox") {
        el.checked = prevChecked;
      } else {
        el.value = prevValue;
      }
    }
  },
  error: "Couldn't save setting",
});
