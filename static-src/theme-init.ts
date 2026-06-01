// ---------------------------------------------------------------------------
// Boot-time theme application. Sets [data-theme] on <html> SYNCHRONOUSLY
// before stylesheets parse, to prevent flash-of-wrong-theme on the splash
// screen. Loaded as a classic blocking <script src="/theme-init.js"> in
// <head> — NOT type="module" (modules are deferred and would defeat the
// purpose).
//
// Compiled to static/theme-init.js by the main tsconfig. tsgo emits a
// non-module script (with "use strict" and no module wrapper) when the
// file has no top-level imports or exports — same mechanism sw.ts uses.
//
// Reads the same per-device state that ui-state.ts writes:
//   key:    defined in ls-keys.ts as LS_UI_STATE_KEY
//   field:  theme  ("dark" | "light" | null)
//
// Cannot import ls-keys.ts (non-module blocking script). The key value
// is constructed inline to match ls-keys.ts's LS_UI_STATE_KEY.
// theme.ts's applyTheme() re-sets the same attribute during restoreAll(),
// so this file is a paint-time hint — but the two must agree to avoid a
// re-flip.
//
// Covered by CSP `script-src 'self'`; no hash maintenance required.
// ---------------------------------------------------------------------------

(function () {
  try {
    // Key must match LS_UI_STATE_KEY in ls-keys.ts
    const lsKey = ["vibekit", "ui-state"].join(".");
    const raw = localStorage.getItem(lsKey);
    let stored: "dark" | "light" | null = null;
    if (raw !== null) {
      const parsed = JSON.parse(raw) as { theme?: string };
      if (parsed.theme === "dark" || parsed.theme === "light") {
        stored = parsed.theme;
      }
    }
    const theme: "dark" | "light" =
      stored ?? (window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");
    document.documentElement.dataset["theme"] = theme;
  } catch {
    document.documentElement.dataset["theme"] = "dark";
  }
})();
