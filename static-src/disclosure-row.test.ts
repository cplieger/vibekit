// Unit tests for disclosure-row.ts — the header-row activation surface.
//
// Pure DOM, no mocks: the module imports nothing. The cases are the four ways a
// row click must NOT reach the control, plus the two ways it must, plus the one
// that would have been an infinite loop (the forwarded click bubbling back into
// the listener that sent it).
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { wireRowToggle } from "./disclosure-row.js";

interface Harness {
  row: HTMLElement;
  control: HTMLButtonElement;
  plain: HTMLElement;
  nested: HTMLButtonElement;
  link: HTMLAnchorElement;
  prose: HTMLElement;
  /** How many times the control's own click handler ran. */
  hits: () => number;
}

let built: HTMLElement[] = [];

function build(): Harness {
  const row = document.createElement("div");
  row.className = "row";

  const control = document.createElement("button");
  control.type = "button";
  control.className = "chevron";
  let n = 0;
  control.addEventListener("click", () => {
    n++;
  });

  const plain = document.createElement("span");
  plain.textContent = "title";

  const nested = document.createElement("button");
  nested.type = "button";
  nested.className = "action";
  nested.textContent = "act";

  const link = document.createElement("a");
  link.href = "#somewhere";
  link.textContent = "src/main.ts";

  const prose = document.createElement("div");
  prose.className = "prose";
  prose.textContent = "the request the user typed";

  row.append(control, plain, nested, link, prose);
  document.body.appendChild(row);
  built.push(row);
  return { row, control, plain, nested, link, prose, hits: () => n };
}

/** Click a descendant, so the event bubbles to the row the way a real one does.
 *  `HTMLElement.click()` sets `target` to the element it is called on. */
function clickOn(el: HTMLElement): void {
  el.click();
}

function clearSelection(): void {
  document.getSelection()?.removeAllRanges();
}

beforeEach(() => {
  built = [];
  clearSelection();
});

afterEach(() => {
  for (const el of built) {
    el.remove();
  }
  clearSelection();
});

describe("wireRowToggle", () => {
  it("a click on inert row content activates the control", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.plain);
    expect(h.hits()).toBe(1);
  });

  it("a click on the row itself activates the control", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.row);
    expect(h.hits()).toBe(1);
  });

  it("activates exactly ONCE — the forwarded click does not re-enter", () => {
    // The regression this guards: the row forwards by calling control.click(),
    // and that synthetic click bubbles straight back into the row's own
    // listener. It terminates because the control is a <button>, so the listener
    // sees something that owns its click and stops. A count of 2 (or a stack
    // overflow) is the failure.
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.plain);
    expect(h.hits()).toBe(1);

    clickOn(h.plain);
    expect(h.hits()).toBe(2);
  });

  it("a click on the control itself activates it once, not twice", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.control);
    expect(h.hits()).toBe(1);
  });

  it("a nested button keeps its own click", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.nested);
    expect(h.hits()).toBe(0);
  });

  it("a nested link keeps its own click", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.link);
    expect(h.hits()).toBe(0);
  });

  it("a click inside a live selection does not activate", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    document.getSelection()?.selectAllChildren(h.prose);
    expect(document.getSelection()?.isCollapsed).toBe(false);

    clickOn(h.prose);
    expect(h.hits()).toBe(0);
  });

  it("a collapsed selection does not block activation", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    document.getSelection()?.selectAllChildren(h.prose);
    document.getSelection()?.collapseToStart();

    clickOn(h.plain);
    expect(h.hits()).toBe(1);
  });

  it("skip is consulted with the clicked element and suppresses activation", () => {
    const h = build();
    const seen: string[] = [];
    wireRowToggle(h.row, h.control, {
      skip: (target) => {
        seen.push(target.className);
        return target.closest(".prose") !== null;
      },
    });

    clickOn(h.prose);
    expect(h.hits()).toBe(0);

    clickOn(h.plain);
    expect(h.hits()).toBe(1);
    expect(seen).toEqual(["prose", ""]);
  });

  it("skip returning false leaves the click alone to activate", () => {
    const h = build();
    wireRowToggle(h.row, h.control, { skip: () => false });

    clickOn(h.prose);
    expect(h.hits()).toBe(1);
  });
});
