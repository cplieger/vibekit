// THE HISTORY ROW'S DATE TAKES ITS OWN LINE ON A PHONE, so the thread's title gets
// the row's width.
//
// `.list-row-meta` is `Date.toLocaleString()` — a date AND a time, in a monospace
// face — and it is a flex sibling of `.list-row-title`, which is the only item on
// the line that shrinks (`flex: 1`, so basis 0). So the meta kept its full intrinsic
// width and the title was the thing ellipsised, which is the opposite of the row's
// own priority: reported as not being able to read the title of a thread because the
// date and time took half the width of the box.
//
// Real layout, in an IFRAME: the rule is behind `width <= 40rem` and the page
// viewport is pinned at 1280x720, so the narrow side needs a viewport of its own.
// The desktop case is measured in the page as the control — the date belongs beside
// the title where there is room for both, and a fix that moved it at every width
// would be a regression there.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const LONG_TITLE = "Rebuild vibekit timeline rail and its outcome vocabulary";
const STAMP = "10/09/2026, 18:36:21";

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let phone: Document;

/** A history row as `history.ts` `buildRow` assembles one: kind chip, the title
 *  block, the timestamp, then the delete control. */
function mountRow(
  doc: Document,
  stamp: string,
): { row: HTMLElement; title: HTMLElement; name: HTMLElement; meta: HTMLElement } {
  const container = doc.createElement("div");
  container.id = "history-table";
  container.className = "list-container";

  const row = doc.createElement("div");
  row.className = "list-row history-table-row";
  const kind = doc.createElement("span");
  kind.className = "history-kind history-kind-chat";
  kind.textContent = "Chat";
  const title = doc.createElement("div");
  title.className = "list-row-title";
  const name = doc.createElement("button");
  name.type = "button";
  name.className = "list-row-name";
  name.textContent = LONG_TITLE;
  title.appendChild(name);
  const meta = doc.createElement("span");
  meta.className = "list-row-meta";
  meta.textContent = stamp;
  const del = doc.createElement("button");
  del.type = "button";
  del.className = "history-delete";

  row.append(kind, title, meta, del);
  container.appendChild(row);
  doc.body.replaceChildren(container);
  return { row, title, name, meta };
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = "390";
  frame.height = "844";
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  phone = inner;
  phone.documentElement.dataset["pointer"] = "coarse";
  const sheet = phone.createElement("style");
  sheet.textContent = style.textContent;
  phone.head.appendChild(sheet);
});

afterAll(() => {
  frame.remove();
  style.remove();
});

describe("on a phone", () => {
  it("is on the tier the rule is written for", () => {
    expect(phone.defaultView?.innerWidth).toBeLessThanOrEqual(640);
  });

  it("puts the date BELOW the title rather than beside it", () => {
    const { title, meta } = mountRow(phone, STAMP);
    const t = title.getBoundingClientRect();
    const m = meta.getBoundingClientRect();
    expect(m.height, "the date is rendered").toBeGreaterThan(0);
    expect(
      m.top,
      `the date's top ${m.top} against the title's bottom ${t.bottom}`,
    ).toBeGreaterThanOrEqual(t.bottom - 0.5);
    expect(m.left, "and it is not indented under the title").toBeLessThanOrEqual(t.left);
  });

  it("gives the title the whole first line, so it is readable", () => {
    // The defect stated as a number: the title's own width against what the date
    // was taking out of it.
    const { row, title, name, meta } = mountRow(phone, STAMP);
    const r = row.getBoundingClientRect();
    const t = title.getBoundingClientRect();
    const m = meta.getBoundingClientRect();
    expect(
      t.width,
      `the title has ${t.width}px of a ${r.width}px row while the date measures ${m.width}px`,
    ).toBeGreaterThan(r.width - m.width);
    // And the name really does need it: this title overflows even the whole line,
    // so a share of it would clip proportionally more.
    expect(name.scrollWidth).toBeGreaterThan(0);
  });

  it("opens no second line for a row with no timestamp", () => {
    // `history.ts` renders the span unconditionally and leaves it empty for a row
    // with no `updatedAt`, and an empty item with a 100% basis would still open a
    // flex line and charge the row's own 8px `gap` for it. `:not(:empty)` is what
    // keeps that from happening, and this is the assertion that would notice.
    const withStamp = mountRow(phone, STAMP).row.getBoundingClientRect().height;
    const without = mountRow(phone, "").row.getBoundingClientRect().height;
    expect(without, `${without}px empty against ${withStamp}px with a date`).toBeLessThan(
      withStamp,
    );
    // One line: the title block's own height plus the row's block padding.
    const { row, title } = mountRow(phone, "");
    const pad = parseFloat(getComputedStyle(row).paddingBlockStart) * 2;
    expect(row.getBoundingClientRect().height).toBeCloseTo(
      title.getBoundingClientRect().height + pad,
      0,
    );
  });
});

describe("on a desktop row", () => {
  it("keeps the date beside the title, where there is room for both", () => {
    // The control. The date belongs on the row's own line at a width that can hold
    // it, so the fix must not reach past its breakpoint.
    expect(window.innerWidth).toBeGreaterThan(640);
    const { title, meta } = mountRow(document, STAMP);
    const t = title.getBoundingClientRect();
    const m = meta.getBoundingClientRect();
    expect(m.left, "the date sits after the title on the same line").toBeGreaterThan(t.right - 0.5);
    expect(Math.abs(m.top + m.height / 2 - (t.top + t.height / 2))).toBeLessThan(2);
  });
});
