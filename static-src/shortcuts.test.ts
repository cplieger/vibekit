// @vitest-environment happy-dom
// The keyboard shortcut reference sheet: its content, and the binding that opens
// it.
//
// The content half exists because the sheet is GENERATED from keys.ts's registry.
// A transcribed list would drift, so the tests assert the generated rows are
// exactly the registry (nothing missing, nothing invented) and separately pin the
// seven chords the app ships — so a new binding forces a look at this file rather
// than appearing silently.
//
// The binding half covers the two mechanical traps a bare `?` walks into:
// keys.ts returns early on any unmodified key, so the sheet cannot be a
// register() row; and app.ts's focusComposerOnTyping redirects any bare printable
// key into the composer, which would type the `?` there while the sheet opened.
//
// keys.ts registers its document listener ONCE per module instance (the
// `initialized` guard), so this file initialises it once with one recording stub
// set and clears the counts per test. Re-importing it with vi.resetModules would
// leave the earlier instance's listener attached to the shared document, and that
// listener's stopImmediatePropagation would swallow the event before the new one
// saw it.

import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";

const openModal = vi.fn();
vi.mock("./modals.js", () => ({
  openModal: (el: HTMLElement) => openModal(el),
  closeTopModal: () => false,
}));

import { sheetGroups, openShortcutsSheet } from "./shortcuts.js";
import { initKeyboardShortcuts, registeredShortcuts } from "./keys.js";

const actions = {
  newChat: vi.fn(),
  toggleShell: vi.fn(),
  toggleFiles: vi.fn(),
  toggleGit: vi.fn(),
  toggleSettings: vi.fn(),
  sendMessage: vi.fn(),
  showShortcuts: vi.fn(),
};

function press(key: string, target?: HTMLElement, extra: KeyboardEventInit = {}): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...extra });
  (target ?? document.body).dispatchEvent(e);
  return e;
}

/** The (chord, description) pair per registered binding, as the sheet must
 *  render it. Derived from the registry so the comparison is a coverage check
 *  rather than a second transcription. */
function registryPairs(): string[] {
  return registeredShortcuts().map((b) => {
    const keys = ["Ctrl"];
    if (b.shift) {
      keys.push("Shift");
    }
    keys.push(b.key.length === 1 ? b.key.toUpperCase() : b.key);
    return `${keys.join("+")}=${b.description}`;
  });
}

beforeAll(() => {
  initKeyboardShortcuts(actions);
});

