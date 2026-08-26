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

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";

// --- Steering save ---

export const saveSteering = apiAction<{ content: string }>({
  name: "settings.save_steering",
  retryable: retryNetwork,
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
  retryable: retryNetwork,
  request: () => ({ method: "POST", path: "/api/logout" }),
  optimistic: ({ emailEl, stAuthEl }) => {
    const prev = emailEl.textContent;
    emailEl.textContent = "";
    stAuthEl.textContent = "not signed in";
    return prev;
  },
  rollback: ({ emailEl, stAuthEl }, op) => {
    if (op === undefined) {
      return;
    }
    emailEl.textContent = op;
    stAuthEl.textContent = op !== "" ? "signed in" : "not signed in";
  },
  error: "Couldn't log out",
});

// --- Kiro settings toggle (the experimental flags) ---

interface KiroSettingArgs {
  key: string;
  value: string;
  input: HTMLInputElement;
}

interface KiroSettingOp {
  prevChecked: boolean;
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
  // Checkbox-only, because every kiro-cli setting vibekit still exposes is one.
  // The non-checkbox arm this used to carry took a focus-time snapshot so a
  // rollback restored the true previous value rather than the rejected one, and
  // its only two inputs were the compaction number fields — removed once their
  // ACP counterparts measured as having no reader upstream. Bring the snapshot
  // back WITH the next number-valued setting, not before: `input.value` is
  // already the new value by the time a change event fires, so a number field
  // rolled back without one keeps the value the server refused.
  optimistic: ({ input }) => {
    return { prevChecked: !input.checked }; // user just toggled, so prev is opposite
  },
  rollback: ({ input }, op) => {
    if (op === undefined) {
      return;
    }
    input.checked = op.prevChecked;
  },
  error: "Couldn't save setting",
});

// --- Load settings (deduped fetch for SSE-triggered reconcile) ---

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const loadSettings = apiAction<void, Record<string, unknown>>({
  name: "settings.load",
  dedupe: true,
  retryable: retryNetwork,
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
    if (op === undefined) {
      return;
    }
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
