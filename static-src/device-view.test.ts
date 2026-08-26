// ---------------------------------------------------------------------------
// device-view.ts owns the four localStorage fields that are not the workspace's:
// the active tab, the two shell fields, and the theme's pre-paint cache.
//
// The property worth pinning hardest is the ONE-OWNER rule, and it is here
// because its violation already shipped once. All four live in a single JSON blob
// under one key, so every write is a read-modify-write — and the module this
// replaced had TWO of them, a `writeLocal` for the device fields beside a
// `cacheTheme` for the theme. That only worked because `writeLocal` remembered to
// re-read the theme and re-attach it by hand; forget that line and choosing a
// theme, then resizing the shell, silently clears the theme the pre-paint snippet
// is about to read.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";

import {
  activeView,
  cacheTheme,
  cachedTheme,
  loadDeviceView,
  setActiveView,
  setShellHeight,
  setShellOpen,
  shellHeight,
  shellOpen,
} from "./device-view.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

function blob(): Record<string, unknown> {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
}

beforeEach(() => {
  localStorage.clear();
});

describe("the three fields this screen keeps to itself", () => {
  it("reads back what it wrote, per field", () => {
    setActiveView("chat-x");
    setShellOpen(true);
    setShellHeight(320);

    expect(activeView()).toBe("chat-x");
    expect(shellOpen()).toBe(true);
    expect(shellHeight()).toBe(320);
  });

  it("answers the defaults when nothing has been written", () => {
    expect(loadDeviceView()).toEqual({ active_view: "", shell_open: false, shell_h: 0 });
  });

  it("keeps a valid field when a sibling is the wrong type", () => {
    // A hand-edited blob, or one written by an older build. `shell_h` feeds the
    // panel's sizing arithmetic directly, so a string there is not a cosmetic
    // problem — and dropping the whole record over one bad field would reset the
    // two that are fine.
    localStorage.setItem(
      LS_UI_STATE_KEY,
      JSON.stringify({ active_view: "chat-y", shell_open: "yes", shell_h: "tall" }),
    );

    expect(loadDeviceView()).toEqual({ active_view: "chat-y", shell_open: false, shell_h: 0 });
  });

  it("refuses a negative or non-finite height", () => {
    for (const bad of [-1, Number.NaN, Number.POSITIVE_INFINITY]) {
      localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ shell_h: bad }));
      expect(shellHeight()).toBe(0);
    }
  });

  it("survives a blob that is not an object, and one that is not JSON at all", () => {
    localStorage.setItem(LS_UI_STATE_KEY, '"a string"');
    expect(loadDeviceView()).toEqual({ active_view: "", shell_open: false, shell_h: 0 });

    localStorage.setItem(LS_UI_STATE_KEY, "{not json");
    expect(loadDeviceView()).toEqual({ active_view: "", shell_open: false, shell_h: 0 });
  });
});

describe("the theme's pre-paint cache", () => {
  it("round-trips the three choices, and 'system' is one of them", () => {
    // "system" is a real stored CHOICE — the user asked to follow the OS — not the
    // absence of one. Coercing it away is what once made Auto unreachable after a
    // single toggle click.
    for (const choice of ["dark", "light", "system"] as const) {
      cacheTheme(choice);
      expect(cachedTheme()).toBe(choice);
    }
  });

  it("reads an unrecognised or absent value as no choice", () => {
    expect(cachedTheme()).toBeNull();
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ theme: "chartreuse" }));
    expect(cachedTheme()).toBeNull();
  });

  it("clearing removes the field, so the snippet falls back to the OS preference", () => {
    cacheTheme("light");
    cacheTheme(null);
    expect("theme" in blob()).toBe(false);
    expect(cachedTheme()).toBeNull();
  });
});

// The rule, from both sides. This is the module's reason to exist.
describe("one owner of the key", () => {
  it("a device-field write preserves the theme cache", () => {
    cacheTheme("light");
    setShellHeight(420);
    setActiveView("chat-z");
    setShellOpen(true);

    expect(cachedTheme()).toBe("light");
  });

  it("a theme write preserves the three device fields", () => {
    setActiveView("chat-z");
    setShellOpen(true);
    setShellHeight(420);
    cacheTheme("dark");

    expect(loadDeviceView()).toEqual({
      active_view: "chat-z",
      shell_open: true,
      shell_h: 420,
    });
  });

  it("leaves a field it does not own alone", () => {
    // Nothing else writes this key today, and the read-modify-write is what makes
    // that safe to stay true: a future field added by another module is preserved
    // rather than dropped on the next shell drag.
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ someone_elses: "value" }));
    setShellHeight(200);
    cacheTheme("dark");

    expect(blob()["someone_elses"]).toBe("value");
    expect(blob()["shell_h"]).toBe(200);
    expect(blob()["theme"]).toBe("dark");
  });
});
