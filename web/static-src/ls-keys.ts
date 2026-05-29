// Single source of truth for localStorage keys used across modules.
// theme-init.ts cannot import this (it's a non-module blocking script),
// but references the same value via inline construction — see its comment.
export const LS_UI_STATE_KEY = "vibekit.ui-state";
