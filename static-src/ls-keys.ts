// Single source of truth for localStorage keys used across modules.
// The anti-FOUC theme-init script inlined in static/index.html can't import
// this (it runs before modules load), so it hardcodes the same literal — kept
// in sync by theme-init-snippet.test.ts.
export const LS_UI_STATE_KEY = "vibekit.ui-state";
