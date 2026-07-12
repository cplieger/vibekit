// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Find-in-Chat tests.
//
//   1. formatCount — the aria-live counter string (empty / no-match / N of M).
//   2. FindEngine — match discovery, case-insensitivity, highlight/unwrap,
//      visibility pruning, and next/prev stepping with wraparound.
//   3. Ctrl-F overlay integration — hotkey open, eligibility, search, stepping,
//      close + focus restore, and the "second Ctrl-F -> native find" escape
//      hatch.
//
// ./scroll.js is mocked so importing find-in-chat.ts doesn't trigger the
// ScrollController's eager DOM init (which needs #messages / #scroll-bottom).
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./scroll.js", () => ({ setUserScrolledUp: vi.fn() }));

import { FindEngine, formatCount } from "./find-in-chat.js";

function root(html: string): HTMLElement {
  const d = document.createElement("div");
  d.innerHTML = html;
  return d;
}

function marks(el: HTMLElement): HTMLElement[] {
  return [...el.querySelectorAll<HTMLElement>("mark.chat-find-hit")];
}

// ---------------------------------------------------------------------------
// formatCount
// ---------------------------------------------------------------------------

describe("formatCount", () => {
  it("is empty for an empty query", () => {
    expect(formatCount(0, -1, "")).toBe("");
    expect(formatCount(5, 2, "")).toBe("");
  });

  it("reports no matches for a non-empty query with zero hits", () => {
    expect(formatCount(0, -1, "zzz")).toBe("No matches");
  });

  it("is 1-based for humans", () => {
    expect(formatCount(3, 0, "x")).toBe("1 of 3");
    expect(formatCount(3, 2, "x")).toBe("3 of 3");
    expect(formatCount(12, 6, "x")).toBe("7 of 12");
  });
});

// ---------------------------------------------------------------------------
// FindEngine: matching + highlighting
// ---------------------------------------------------------------------------

