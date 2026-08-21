// @vitest-environment happy-dom
//
// The keydown handler itself: which keys reach the shortcut table, which ones
// belong to the field they were typed in, and what Escape does.
//
// shortcuts.test.ts already covers the REGISTRY (that the reference sheet is
// generated from it) and the bare `?`. This file covers the handler's gates,
// which are where the module's decisions actually live:
//
//  1. THE MODIFIER GATE. Every row in the table requires Ctrl or Cmd, so a bare
//     printable key must return before the loop is reached. Without that, `k`
//     typed anywhere on the page would open a new chat.
//  2. THE SHIFT MATCH. It is two-sided on purpose: a chord that wants Shift
//     needs it, and a chord that does not want it must not fire when it is held,
//     or Ctrl+Shift+K would silently mean Ctrl+K.
//  3. THE TEXT-ENTRY CARVE-OUTS. Two chords survive a focused field and the
//     rest do not — Ctrl+Enter (send) and Ctrl+K / Ctrl+N (new chat). Everything
//     else belongs to the field, and taking it would swallow the browser's own
//     binding while the user is typing.
//  4. ESCAPE. It closes the topmost modal via the shared helper and stops there;
//     only with nothing to close does it become the file browser's deselect.
//     Doing both would deselect behind a dialog the user was only dismissing.
//
// keys.ts registers its document listener ONCE per module instance
// (the `initialized` guard), so this file initialises it once in beforeAll and
// clears the recording stubs per test — the same discipline shortcuts.test.ts
// documents, and for the same reason.

import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";

const { closeTop } = vi.hoisted(() => ({ closeTop: vi.fn(() => false) }));
vi.mock("./modals.js", () => ({ closeTopModal: () => closeTop() }));

import { initKeyboardShortcuts, registeredShortcuts } from "./keys.js";
import { onBus, BUS_KEYS_ESCAPE } from "./bus.js";

const actions = {
  newChat: vi.fn(),
  toggleShell: vi.fn(),
  toggleFiles: vi.fn(),
  toggleGit: vi.fn(),
  toggleSettings: vi.fn(),
  sendMessage: vi.fn(),
  showShortcuts: vi.fn(),
};

function press(key: string, target?: Element, extra: KeyboardEventInit = {}): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...extra });
  (target ?? document.body).dispatchEvent(e);
  return e;
}

function composer(): HTMLElement {
  return document.getElementById("prompt-input") as HTMLElement;
}

beforeAll(() => {
  initKeyboardShortcuts(actions);
});

beforeEach(() => {
  closeTop.mockReturnValue(false);
  for (const fn of Object.values(actions)) {
    fn.mockClear();
  }
  document.body.innerHTML = `
    <textarea id="prompt-input"></textarea>
    <input id="a-text-field" type="text">
    <svg id="an-icon"></svg>`;
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("Escape", () => {
  it("closes the topmost modal and goes no further", () => {
    closeTop.mockReturnValue(true);
    let deselects = 0;
    const off = onBus(BUS_KEYS_ESCAPE, () => {
      deselects++;
    });
    try {
      const e = press("Escape");
      // The key was consumed by the dialog, so the page must not also see it.
      expect(e.defaultPrevented).toBe(true);
      // Deselecting behind a dialog the user was only dismissing loses a
      // selection they never touched.
      expect(deselects).toBe(0);
    } finally {
      off();
    }
  });

  it("clears the file browser selection when there is no modal to close", () => {
    closeTop.mockReturnValue(false);
    let deselects = 0;
    const off = onBus(BUS_KEYS_ESCAPE, () => {
      deselects++;
    });
    try {
      const e = press("Escape");
      expect(deselects).toBe(1);
      // Nothing was consumed, so Escape keeps whatever meaning the page has for
      // it (leaving fullscreen, cancelling an IME composition).
      expect(e.defaultPrevented).toBe(false);
    } finally {
      off();
    }
  });
});

describe("the modifier gate", () => {
  it("ignores a registered key pressed with no modifier at all", () => {
    // Every table row requires Ctrl or Cmd, so the gate returns before the loop.
    // Without it, typing `k` in the page body would open a new chat.
    press("k");
    press("/");
    press(",");
    expect(actions.newChat).not.toHaveBeenCalled();
    expect(actions.toggleShell).not.toHaveBeenCalled();
    expect(actions.toggleSettings).not.toHaveBeenCalled();
  });

  it("prevents the default of a chord it handled", () => {
    // Ctrl+K is the browser's search-bar focus on some platforms; the app claims
    // it, so it has to say so.
    const e = press("k", undefined, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });
});

describe("the shift match", () => {
  it("does not fire a no-shift chord while Shift is held", () => {
    // Ctrl+Shift+K and Ctrl+Shift+comma are different chords and belong to the
    // browser. Matching them here would make Ctrl+Shift+K silently mean Ctrl+K.
    press("k", undefined, { ctrlKey: true, shiftKey: true });
    press(",", undefined, { ctrlKey: true, shiftKey: true });
    expect(actions.newChat).not.toHaveBeenCalled();
    expect(actions.toggleSettings).not.toHaveBeenCalled();
  });
});

describe("a focused text field", () => {
  it("sends on Ctrl+Enter from the composer", () => {
    const e = press("Enter", composer(), { ctrlKey: true });
    expect(actions.sendMessage).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("leaves every other chord to the field it was typed in", () => {
    // Ctrl+comma while typing belongs to the textarea (and to the browser),
    // not to the settings panel.
    const e = press(",", composer(), { ctrlKey: true });
    expect(actions.toggleSettings).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
  });

  it("keeps Ctrl+K working from a textarea", () => {
    const e = press("k", composer(), { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("keeps Ctrl+N working from a text input", () => {
    const field = document.getElementById("a-text-field") as HTMLElement;
    const e = press("n", field, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("treats a target that is not an HTML element as no field at all", () => {
    // An inline SVG inside a button is a real keydown target and is not text
    // entry: it has no isContentEditable at all, so the type guard is what
    // decides, and reading a missing property as truthy would suppress every
    // bare-key binding on the page.
    const icon = document.getElementById("an-icon") as unknown as Element;
    press("?", icon);
    expect(actions.showShortcuts).toHaveBeenCalledTimes(1);
  });
});

describe("the init guard", () => {
  it("registers one listener and one table however often it is initialised", () => {
    // A second init would attach a second listener to the shared document, and
    // every chord would then fire twice — which for `newChat` is two chats.
    const second = {
      newChat: vi.fn(),
      toggleShell: vi.fn(),
      toggleFiles: vi.fn(),
      toggleGit: vi.fn(),
      toggleSettings: vi.fn(),
      sendMessage: vi.fn(),
      showShortcuts: vi.fn(),
    };
    initKeyboardShortcuts(second);

    expect(registeredShortcuts()).toHaveLength(7);
    press("k", undefined, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(second.newChat).not.toHaveBeenCalled();
  });
});
