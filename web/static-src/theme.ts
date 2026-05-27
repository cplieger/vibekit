// ---------------------------------------------------------------------------
// Theme toggle (dark / light). Persisted per-device in localStorage via
// ui-state.ts.
// ---------------------------------------------------------------------------

import * as uiState from "./ui-state.js";
import { $ } from "./dom.js";

export type ThemeChoice = "dark" | "light";

let current: ThemeChoice = "dark";

export function applyTheme(choice: ThemeChoice): void {
  current = choice;
  document.documentElement.dataset["theme"] = choice;
  updateIcon();
}

export function getSystemTheme(): ThemeChoice {
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function toggleTheme(): void {
  const next: ThemeChoice = current === "dark" ? "light" : "dark";
  applyTheme(next);
  uiState.save({ theme: next });
}

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
    void incoming.offsetHeight;
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

/** Wire up the theme toggle button. Call once during UI init. */
export function initThemeToggle(): void {
  $.themeBtn.addEventListener("click", toggleTheme);
}
