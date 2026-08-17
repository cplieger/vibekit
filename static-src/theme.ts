// ---------------------------------------------------------------------------
// Theme toggle (light / dark / system). The preference is persisted per-device inside
// the `vibekit.ui-state` localStorage blob (managed by ui-state.ts) and applied
// to <html data-theme="…"> so the CSS in 01-tokens.css can key off it.
//
// The controller is @cplieger/ui-primitives' createTheme: it owns the
// resolve-and-apply lifecycle and the OS-preference follow, while vibekit
// supplies a storage adapter that keeps the preference inside its existing
// ui-state blob (read-modify-write of the `theme` field; sibling fields — tab
// order, active view, shell state — are left untouched).
//
// THREE states, and the third is not decoration: with a 2-state toggle the OS
// preference became unreachable the moment the user clicked once, because
// "follow the OS" was only ever the value of an UNSET field. The library models
// this properly (set("system"), cycle() as light -> dark -> system, an unset
// field resolving to "system"), so the fix is to use what it already has. The
// storage type had to carry "system" too, or the choice round-tripped to null.
//
// The button shows the CHOICE, not the resolved theme. Showing the resolved one
// would make Auto indistinguishable from whichever concrete theme it landed on,
// which is the whole point of having the third state visible.
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

type ThemeChoice = "dark" | "light" | "system";

// Persistence adapter: the theme preference lives in the `theme` field of the
// vibekit.ui-state blob, NOT a bare localStorage key. get() returns that field
// ("dark" | "light" | "system" | null); set() does a read-modify-write via
// uiState.save() so every sibling field is preserved. A null field (never
// chosen) reads as "unset" → createTheme treats it as "system" and resolves the
// OS preference, matching the pre-paint anti-FOUC snippet in index.html (which
// already accepted "system" before this module wrote one).
const uiStateThemeStorage: ThemeStorage = {
  get: () => uiState.load().theme,
  set: (value) => {
    uiState.save({ theme: value === "light" || value === "system" ? value : "dark" });
  },
};

let controller: ThemeController | null = null;

// The CHOICE currently reflected by the toggle icon. updateIcon() animates the
// transition FROM the icon on screen TO the icon for `current`, so `shown`
// tracks what the user can actually see; a 3-icon cycle cannot derive the
// outgoing icon from the incoming one the way a 2-icon flip could.
let current: ThemeChoice = "dark";
let shown: ThemeChoice | null = null;

/** Next state in the cycle, matching the library's own order. */
const NEXT: Record<ThemeChoice, ThemeChoice> = {
  light: "dark",
  dark: "system",
  system: "light",
};

const LABEL: Record<ThemeChoice, string> = {
  light: "light theme",
  dark: "dark theme",
  system: "system theme",
};

function updateIcon(): void {
  const btn = $.themeBtn;
  const icons: Record<ThemeChoice, Element | null> = {
    dark: btn.querySelector(".theme-icon-dark"),
    light: btn.querySelector(".theme-icon-light"),
    system: btn.querySelector(".theme-icon-system"),
  };
  const incoming = icons[current];
  if (incoming === null) {
    return;
  }
  const outgoing = shown === null || shown === current ? null : icons[shown];
  shown = current;

  // Subflux-inspired vertical slide: the outgoing icon slides down
  // ("setting"), then we swap visibility and the incoming icon slides
  // up from below ("rising"). A warm/cool glow on the button sells
  // the sun/moon metaphor; "system" borrows the cool one.
  const glowClass = current === "light" ? "glow-sun" : "glow-moon";

  let settled = false;
  const settle = (): void => {
    if (settled) {
      return;
    }
    settled = true;
    // Swap: hide every non-current icon, show incoming at the "rising" position.
    for (const [choice, icon] of Object.entries(icons)) {
      if (icon !== null && choice !== current) {
        icon.classList.add("hidden");
        icon.classList.remove("theme-setting");
      }
    }
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

  // Start: slide the outgoing icon down. On the first paint there is nothing on
  // screen to slide out, so settle immediately rather than animating from blank.
  if (outgoing === null) {
    settle();
  } else {
    outgoing.classList.add("theme-setting");
    outgoing.addEventListener("transitionend", settle, { once: true });
    // Safety timeout in case transitionend doesn't fire.
    setTimeout(settle, 350);
  }

  // The label names what a click DOES, and for "system" also what it resolved
  // to, because that is the one state whose appearance does not name itself.
  const suffix =
    current === "system" && controller !== null ? ` (now ${controller.resolved()})` : "";
  const label = `Theme: ${LABEL[current]}${suffix}. Switch to ${LABEL[NEXT[current]]}`;
  btn.setAttribute("data-tooltip", label);
  btn.setAttribute("aria-label", label);
}

/** Create the theme controller (applies the persisted / OS theme immediately)
 *  and wire up the toggle button. Call once during UI init. */
export function initThemeToggle(): void {
  controller = createTheme({
    storageKey: LS_UI_STATE_KEY,
    storage: uiStateThemeStorage,
    attribute: "data-theme",
    // The resolved theme drives the <html> attribute (the library's job); the
    // icon follows the CHOICE, read back from the controller.
    onChange: () => {
      current = controller?.get() ?? "system";
      updateIcon();
    },
  });
  // onChange fires during createTheme, before `controller` is assigned, so the
  // first icon paint happens here with the controller in hand.
  current = controller.get();
  updateIcon();

  $.themeBtn.addEventListener("click", () => {
    controller?.cycle();
  });
}