beforeEach(() => {
  for (const fn of [openModal, ...Object.values(actions)]) {
    fn.mockClear();
  }
  document.body.innerHTML = `
    <div id="shortcuts-modal"><div id="shortcuts-list"></div></div>
    <textarea id="prompt-input"></textarea>
    <input id="a-text-field" type="text">
    <div id="an-editor" contenteditable="true"></div>`;
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("the sheet's content", () => {
  it("covers every chord keys.ts registers, and invents none", () => {
    // "Elsewhere" is the authored group for bindings no register() call owns.
    const generated = sheetGroups()
      .filter((g) => g.name !== "Elsewhere")
      .flatMap((g) =>
        g.rows.flatMap((r) => r.chords.map((c) => `${c.join("+")}=${r.description}`)),
      );
    expect(generated.slice().sort()).toEqual(registryPairs().slice().sort());
  });

  it("ships exactly the chords the app registers today", () => {
    // Deliberately exhaustive: a binding added to keys.ts fails here until it is
    // acknowledged, which is the moment to check it reads correctly on the sheet.
    expect(registeredShortcuts().map((b) => `${b.shift ? "Shift+" : ""}${b.key}`)).toEqual([
      "k",
      "n",
      "/",
      "Shift+f",
      "Shift+g",
      ",",
      "Enter",
    ]);
  });

  it("merges the chords that do the same thing into one row", () => {
    const chats = sheetGroups().find((g) => g.name === "Chats");
    expect(chats?.rows).toHaveLength(1);
    expect(chats?.rows[0]?.description).toBe("New conversation");
    // Ctrl+K and Ctrl+N are the same action, so the sentence is printed once.
    expect(chats?.rows[0]?.chords).toEqual([
      ["Ctrl", "K"],
      ["Ctrl", "N"],
    ]);
  });

  it("keeps the registration order, so the groups read as they were declared", () => {
    expect(sheetGroups().map((g) => g.name)).toEqual(["Chats", "Panels", "Composer", "Elsewhere"]);
  });

  it("lists the bindings that live outside the registry", () => {
    const other = sheetGroups().find((g) => g.name === "Elsewhere");
    // Escape and `?` (keys.ts, above the modifier gate), Ctrl+F TWICE (app.ts's
    // capture-phase listener routes it by the active tab's kind, so the chord
    // has two meanings and the sheet says both) and F2 (files.ts). A sheet
    // missing these is wrong for the reader even though no register() call owns
    // them.
    expect(other?.rows.map((r) => r.chords[0]?.join("+"))).toEqual([
      "Esc",
      "Ctrl+F",
      "Ctrl+F",
      "F2",
      "?",
    ]);
    expect(other?.rows.map((r) => r.description)).toEqual([
      "Close a dialog, or clear the file browser selection",
      "Search this conversation",
      "Find in files, from a file browser or editor tab",
      "Rename in the file browser",
      "Show this list",
    ]);
  });

  it("renders every row into the modal and opens it", () => {
    openShortcutsSheet();

    const list = document.getElementById("shortcuts-list");
    expect(list?.querySelectorAll(".shortcut-group").length).toBe(sheetGroups().length);
    const rows = sheetGroups().reduce((n, g) => n + g.rows.length, 0);
    expect(list?.querySelectorAll(".shortcut-row").length).toBe(rows);
    // The keys are <kbd>, so a screen reader announces them as keys.
    expect(list?.querySelectorAll("kbd").length ?? 0).toBeGreaterThan(rows);
    expect(openModal).toHaveBeenCalledTimes(1);
  });

  it("rebuilds on every open rather than accumulating", () => {
    openShortcutsSheet();
    const first = document.getElementById("shortcuts-list")?.childElementCount;
    openShortcutsSheet();
    expect(document.getElementById("shortcuts-list")?.childElementCount).toBe(first);
  });
});

describe("the ? binding", () => {
  it("opens the sheet from a bare ?", () => {
    const e = press("?");
    expect(actions.showShortcuts).toHaveBeenCalledTimes(1);
    // preventDefault, so the character does not also reach the page.
    expect(e.defaultPrevented).toBe(true);
  });

  it("stops the composer-focus handler from typing the ? as well", () => {
    // app.ts registers focusComposerOnTyping on document AFTER keys.ts, in the
    // same phase: it fires for any bare printable key and would focus the
    // composer and let the same keystroke land in it.
    const sibling = vi.fn();
    document.addEventListener("keydown", sibling);
    try {
      press("?");
      expect(actions.showShortcuts).toHaveBeenCalledTimes(1);
      expect(sibling).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener("keydown", sibling);
    }
  });

  it("lets every other bare printable key through to that handler", () => {
    const sibling = vi.fn();
    document.addEventListener("keydown", sibling);
    try {
      press("a");
      expect(actions.showShortcuts).not.toHaveBeenCalled();
      expect(sibling).toHaveBeenCalledTimes(1);
    } finally {
      document.removeEventListener("keydown", sibling);
    }
  });

  it("does not open from any text-entry surface", () => {
    for (const id of ["prompt-input", "a-text-field", "an-editor"]) {
      const el = document.getElementById(id);
      expect(el, id).not.toBeNull();
      const e = press("?", el as HTMLElement);
      expect(
        actions.showShortcuts,
        `? in #${id} must be a character, not a shortcut`,
      ).not.toHaveBeenCalled();
      // The key belongs to the field, so the default must stand.
      expect(e.defaultPrevented).toBe(false);
    }
  });

  it("is not reachable as a modifier chord", () => {
    // `?` is Shift+/ already. Ctrl+? and Alt+? are different chords and belong to
    // the browser, not to this sheet.
    press("?", undefined, { ctrlKey: true });
    press("?", undefined, { metaKey: true });
    press("?", undefined, { altKey: true });
    expect(actions.showShortcuts).not.toHaveBeenCalled();
  });

  it("leaves the registered Ctrl chords working", () => {
    press("k", undefined, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    press("/", undefined, { ctrlKey: true });
    expect(actions.toggleShell).toHaveBeenCalledTimes(1);
    press("f", undefined, { ctrlKey: true, shiftKey: true });
    expect(actions.toggleFiles).toHaveBeenCalledTimes(1);
    // Ctrl+F without Shift is the transcript search, owned elsewhere.
    press("f", undefined, { ctrlKey: true });
    expect(actions.toggleFiles).toHaveBeenCalledTimes(1);
  });
});
