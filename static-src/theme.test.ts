//
// Behavior of the theme controller wiring (theme.ts). The controller itself is
// @cplieger/ui-primitives' createTheme; these tests cover vibekit's contract on
// top of it: the toggle CYCLES three states (light -> dark -> system), an unset
// value resolves the OS preference without persisting anything, and the label
// names what "system" resolved to.
//
// The storage adapter is a PARAMETER now (settings.ts supplies the real one, and
// the authority is a config.json key), so these cases hand it the paint-cache
// adapter — device-view.ts's own read-modify-write of the `theme` field. That is
// deliberate rather than convenient: the cache is what the pre-paint snippet
// reads, so the blob assertions below still pin the one storage behaviour a
// reader would notice breaking, siblings preserved and all.
//
// The third state is the point: with a 2-state toggle, "follow the OS" was only
// ever the value of an unset field, so one click made it unreachable without
// clearing localStorage.
import { describe, it, expect, beforeEach, afterEach } from "vitest";

import { initThemeToggle } from "./theme.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { cacheTheme, cachedTheme } from "./device-view.js";
import type { ThemeChoice } from "./device-view.js";

/** The paint-cache adapter, which is the half of settings.ts's real adapter that
 *  touches storage. */
const cacheStorage = {
  get: (): string | null => cachedTheme(),
  set: (value: string): void => {
    cacheTheme(value === "light" || value === "system" ? (value as ThemeChoice) : "dark");
  },
};

function setupButton(): HTMLButtonElement {
  document.body.innerHTML =
    '<button id="theme-btn">' +
    '<svg class="theme-icon-dark"></svg>' +
    '<svg class="theme-icon-light hidden"></svg>' +
    '<svg class="theme-icon-system hidden"></svg>' +
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
    initThemeToggle(cacheStorage);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });

  it("cycles dark -> system -> light and persists each into the blob, preserving siblings", () => {
    stubMatchMedia(true); // OS = dark, so "system" resolves dark
    localStorage.setItem(
      LS_UI_STATE_KEY,
      JSON.stringify({ theme: "dark", shell_open: true, fb_path: "/x", tab_order: ["a", "b"] }),
    );
    const btn = setupButton();
    initThemeToggle(cacheStorage);
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");

    btn.click(); // dark -> system
    let blob = readBlob();
    expect(blob["theme"]).toBe("system");
    // Siblings survive the read-modify-write, which is the no-migration invariant.
    expect(blob["shell_open"]).toBe(true);
    expect(blob["fb_path"]).toBe("/x");
    expect(blob["tab_order"]).toEqual(["a", "b"]);

    btn.click(); // system -> light
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    blob = readBlob();
    expect(blob["theme"]).toBe("light");
    expect(blob["shell_open"]).toBe(true);
  });

  // The regression this group fixed: "system" must survive a reload. It used to
  // round-trip to null through ui-state's coercion, which put the user back in
  // the two concrete states with no way to ask for the OS preference again.
  it("a stored system choice survives a reload and still follows the OS", () => {
    stubMatchMedia(true); // OS = dark
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ theme: "system" }));
    setupButton();
    initThemeToggle(cacheStorage);

    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    // Still stored as the choice, not flattened to the theme it resolved to.
    expect(readBlob()["theme"]).toBe("system");
  });

  // The label is the only thing that distinguishes "system" from the concrete
  // theme it landed on, since the resolved appearance is identical.
  it("names the resolved theme while following the OS", () => {
    stubMatchMedia(false); // OS = light
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ theme: "system" }));
    const btn = setupButton();
    initThemeToggle(cacheStorage);

    expect(btn.getAttribute("aria-label")).toContain("now light");
  });

  it("resolves the OS preference when the theme field is absent, without persisting it", () => {
    stubMatchMedia(true); // OS = dark
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ shell_open: true }));
    setupButton();
    initThemeToggle(cacheStorage);
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    // Unset preference stays "system" internally — nothing is written to the blob.
    const blob = readBlob();
    expect(blob["theme"]).toBeUndefined();
    expect(blob["shell_open"]).toBe(true);
  });

  it("first toggle from an unset state pins a concrete theme without changing appearance", () => {
    stubMatchMedia(false); // OS = light
    // No blob at all, so the choice reads as "system".
    const btn = setupButton();
    initThemeToggle(cacheStorage);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(readBlob()["theme"]).toBeUndefined();

    // system -> light: the same appearance, now a stored choice rather than a
    // followed one. That is the cycle's order, not a no-op.
    btn.click();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(readBlob()["theme"]).toBe("light");

    btn.click(); // light -> dark
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(readBlob()["theme"]).toBe("dark");
  });
});
