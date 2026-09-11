// A DOCS ROW WITH ONE BLOCK IS AS DENSE AS ANY OTHER LIST ROW.
//
// `.docs-row` carries `padding-block: var(--sp-2)` because its rows normally stack
// three blocks (title, badge line, description) and the shared 4px packed adjacent
// rows against the separator. A SPEC has no front-matter, so `metaFor` yields no
// badges and `subtitleFor` no description and both are withheld when empty — the
// surface holds `.docs-row-top` alone, and the three-block padding then leaves the
// title floating in a tall box. Reported as the spec titles not being vertically
// centred, which they are: `align-items: center` was never the defect.
//
// Real layout, because the claim is geometric: the row's height, its padding and
// where the line's midpoint actually lands.
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
const hosts: HTMLElement[] = [];

/** A docs row as `docs.ts` `rowParts` assembles one: the surface holds the title
 *  block, then optionally a badge line and a description. */
function row(blocks: number): HTMLElement {
  const outer = document.createElement("div");
  outer.className = "docs-row list-row";
  const surface = document.createElement("div");
  surface.className = "docs-row-surface";
  const top = document.createElement("div");
  top.className = "docs-row-top";
  const name = document.createElement("span");
  name.className = "list-row-name";
  name.textContent = "vibekit-performance";
  top.append(name);
  surface.append(top);
  if (blocks > 1) {
    const meta = document.createElement("span");
    meta.className = "list-row-meta docs-row-meta";
    meta.append(Object.assign(document.createElement("span"), { className: "docs-badge" }));
    surface.append(meta);
  }
  if (blocks > 2) {
    const sub = document.createElement("div");
    sub.className = "docs-row-sub";
    sub.textContent = "A description of the document.";
    surface.append(sub);
  }
  outer.append(surface);
  document.body.append(outer);
  hosts.push(outer);
  return outer;
}

const padBlock = (el: HTMLElement): number =>
  Number.parseFloat(getComputedStyle(el).paddingBlockStart);

/** How far the name's midpoint sits from the row's own. 0 is centred. */
function midpointDelta(outer: HTMLElement): number {
  const name = outer.querySelector(".list-row-name");
  if (!(name instanceof HTMLElement)) {
    throw new Error("no name");
  }
  const r = outer.getBoundingClientRect();
  const n = name.getBoundingClientRect();
  return (n.top + n.bottom) / 2 - (r.top + r.bottom) / 2;
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

describe("a one-block docs row", () => {
  it("drops the three-block padding", () => {
    const one = padBlock(row(1));
    const three = padBlock(row(3));
    expect(one, "a single line must not wear the stacked padding").toBeLessThan(three);
  });

  it("is no taller than a row of the same one line elsewhere on the page", () => {
    // The tier floor decides its height once the padding is out of the way, which is
    // what makes it match History, Tools and the git rows rather than merely being
    // smaller than it was.
    const spec = row(1);
    const plain = document.createElement("div");
    plain.className = "list-row";
    const n = document.createElement("span");
    n.className = "list-row-name";
    n.textContent = "vibekit-performance";
    plain.append(n);
    document.body.append(plain);
    hosts.push(plain);

    expect(spec.getBoundingClientRect().height).toBeCloseTo(
      plain.getBoundingClientRect().height,
      0,
    );
  });

  it("centres its line, which it did before too", () => {
    // The control for the whole change: the reported symptom was mis-centring, and
    // it was not. If this ever fails the diagnosis was wrong, not the padding.
    expect(Math.abs(midpointDelta(row(1)))).toBeLessThan(1);
  });

  it("leaves a stacked row's padding alone", () => {
    // The rule is scoped by block COUNT, so the rows it must not touch are the ones
    // with something on the second line.
    expect(padBlock(row(2))).toBe(padBlock(row(3)));
  });
});
