// THE HISTORY ROW'S TARGET IS THE WHOLE BOX, minus the one destructive control in
// it.
//
// The open control used to be a button around the TITLE TEXT, so the target was
// the shape of that text: measured on the shipped stylesheet, 27.8% of the row on
// a mouse and 39.0% under a finger, a band with the kind chip outside it at the
// leading edge, the date outside it at the trailing one, and the row's own second
// and third lines below it. A click anywhere still opened the row through the
// container's delegated listener, so nothing about a mouse click was broken —
// which is why it survived: what the shape decided was the press feedback, the
// focus ring, and what a pointer or an accessibility check resolves at the row's
// own centre, which was a `<span>`.
//
// Real layout in real Chromium, because every assertion here is a box or a hit
// test and a DOM emulator reports 0 for both.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
const host = document.createElement("div");

/** A history row as `history.ts` `buildRow` assembles one. `history.test.ts`
 *  pins that this shape is the one the builder produces; this file owns what the
 *  stylesheet then does with it. */
function mountRow(opts: { delete: boolean; detail?: boolean }): {
  row: HTMLElement;
  open: HTMLElement;
  del: HTMLButtonElement | null;
} {
  const container = document.createElement("div");
  container.id = "history-table";
  container.className = "list-container";

  const row = document.createElement("div");
  row.className = "list-row history-table-row";
  row.setAttribute("data-key", "s:sess_1");

  const open = document.createElement("button");
  open.type = "button";
  open.className = "history-row-main";
  open.setAttribute("aria-label", "Open Rebuild the timeline rail");

  const kind = document.createElement("span");
  kind.className = "history-kind history-kind-chat";
  kind.textContent = "Chat";
  const title = document.createElement("span");
  title.className = "list-row-title";
  const name = document.createElement("span");
  name.className = "list-row-name";
  name.textContent = "Rebuild the timeline rail";
  title.append(name);
  if (opts.detail !== false) {
    const summary = document.createElement("span");
    summary.className = "list-row-summary";
    summary.textContent = "a one line summary of the conversation";
    const facts = document.createElement("span");
    facts.className = "history-facts";
    facts.textContent = "opus · vibe · 12 turns";
    title.append(summary, facts);
  }
  const meta = document.createElement("span");
  meta.className = "list-row-meta";
  meta.textContent = "10/09/2026, 18:36:21";
  open.append(kind, title, meta);

  let del: HTMLButtonElement | null = null;
  if (opts.delete) {
    del = document.createElement("button");
    del.type = "button";
    del.className = "history-delete";
    del.setAttribute("data-history-delete", "s:sess_1");
    del.setAttribute("aria-label", "Delete Rebuild the timeline rail");
  }

  row.append(open);
  if (del !== null) {
    row.append(del);
  }
  container.appendChild(row);
  host.replaceChildren(container);
  return { row, open, del };
}

/** Whether a point activates the open control: a hit on any of its descendants
 *  does, because the click reaches the button by bubbling. */
function opens(open: HTMLElement, x: number, y: number): boolean {
  const hit = document.elementFromPoint(x, y);
  return hit === open || (hit !== null && open.contains(hit));
}

beforeAll(() => {
  style = mountAppCSS();
  host.style.cssText = "position:fixed;inset-block-start:0;inset-inline-start:0;inline-size:900px;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

describe.each(["fine", "coarse"] as const)("on a %s pointer", (tier) => {
  beforeAll(() => {
    document.documentElement.dataset["pointer"] = tier;
  });

  it("gives the open control the row's full height and its leading edge", () => {
    const { row, open } = mountRow({ delete: true });
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    // The row carries no inset of its own, so the control starts where the row
    // does and is as tall as it is. Both halves are what an inset on the ROW
    // would take away.
    expect(o.left, "the control starts at the row's leading edge").toBeCloseTo(r.left, 0);
    expect(o.top, "and at its top").toBeCloseTo(r.top, 0);
    expect(o.height, "and it is the row's whole height").toBeCloseTo(r.height, 0);
  });

  it("covers all but the delete button's own column", () => {
    const { row, open, del } = mountRow({ delete: true });
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    const d = del!.getBoundingClientRect();
    const share = (o.width * o.height) / (r.width * r.height);
    // The number the complaint was about. A regression to a text-shaped control
    // lands near 0.28 on this tier's own measurement, so the floor separates the
    // two shapes rather than merely being satisfied by the current one.
    expect(share, `the control covers ${(share * 100).toFixed(1)}% of the row`).toBeGreaterThan(
      0.9,
    );
    // And it stops short of the destructive control rather than reaching under it.
    expect(o.right, "the open target does not overlap the delete button").toBeLessThanOrEqual(
      d.left + 0.5,
    );
  });

  it("opens from every part of the row a reader would aim at", () => {
    const { row, open } = mountRow({ delete: true });
    const r = row.getBoundingClientRect();
    const cy = r.top + r.height / 2;
    // The four places the old band excluded: the row's own centre (which resolved
    // to a summary span), the kind chip's gutter at the leading edge, the row's
    // last line, and the date.
    expect(opens(open, r.left + r.width / 2, cy), "the row's centre").toBe(true);
    expect(opens(open, r.left + 2, cy), "the leading edge").toBe(true);
    expect(opens(open, r.left + r.width / 2, r.bottom - 2), "the last line").toBe(true);
    expect(opens(open, r.left + r.width / 2, r.top + 2), "the first line").toBe(true);
  });

  it("leaves the delete button its own target, unreachable from the open one", () => {
    const { open, del } = mountRow({ delete: true });
    const d = del!.getBoundingClientRect();
    const cx = d.left + d.width / 2;
    const cy = d.top + d.height / 2;
    const hit = document.elementFromPoint(cx, cy);
    expect(hit === del || (hit !== null && del!.contains(hit)), "the delete button answers").toBe(
      true,
    );
    expect(opens(open, cx, cy), "and the open control does not").toBe(false);
  });

  it("fills the height of a ONE-LINE row, where the delete button sets it", () => {
    // The title alone is shorter than the delete button's 44px touch target on a
    // coarse pointer, so the row's height comes from the delete. Both are
    // `button`s reading the same `--hit-floor`, which is what keeps the control
    // level with it; centred at its content height instead it would leave a dead
    // strip above and below, which is this file's shape one axis in.
    const { row, open } = mountRow({ delete: true, detail: false });
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    expect(o.height, `control ${o.height} against row ${r.height}`).toBeCloseTo(r.height, 0);
    expect(opens(open, r.left + r.width / 2, r.top + 1), "the row's top edge opens").toBe(true);
    expect(opens(open, r.left + r.width / 2, r.bottom - 1), "and its bottom edge").toBe(true);
  });

  it("reaches the trailing edge when the row has no delete button", () => {
    // A run still moving carries none (`buildDeleteButton` withholds it), and the
    // control grows into the space rather than leaving a dead strip.
    const { row, open, del } = mountRow({ delete: false });
    expect(del).toBeNull();
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    expect(o.right).toBeCloseTo(r.right, 0);
    expect(opens(open, r.right - 2, r.top + r.height / 2), "the trailing edge opens").toBe(true);
  });
});
