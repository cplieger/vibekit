// ---------------------------------------------------------------------------
// Per-device UI state: tab order, active view, shell open state,
// editor open files, file browser path, theme.
//
// Stored in localStorage, NOT synced to the server. Each device keeps its
// own view preferences; global preferences (notifications, last_model) live
// in /api/settings.
// ---------------------------------------------------------------------------

import { LS_UI_STATE_KEY } from "./ls-keys.js";

const LS_KEY = LS_UI_STATE_KEY;

export interface UIState {
  tab_order: string[];
  active_view: string;
  shell_open: boolean;
  /** User-dragged shell panel height in px; 0 = use the CSS default (16rem). */
  shell_h: number;
  editor_files: string[];
  fb_path: string;
  theme: "dark" | "light" | null;
  dismissed_banners: string[];
}

function empty(): UIState {
  return {
    tab_order: [],
    active_view: "",
    shell_open: false,
    shell_h: 0,
    editor_files: [],
    fb_path: "",
    theme: null,
    dismissed_banners: [],
  };
}

export function load(): UIState {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (raw === null) {
      return empty();
    }
    return sanitize(JSON.parse(raw));
  } catch {
    return empty();
  }
}

/** Validate the parsed localStorage value field by field instead of
 *  shallow-spreading it: stale or hand-edited data (a string where an
 *  array is expected, a non-finite shell height) previously flowed
 *  straight into tab restore and the shell sizing math. Any invalid
 *  field falls back to its default; valid siblings are kept. */
function sanitize(d: unknown): UIState {
  const e = empty();
  if (typeof d !== "object" || d === null) {
    return e;
  }
  const o = d as Record<string, unknown>;
  return {
    tab_order: strArray(o["tab_order"]) ?? e.tab_order,
    active_view: typeof o["active_view"] === "string" ? o["active_view"] : e.active_view,
    shell_open: typeof o["shell_open"] === "boolean" ? o["shell_open"] : e.shell_open,
    shell_h:
      typeof o["shell_h"] === "number" && Number.isFinite(o["shell_h"]) && o["shell_h"] >= 0
        ? o["shell_h"]
        : e.shell_h,
    editor_files: strArray(o["editor_files"]) ?? e.editor_files,
    fb_path: typeof o["fb_path"] === "string" ? o["fb_path"] : e.fb_path,
    theme: o["theme"] === "dark" || o["theme"] === "light" ? o["theme"] : null,
    dismissed_banners: strArray(o["dismissed_banners"]) ?? e.dismissed_banners,
  };
}

function strArray(v: unknown): string[] | undefined {
  return Array.isArray(v) && v.every((x) => typeof x === "string") ? v : undefined;
}

export function save(patch: Partial<UIState>): void {
  try {
    const current = load();
    const next = { ...current, ...patch };
    localStorage.setItem(LS_KEY, JSON.stringify(next));
  } catch {
    // ignore quota / disabled storage
  }
}
