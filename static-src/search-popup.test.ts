// @vitest-environment happy-dom
// The page search popup, which is what History, the configuration browser and
// the git view's two panels all are now.
//
// Four boxes had four answers to one question before this: two permanent in-flow
// fields built through the shared shell, and two hand-authored `<input
// type="search">` elements with their own magnifier — so the toolbar's magnifier
// meant a floating box on a chat, a full-width field on /history, and nothing at
// all on the git view. What this file pins is the part that could not be shared
// until they agreed on placement: the reveal, and the ONE rule a hidden filter
// needs that a permanent one did not.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { createSearchPopup } from "./search-popup.js";
import type { FindKind } from "./find-registry.js";

function fixture(): HTMLElement {
  document.body.innerHTML = `
    <button type="button" id="find-btn" aria-pressed="false"></button>
    <div id="host"></div>`;
  return document.getElementById("host") as HTMLElement;
}

/** A popup over a synchronous query, which is what all three filters are. */
function build(over: { note?: boolean; kind?: FindKind } = {}) {
  const seen: string[] = [];
  const rendered: string[] = [];
  const popup = createSearchPopup<string>({
    id: "probe",
    kind: "filter",
    label: "Probe things",
    placeholder: "Probe\u2026",
    host: () => document.getElementById("host"),
    ...over,
    query: (q) => {
      seen.push(q);
      return q;
    },
    render: (r) => {
      rendered.push(r ?? "");
    },
  });
  return { popup, seen, rendered };
}

function typeInto(value: string): void {
  const input = document.getElementById("probe-input") as HTMLInputElement;
  input.value = value;
  input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
}

beforeEach(() => {
  fixture();
});

describe("building", () => {
  it("builds nothing until it is opened", () => {
    const { popup } = build();
    expect(document.getElementById("probe-input")).toBeNull();
    expect(popup.shell).toBeNull();
    popup.open();
    expect(document.getElementById("probe-input")).not.toBeNull();
    expect(popup.shell).not.toBeNull();
  });

  it("DECLINES when its host is not there, so the chord reaches native find", () => {
    // Every page here is lazily loaded, so the host arrives with the page. A
    // decline is the honest answer, not an exception.
    document.body.innerHTML = `<button type="button" id="find-btn"></button>`;
    const { popup } = build();
    expect(popup.open()).toBe(false);
    expect(popup.isOpen()).toBe(false);
  });

  it("is hidden before its first open, so it takes no clicks it was never given", () => {
    // The primitive writes `[hidden]` only at the END of a leave, so a freshly
    // built panel is visible to the layout — and this one is a fixed-position box
    // at opacity 0 over the page.
    const { popup } = build();
    popup.close();
    expect(popup.isOpen()).toBe(false);
  });

  it("carries the shared classes rather than a per-page skin", () => {
    const { popup } = build();
    popup.open();
    const region = document.getElementById("probe");
    expect(region?.className).toContain("page-find");
    expect(region?.className).toContain("search-pop");
    expect(region?.className).toContain("uip-popup");
    expect(region?.getAttribute("role")).toBe("search");
  });
});

describe("closing clears", () => {
  // The rule a hidden box needs and a permanent one did not. A popup that closed
  // holding `redis` would leave the page showing three of forty rows with nothing
  // on screen saying why, and the only way back would be reopening a box the
  // reader has no reason to think is still armed.
  it("empties the field and re-runs, so the page repaints unfiltered", () => {
    const { popup, seen } = build();
    popup.open();
    // Opening runs nothing: the close below is what clears, so a freshly opened
    // box is always empty and a run would be a repaint of the list already on
    // screen — which on History is a refetch of every session.
    expect(seen).toEqual([]);
    typeInto("redis");
    expect(seen).toEqual(["redis"]);
    popup.close();
    expect((document.getElementById("probe-input") as HTMLInputElement).value).toBe("");
    expect(seen.at(-1)).toBe("");
  });

  it("does not re-run when there was nothing typed", () => {
    // Closing an untouched box must not be a refetch of the list already on
    // screen.
    const { popup, seen } = build();
    popup.open();
    const before = seen.length;
    popup.close();
    expect(seen.length).toBe(before);
  });

  it("reset() clears WITHOUT the repaint, for a page tearing its view down", () => {
    const { popup, seen } = build();
    popup.open();
    typeInto("redis");
    const before = seen.length;
    popup.reset();
    expect((document.getElementById("probe-input") as HTMLInputElement).value).toBe("");
    expect(seen.length, "the next mount owns that render").toBe(before);
  });

  it("Escape closes, so one gesture has one outcome", () => {
    const { popup, seen } = build();
    popup.open();
    typeInto("redis");
    const input = document.getElementById("probe-input") as HTMLInputElement;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(popup.isOpen()).toBe(false);
    expect(input.value).toBe("");
    expect(seen.at(-1)).toBe("");
  });

  it("the × closes it too", () => {
    const { popup } = build();
    popup.open();
    const close = document.querySelector<HTMLButtonElement>('#probe [aria-label="Close filter"]');
    expect(close).not.toBeNull();
    close?.click();
    expect(popup.isOpen()).toBe(false);
  });
});