describe("FindEngine matching", () => {
  it("wraps every match across multiple nodes and marks the first current", () => {
    const el = root(
      `<div class="message">alpha TODO beta</div>` +
        `<div class="message">gamma <b>TODO</b> delta</div>` +
        `<div class="message">nothing here</div>`,
    );
    const eng = new FindEngine(el);
    const n = eng.search("TODO");
    expect(n).toBe(2);
    expect(eng.total).toBe(2);
    expect(marks(el)).toHaveLength(2);
    expect(eng.currentIndex).toBe(0);
    expect(eng.currentMark()).toBe(marks(el)[0]);
    expect(marks(el)[0]?.classList.contains("chat-find-hit-current")).toBe(true);
    expect(marks(el)[1]?.classList.contains("chat-find-hit-current")).toBe(false);
  });

  it("matches case-insensitively but preserves the original casing in the mark", () => {
    const el = root(`<p>Todo todo TODO tODo</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("todo")).toBe(4);
    const texts = marks(el).map((m) => m.textContent);
    expect(texts).toEqual(["Todo", "todo", "TODO", "tODo"]);
  });

  it("finds multiple matches within a single text node and preserves surrounding text", () => {
    const el = root(`<p>a TODO b TODO c</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("TODO")).toBe(2);
    expect(el.textContent).toBe("a TODO b TODO c");
  });

  it("returns zero and a null current mark when nothing matches", () => {
    const el = root(`<p>the quick brown fox</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("zzz")).toBe(0);
    expect(eng.currentIndex).toBe(-1);
    expect(eng.currentMark()).toBeNull();
    expect(marks(el)).toHaveLength(0);
  });

  it("treats an empty query as a clear (no marks)", () => {
    const el = root(`<p>TODO TODO</p>`);
    const eng = new FindEngine(el);
    eng.search("TODO");
    expect(marks(el)).toHaveLength(2);
    expect(eng.search("")).toBe(0);
    expect(marks(el)).toHaveLength(0);
  });

  it("clear() removes all marks and restores the original text; re-search still works", () => {
    const el = root(`<div>x TODO y</div><div>z TODO w</div>`);
    const original = el.textContent;
    const eng = new FindEngine(el);
    eng.search("TODO");
    expect(marks(el)).toHaveLength(2);
    eng.clear();
    expect(marks(el)).toHaveLength(0);
    expect(el.textContent).toBe(original);
    // Re-search after clear finds the same matches (text nodes were merged back).
    expect(eng.search("TODO")).toBe(2);
  });

  it("re-searching replaces the previous highlight (no stale marks)", () => {
    const el = root(`<p>foo bar foo baz</p>`);
    const eng = new FindEngine(el);
    eng.search("foo");
    expect(marks(el)).toHaveLength(2);
    eng.search("ba");
    expect(marks(el)).toHaveLength(2);
    expect(marks(el).map((m) => m.textContent)).toEqual(["ba", "ba"]);
  });
});

// ---------------------------------------------------------------------------
// FindEngine: visibility pruning
// ---------------------------------------------------------------------------

describe("FindEngine visibility", () => {
  it("skips text inside .hidden, [hidden], and aria-hidden subtrees", () => {
    const el = root(
      `<div class="message">visible TODO</div>` +
        `<div class="hidden">hidden TODO</div>` +
        `<div hidden>attr TODO</div>` +
        `<div aria-hidden="true">aria TODO</div>`,
    );
    const eng = new FindEngine(el);
    expect(eng.search("TODO")).toBe(1);
  });

  it("skips a closed <details> but searches an open one", () => {
    const closed = root(`<details><summary>Reasoning</summary><p>TODO inside</p></details>`);
    expect(new FindEngine(closed).search("TODO")).toBe(0);

    const open = root(`<details open><summary>Reasoning</summary><p>TODO inside</p></details>`);
    expect(new FindEngine(open).search("TODO")).toBe(1);
  });

  it("skips the live-streaming bubble", () => {
    const el = root(
      `<div class="message assistant streaming">streaming TODO</div>` +
        `<div class="message assistant">settled TODO</div>`,
    );
    expect(new FindEngine(el).search("TODO")).toBe(1);
  });

  it("skips script and style content", () => {
    const el = root(`<p>real TODO</p><script>var TODO=1;</script><style>.TODO{}</style>`);
    expect(new FindEngine(el).search("TODO")).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// FindEngine: stepping
// ---------------------------------------------------------------------------

describe("FindEngine stepping", () => {
  function threeHits(): { el: HTMLElement; eng: FindEngine } {
    const el = root(`<p>hit hit hit</p>`);
    const eng = new FindEngine(el);
    eng.search("hit");
    return { el, eng };
  }

  it("next() advances and wraps around", () => {
    const { eng } = threeHits();
    expect(eng.currentIndex).toBe(0);
    eng.next();
    expect(eng.currentIndex).toBe(1);
    eng.next();
    expect(eng.currentIndex).toBe(2);
    eng.next();
    expect(eng.currentIndex).toBe(0);
  });

  it("prev() retreats and wraps around", () => {
    const { eng } = threeHits();
    eng.prev();
    expect(eng.currentIndex).toBe(2);
    eng.prev();
    expect(eng.currentIndex).toBe(1);
  });

  it("moves the current class to the active mark", () => {
    const { el, eng } = threeHits();
    eng.next();
    const ms = marks(el);
    expect(ms[0]?.classList.contains("chat-find-hit-current")).toBe(false);
    expect(ms[1]?.classList.contains("chat-find-hit-current")).toBe(true);
    expect(eng.currentMark()).toBe(ms[1]);
  });

  it("next()/prev() are no-ops with zero matches", () => {
    const el = root(`<p>nada</p>`);
    const eng = new FindEngine(el);
    eng.search("xyz");
    eng.next();
    eng.prev();
    expect(eng.currentIndex).toBe(-1);
  });

  it("setCurrent() clamps to the valid range", () => {
    const { eng } = threeHits();
    eng.setCurrent(2);
    expect(eng.currentIndex).toBe(2);
    eng.setCurrent(99); // out of range -> ignored
    expect(eng.currentIndex).toBe(2);
    eng.setCurrent(-5); // out of range -> ignored
    expect(eng.currentIndex).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// Ctrl-F overlay integration
// ---------------------------------------------------------------------------

describe("Ctrl-F overlay", () => {
  // The overlay controller keeps module-level singleton state (the built
  // overlay, open flag). beforeEach wipes the DOM, so we re-import a fresh
  // module graph each test to avoid reusing a now-detached overlay. The
  // ./scroll.js mock persists across resetModules.
  let onHotkey: (e: KeyboardEvent) => void;

  beforeEach(async () => {
    vi.resetModules();
    document.body.innerHTML = `
      <div id="chat-view" data-tab-view>
        <div id="messages-wrap-outer">
          <div id="messages-wrap">
            <div id="messages">
              <div class="message assistant">first TODO here</div>
              <div class="message assistant">second TODO and TODO again</div>
            </div>
          </div>
        </div>
        <textarea id="prompt-input"></textarea>
      </div>
      <div id="shell-panel" class="hidden"><textarea id="term-input"></textarea></div>`;
    const mod = await import("./find-in-chat.js");
    onHotkey = mod.handleFindHotkey;
  });

  function ctrlF(): KeyboardEvent {
    return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
  }

  function input(): HTMLInputElement | null {
    return document.getElementById("chat-find-input") as HTMLInputElement | null;
  }

  function typeAndEnter(value: string, shift = false): void {
    const el = input();
    if (el === null) {
      throw new Error("find input not built");
    }
    el.value = value;
    el.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        shiftKey: shift,
        bubbles: true,
        cancelable: true,
      }),
    );
  }

  it("opens on Ctrl-F when the chat view is active and preventDefaults the browser find", () => {
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(true);
    const overlay = document.getElementById("chat-find");
    expect(overlay).not.toBeNull();
    expect(overlay?.classList.contains("hidden")).toBe(false);
    expect(document.activeElement).toBe(input());
  });

  it("does not hijack Ctrl-F when the chat view is hidden", () => {
    document.getElementById("chat-view")?.classList.add("hidden");
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("does not hijack Ctrl-F while focus is inside the shell panel", () => {
    (document.getElementById("term-input") as HTMLTextAreaElement).focus();
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("searches, updates the counter, steps with Enter, and closes on Escape", () => {
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");

    // Type "TODO" + Enter: lands on the first of three matches.
    typeAndEnter("TODO");
    expect(document.querySelectorAll("mark.chat-find-hit")).toHaveLength(3);
    expect(count?.textContent).toBe("1 of 3");

    // Enter again steps forward; Shift+Enter steps back.
    typeAndEnter("TODO");
    expect(count?.textContent).toBe("2 of 3");
    typeAndEnter("TODO", true);
    expect(count?.textContent).toBe("1 of 3");

    // Escape closes, clears highlights, and restores focus.
    const el = input();
    el?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(document.getElementById("chat-find")?.classList.contains("hidden")).toBe(true);
    expect(document.querySelectorAll("mark.chat-find-hit")).toHaveLength(0);
  });

  it("lets a second Ctrl-F fall through to the browser (escape hatch) while the field is focused", () => {
    onHotkey(ctrlF()); // open, input focused
    expect(document.activeElement).toBe(input());
    const second = ctrlF();
    onHotkey(second);
    expect(second.defaultPrevented).toBe(false);
  });
});
