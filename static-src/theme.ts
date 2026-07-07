// ---------------------------------------------------------------------------
// Theme toggle (dark / light). The preference is persisted per-device inside
// the `vibekit.ui-state` localStorage blob (managed by ui-state.ts) and applied
// to <html data-theme="…"> so the CSS in 01-tokens.css can key off it.
//
// The controller is @cplieger/ui-primitives' createTheme: it owns the
// resolve-and-apply lifecycle and the OS-preference follow, while vibekit
// supplies a storage adapter that keeps the preference inside its existing
// ui-state blob (read-modify-write of the `theme` field; sibling fields — tab
// order, active view, shell state — are left untouched). The UX stays strictly
// 2-state (dark / light): the toggle flips between the two concrete themes and
// never selects "system". An unset field reads as "system", so first paint
// falls back to the OS preference exactly as before.
//
// requires @cplieger/ui-primitives >= 2.1.0 (createTheme storage adapter +
// themeInitSnippetFromJSON); verified locally via node_modules overlay until
// released.
// ---------------------------------------------------------------------------

import * as uiState from "./ui-state.js";
import { $ } from "./dom.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { createTheme } from "@cplieger/ui-primitives/theme";
import type { ThemeController, ThemeStorage } from "@cplieger/ui-primitives/theme";

type ThemeChoice = "dark" | "light";

// Persistence adapter: the theme preference lives in the `theme` field of the
// vibekit.ui-state blob, NOT a bare localStorage key. get() returns that field
// ("dark" | "light" | null); set() does a read-modify-write via uiState.save()
// so every sibling field is preserved. A null field (never chosen) reads as
// "unset" → createTheme treats it as "system" and resolves the OS preference,
// matching the pre-paint anti-FOUC snippet in index.html. Because the toggle is
// 2-state, set() only ever receives "dark" / "light".
const uiStateThemeStorage: ThemeStorage = {
  get: () => uiState.load().theme,
  set: (value) => {
    uiState.save({ theme: value === "light" ? "light" : "dark" });
  },
};

let controller: ThemeController | null = null;

// The concrete theme currently reflected by the toggle icon. updateIcon()
// animates the transition FROM the icon on screen TO the icon for `current`, so
// it is set to the newly-resolved value immediately before each call (via the
// controller's onChange).
let current: ThemeChoice = "dark";

function updateIcon(): void {
  const btn = $.themeBtn;
  const dark = btn.querySelector(".theme-icon-dark");
  const light = btn.querySelector(".theme-icon-light");
  if (dark === null || light === null) {
    return;
  }

  // Subflux-inspired vertical slide: the outgoing icon slides down
  // ("setting"), then we swap visibility and the incoming icon slides
  // up from below ("rising"). A warm/cool glow on the button sells
  // the sun/moon metaphor.
  const outgoing = current === "dark" ? light : dark;
  const incoming = current === "dark" ? dark : light;
  const glowClass = current === "dark" ? "glow-moon" : "glow-sun";

  let settled = false;
  const settle = (): void => {
    if (settled) {
      return;
    }
    settled = true;
    // Swap: hide outgoing, show incoming at the "rising" position.
    outgoing.classList.add("hidden");
    outgoing.classList.remove("theme-setting");
    incoming.classList.remove("hidden");
    incoming.classList.add("theme-rising");
    btn.classList.add(glowClass);
    // Force reflow so the browser sees the "rising" start state.
    void (incoming as HTMLElement).offsetHeight;
    // Remove "rising" so the transition runs from below → center.
    incoming.classList.remove("theme-rising");
    incoming.addEventListener(
      "transitionend",
      () => {
        btn.classList.remove("glow-sun", "glow-moon");
      },
      { once: true },
    );
    // Safety: clear glow if transitionend doesn't fire (e.g. reduced motion).
    setTimeout(() => {
      btn.classList.remove("glow-sun", "glow-moon");
    }, 350);
  };

  // Start: slide the outgoing icon down.
  outgoing.classList.add("theme-setting");
  outgoing.addEventListener("transitionend", settle, { once: true });
  // Safety timeout in case transitionend doesn't fire.
  setTimeout(settle, 350);

  btn.setAttribute(
    "data-tooltip",
    current === "dark" ? "Switch to light theme" : "Switch to dark theme",
  );
  btn.setAttribute(
    "aria-label",
    current === "dark" ? "Switch to light theme" : "Switch to dark theme",
  );
}

/** Create the theme controller (applies the persisted / OS theme immediately)
 *  and wire up the toggle button. Call once during UI init. */
export function initThemeToggle(): void {
  controller = createTheme({
    storageKey: LS_UI_STATE_KEY,
    storage: uiStateThemeStorage,
    attribute: "data-theme",
    onChange: (resolved) => {
      current = resolved;
      updateIcon();
    },
  });

  $.themeBtn.addEventListener("click", () => {
    if (controller === null) {
      return;
    }
    // Strictly 2-state: flip between the two concrete themes; never "system".
    controller.set(controller.resolved() === "dark" ? "light" : "dark");
  });
}