describe("the toolbar magnifier", () => {
  it("takes aria-pressed on open and gives it back on close", () => {
    // aria-pressed, not aria-expanded: find is a TOGGLE, and 70-selection.css
    // already styles `.icon-btn[aria-pressed="true"]` as the app's one selected
    // treatment.
    const { popup } = build();
    popup.open();
    expect(document.getElementById("find-btn")?.getAttribute("aria-pressed")).toBe("true");
    popup.close();
    expect(document.getElementById("find-btn")?.getAttribute("aria-pressed")).toBe("false");
  });

  it("toggles rather than only opening", () => {
    const { popup } = build();
    popup.toggle();
    expect(popup.isOpen()).toBe(true);
    popup.toggle();
    expect(popup.isOpen()).toBe(false);
  });

  it("restores focus to wherever it was", () => {
    const { popup } = build();
    const btn = document.getElementById("find-btn") as HTMLButtonElement;
    btn.focus();
    popup.open();
    expect(popup.focused()).toBe(true);
    popup.close();
    expect(document.activeElement).toBe(btn);
  });

  it("re-opening an already-open box lands the caret in it", () => {
    // show() on an open popup is a no-op reveal, so both doors — the button and
    // the chord — have to focus explicitly.
    const { popup } = build();
    popup.open();
    (document.getElementById("find-btn") as HTMLButtonElement).focus();
    expect(popup.focused()).toBe(false);
    popup.open();
    expect(popup.focused()).toBe(true);
  });
});

describe("search versus filter", () => {
  // ONE component, two readings. The kind decides the glyph and the wording and
  // nothing structural, so a reader learns one control and is told which of the
  // two things this page has.
  it("draws a funnel for a filter and a magnifier for a search", () => {
    const filter = build({ kind: "filter" });
    filter.popup.open();
    expect(document.querySelector("#probe .page-find-icon polygon")).not.toBeNull();
    expect(document.querySelector("#probe .page-find-icon circle")).toBeNull();

    fixture();
    const search = build({ kind: "search" });
    search.popup.open();
    expect(document.querySelector("#probe .page-find-icon circle")).not.toBeNull();
    expect(document.querySelector("#probe .page-find-icon polygon")).toBeNull();
  });

  it("names the kind in the × and in the field's tooltip", () => {
    // The word a reader hears has to match the glyph they see.
    const filter = build({ kind: "filter" });
    filter.popup.open();
    expect(document.querySelector('#probe [aria-label="Close filter"]')).not.toBeNull();
    expect((document.getElementById("probe-input") as HTMLInputElement).title).toContain("Filter");

    fixture();
    const search = build({ kind: "search" });
    search.popup.open();
    expect(document.querySelector('#probe [aria-label="Close search"]')).not.toBeNull();
    expect((document.getElementById("probe-input") as HTMLInputElement).title).toContain("Search");
  });

  it("reports its own kind, so a page hands the popup straight to the registry", () => {
    expect(build({ kind: "search" }).popup.kind()).toBe("search");
    expect(build({ kind: "filter" }).popup.kind()).toBe("filter");
  });

  it("states the Ctrl-F escape hatch either way", () => {
    const { popup } = build();
    popup.open();
    expect((document.getElementById("probe-input") as HTMLInputElement).title).toContain("Ctrl+F");
  });
});

describe("what it does NOT offer", () => {
  it("has no match-case toggle on either kind, for two reasons that agree", () => {
    // A filter folds the query AND the row it matches it against, so there is
    // nothing a toggle could change; the one page search is case-insensitive at
    // its endpoint by decision. The surfaces that DO offer it have a cursor.
    const { popup } = build();
    popup.open();
    expect(document.querySelector('#probe [aria-label="Match case"]')).toBeNull();
  });

  it("has no prev/next, because a ranked list has no cursor", () => {
    const { popup } = build();
    popup.open();
    expect(document.querySelector('#probe [aria-label="Next match"]')).toBeNull();
    expect(document.querySelector('#probe [aria-label="Previous match"]')).toBeNull();
  });

  it("is not type=search, because it carries its own ×", () => {
    // Two clear controls a thumb-width apart, doing different things, is worse
    // than one.
    const { popup } = build();
    popup.open();
    expect((document.getElementById("probe-input") as HTMLInputElement).type).toBe("text");
  });

  it("builds the note only when the page asked for one", () => {
    const { popup } = build();
    popup.open();
    expect(document.getElementById("probe-note")).toBeNull();
    document.body.innerHTML = "";
    fixture();
    const withNote = build({ note: true });
    withNote.popup.open();
    expect(document.getElementById("probe-note")).not.toBeNull();
  });
});

describe("the query lifecycle is the shell's, not a second copy", () => {
  it("renders what the query returned, for the query that returned it", () => {
    const { popup, rendered } = build();
    popup.open();
    typeInto("alpha");
    expect(rendered.at(-1)).toBe("alpha");
  });

  it("runs on Enter without waiting for the debounce", () => {
    const { popup, seen } = build();
    popup.open();
    typeInto("beta");
    expect(seen).toContain("beta");
  });

  it("cancels in-flight work on close", () => {
    const cancel = vi.fn();
    const { popup } = build();
    popup.open();
    const shell = popup.shell;
    if (shell === null) {
      throw new Error("not built");
    }
    const original = shell.cancel;
    Object.defineProperty(shell, "cancel", { value: cancel, configurable: true });
    popup.close();
    expect(cancel).toHaveBeenCalled();
    Object.defineProperty(shell, "cancel", { value: original, configurable: true });
  });
});
