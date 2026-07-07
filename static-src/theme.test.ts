// @vitest-environment happy-dom
//
// Behavior of the theme controller wiring (theme.ts). The controller itself is
// @cplieger/ui-primitives' createTheme; these tests cover vibekit's contract on
// top of it: the theme lives in the `theme` field of the vibekit.ui-state blob
// (read-modify-write, siblings preserved — the no-migration invariant), the
// toggle stays strictly 2-state (dark <-> light, never "system"), and an unset
// field resolves the OS preference without persisting anything.
import { describe, it, expect, beforeEach, afterEach } from "vitest";

import { initThemeToggle } from "./theme.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

function setupButton(): HTMLButtonElement {
  document.body.innerHTML =
    '<button id="theme-btn">' +
    '<svg class="theme-icon-dark"></svg>' +
    '<svg class="theme-icon-light hidden"></svg>' +
    "</button>";
  return document.getElementById("theme-btn") as HTMLButtonElement;
}

function readBlob(): Record<string, unknown> {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
}

let originalMatchMedia: typeof window.matchMedia;

/** Force window.matchMedia so the OS preference is deterministic. */
function stubMatchMedia(osDark: boolean): void {
  window.matchMedia = ((query: string) => ({
    media: query,
    matches: query.includes("dark") ? osDark : !osDark,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    onchange: null,
    dispatchEvent: () => true,
  })) as unknown as typeof window.matchMedia;
}

beforeEach(() => {
  originalMatchMedia = window.matchMedia;
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

afterEach(() => {
  window.matchMedia = originalMatchMedia;
});

describe("initThemeToggle", () => {
  it("applies the stored theme field to <html data-theme> on init", () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ theme: "light", shell_open: true }));
    setupButton();
    initThemeToggle();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });

  it("toggle flips 2-state and persists into the blob, preserving sibling fields", () => {
    localStorage.setItem(
      LS_UI_STATE_KEY,
      JSON.stringify({ theme: "dark", shell_open: true, fb_path: "/x", tab_order: ["a", "b"] }),
    );
    const btn = setupButton();
    initThemeToggle();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");

    btn.click(); // dark -> light
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    let blob = readBlob();
    expect(blob["theme"]).toBe("light");
    expect(blob["shell_open"]).toBe(true);
    expect(blob["fb_path"]).toBe("/x");
    expect(blob["tab_order"]).toEqual(["a", "b"]);

    btn.click(); // light -> dark (never "system")
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    blob = readBlob();
    expect(blob["theme"]).toBe("dark");
    expect(blob["shell_open"]).toBe(true);
  });

  it("resolves the OS preference when the theme field is absent, without persisting it", () => {
    stubMatchMedia(true); // OS = dark
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ shell_open: true }));
    setupButton();
    initThemeToggle();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    // Unset preference stays "system" internally — nothing is written to the blob.
    const blob = readBlob();
    expect(blob["theme"]).toBeUndefined();
    expect(blob["shell_open"]).toBe(true);
  });

  it("first toggle from an unset (OS-followed) state pins a concrete theme", () => {
    stubMatchMedia(false); // OS = light
    // No blob at all.
    const btn = setupButton();
    initThemeToggle();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(readBlob()["theme"]).toBeUndefined();

    btn.click(); // resolved light -> dark, now pinned + persisted
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(readBlob()["theme"]).toBe("dark");
  });
});
