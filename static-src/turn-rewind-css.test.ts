// The turn footer's Rewind: glyph plus word on desktop, glyph alone on a narrow
// row.
//
// Two claims, and they need two kinds of test. That the label really disappears
// is a COMPUTED fact against the assembled cascade — a source read cannot answer
// it, since `.turn-rewind-label` has to win inside the query. That its
// breakpoint MATCHES the one the footer's actions collapse at is a SOURCE fact,
// and computed style cannot answer it: nothing couples the two numbers except
// their being equal. There is no device attribute to key on (`device-view.ts`
// holds no viewport width), and the two rules live in different files, so if one
// moves the row gets a word back at a width that cannot hold it, or loses it at
// one that can.
//
// The viewport is pinned at 1280x720 (vitest.config.ts) and nothing resizes it,
// so the narrow side is measured in an IFRAME, which carries a viewport of its
// own for the width query to evaluate against.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const BREAKPOINT = "width <= 40rem";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** The footer's Rewind as `mountRewind` builds it: the glyph, then the label. */
function mountRewind(doc: Document): { btn: HTMLElement; label: HTMLElement; glyph: HTMLElement } {
  const footer = doc.createElement("div");
  footer.className = "turn-footer";
  const btn = doc.createElement("button");
  btn.className = "turn-rewind";
  btn.type = "button";
  const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg") as unknown as HTMLElement;
  glyph.setAttribute("class", "ic-ui");
  const label = doc.createElement("span");
  label.className = "turn-rewind-label";
  label.textContent = "Rewind";
  btn.append(glyph, label);
  footer.appendChild(btn);
  doc.body.replaceChildren(footer);
  return { btn, label, glyph };
}

describe("Rewind at desktop width", () => {
  it("shows the glyph AND the word", () => {
    const { label, glyph } = mountRewind(document);
    expect(window.innerWidth).toBeGreaterThan(640);
    expect(getComputedStyle(label).display).not.toBe("none");
    expect(getComputedStyle(glyph).display).not.toBe("none");
  });

  it("lays the two out on one centred line, with a gap between them", () => {
    const { btn } = mountRewind(document);
    const cs = getComputedStyle(btn);
    // `flex`, not the authored `inline-flex`: the footer is a grid and a grid
    // item's display BLOCKIFIES, so this is what production resolves. Measured
    // rather than assumed — the authored value is the wrong observable here.
    expect(cs.display).toBe("flex");
    expect(cs.alignItems).toBe("center");
    expect(parseFloat(cs.columnGap)).toBeGreaterThan(0);
  });
});

describe("Rewind on a narrow row", () => {
  let frame: HTMLIFrameElement;
  let doc: Document;

  beforeAll(() => {
    frame = document.createElement("iframe");
    // 600px is inside 40rem (640px at the 16px root size the app never changes);
    // the height is irrelevant to a width query.
    frame.width = "600";
    frame.height = "400";
    document.body.appendChild(frame);
    const inner = frame.contentDocument;
    if (inner === null) {
      throw new Error("iframe has no contentDocument");
    }
    doc = inner;
    const sheet = doc.createElement("style");
    sheet.textContent = style.textContent;
    doc.head.appendChild(sheet);
  });

  afterAll(() => {
    frame.remove();
  });

  it("drops the word and keeps the glyph", () => {
    expect(doc.defaultView?.innerWidth).toBeLessThanOrEqual(640);
    const { label, glyph } = mountRewind(doc);
    expect(getComputedStyle(label).display).toBe("none");
    expect(getComputedStyle(glyph).display).not.toBe("none");
  });

  it("centres the glyph, so the hit-target floor cannot leave it off-centre", () => {
    // The floor lifts this box to `--hit-floor` (61-mcp-tools.css) without
    // touching its content alignment, which is what renders a floor-widened
    // icon-only button visibly off-centre.
    const { btn } = mountRewind(doc);
    expect(getComputedStyle(btn).justifyContent).toBe("center");
  });
});

describe("the label's breakpoint", () => {
  it("is the width the footer's own actions collapse at", () => {
    // The assertion that stops the two drifting apart. Read as PRELUDES rather
    // than as a shared token, because a media query cannot read a custom
    // property — so the coupling is the number, and two files stating it is the
    // most either side can do.
    const turns = ruleContaining(loadCSS("29-turns.css"), ".turn-rewind-label", BREAKPOINT);
    expect(turns.body).toMatch(/display:\s*none/u);

    const actions = ruleContaining(
      loadCSS("61-mcp-tools.css"),
      ".turn-actions-more > .turn-action-more",
      BREAKPOINT,
    );
    expect(actions.body).toMatch(/display:\s*inline-flex/u);
  });

  it("states the label's hide only inside that query", () => {
    // The scope argument is mandatory: a top-level `display` on the label would
    // hide the word at every width, which is the regression this catches.
    expect(() => ruleContaining(loadCSS("29-turns.css"), ".turn-rewind-label", "top")).toThrow();
  });
});
