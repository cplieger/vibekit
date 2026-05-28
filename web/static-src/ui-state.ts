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
  editor_files: string[];
  fb_path: string;
  theme: "dark" | "light" | null;
  merge_method: Record<string, string>;
  merge_delete_branch: Record<string, boolean>;
  dismissed_banners: string[];
}

function empty(): UIState {
  return {
    tab_order: [],
    active_view: "",
    shell_open: false,
    editor_files: [],
    fb_path: "",
    theme: null,
    merge_method: {},
    merge_delete_branch: {},
    dismissed_banners: [],
  };
}

export function load(): UIState {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (raw === null) {
      return empty();
    }
    const d = JSON.parse(raw) as Partial<UIState>;
    const e = empty();
    return { ...e, ...d };
  } catch {
    return empty();
  }
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
