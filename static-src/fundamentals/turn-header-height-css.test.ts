// THE HEADER'S HEIGHT IS THE PROMPT'S HEIGHT, and these are the numbers that
// claim says.
//
// The meta row is gone: every control that used to set the band's height — the
// fold chevron and the copy button — is out of flow, so the only thing left
// contributing height is the request text itself. That is the whole change, and
// it is invisible to a rule-text assertion: `disclosure-row-css.test.ts` can see
// that the chevron is `position: absolute`, and only a measurement can see that
// the band is therefore 30px instead of 86.
//
// Measured rather than declared, because three separate mechanisms have to agree
// for these numbers to hold — the chevron's escape from `--hit-floor`
// (61-mcp-tools.css), the header's own `padding-block`, and the fold-conditional
// clamp — and each lives in a different rule.
//
// `mountAppCSS` assembles the stylesheet from `css/MANIFEST` in declared order,
// the way `cmd/bundle` concatenates it, because equal-specificity ties in this
// app are decided by that order rather than by the selectors.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";

/** 14px `--fs-md` at `line-height: 1.5`. Exact, with no font-metric dependency,
 *  which is what makes a per-line assertion meaningful at all. Measured as the
 *  ADVANCE between consecutive line tops, never as a rect's own height:
 *  `Range.getClientRects()` answers the text's ink box (17px for this font at
 *  this size), so a height assertion would pin a font metric rather than the
 *  leading. */
const LINE = 21;
/** `--sp-1` block padding at each edge, plus the 1px bottom border an OPEN
 *  header carries. A folded one drops that border — `.turn[data-folded] >
 *  .turn-header { border-bottom: 0 }`, on the ground that the line belongs to
 *  the element below and two rules 1px apart is a doubling. */
const CHROME_OPEN = 9;
const CHROME_FOLDED = 8;

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // A real transcript column, so wrapping is deterministic rather than a
  // function of the runner's viewport.
  host.style.width = "390px";
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

vi.mock("../scroll.js", () =>
  import("../__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
);

const LONG =
  "Fix the flaky test in auth_test.go, it fails on CI about one run in five " +
  "and I cannot reproduce it locally no matter how many times I loop it, so " +
  "I suspect a timing dependency somewhere in the token refresh path that " +
  "only shows up under sustained load from the other suites.";

async function turn(
  state: "open" | "folded",
  request: string,
): Promise<{ card: HTMLElement; header: HTMLElement; text: HTMLElement }> {
  const { buildTurnHeader } = await import("./turn-header.js");
  const card = document.createElement("div");
  card.className = "turn";
  if (state === "folded") {
    card.setAttribute("data-folded", "");
  }
  card.appendChild(
    buildTurnHeader({ n: 14, outcome: "completed", ts: Date.now(), request, attachments: [] }),
  );
  host.replaceChildren(card);
  const header = card.querySelector<HTMLElement>(".turn-header");
  const text = card.querySelector<HTMLElement>(".turn-req-text");
  if (header === null || text === null) {
    throw new Error("header or request text missing");
  }
  return { card, header, text };
}

/** One entry per rendered line box, in document order.
 *
 *  `Range.getClientRects()` yields a rect per line box, but a line holding runs
 *  of different font sizes yields several, so they are folded by rounded top —
 *  which is also what makes a "no line is taller than another" assertion
 *  possible at all. */
function lineBoxes(el: HTMLElement): { top: number; left: number; height: number }[] {
  const range = document.createRange();
  range.selectNodeContents(el);
  const origin = el.getBoundingClientRect();
  const byTop = new Map<number, { top: number; left: number; bottom: number }>();
  for (const r of Array.from(range.getClientRects())) {
    if (r.width === 0 && r.height === 0) {
      continue;
    }
    const key = Math.round(r.top);
    const seen = byTop.get(key);
    if (seen === undefined) {
      byTop.set(key, { top: r.top, left: r.left, bottom: r.bottom });
    } else {
      seen.left = Math.min(seen.left, r.left);
      seen.top = Math.min(seen.top, r.top);
      seen.bottom = Math.max(seen.bottom, r.bottom);
    }
  }
  return Array.from(byTop.values())
    .sort((a, b) => a.top - b.top)
    .map((r) => ({
      top: r.top - origin.top,
      left: r.left - origin.left,
      height: r.bottom - r.top,
    }));
}

describe("the header's height is the prompt's height", () => {
  it("is 30px for a one-line request, at both pointer tiers", async () => {
    // 86px on a phone before this change, 66 on desktop, because the meta row's
    // two buttons set the band's height through `--hit-floor`. Nothing in the
    // header reads that token any more, so one number now serves both tiers.
    for (const tier of ["fine", "coarse"] as const) {
      document.documentElement.setAttribute("data-pointer", tier);
      const { header } = await turn("open", "Fix the flaky test");
      expect(header.getBoundingClientRect().height, tier).toBeCloseTo(LINE + CHROME_OPEN, 0);
    }
    document.documentElement.removeAttribute("data-pointer");
  });

  it("grows by exactly one line per line, and no line is taller than another", async () => {
    // The uneven-first-line defect this forbids: leave the chevron in flow and
    // its 44px box makes line 1 taller than the lines under it, which is the
    // shape the reader rejected.
    const { text } = await turn("open", LONG);
    const lines = lineBoxes(text);
    expect(lines.length).toBeGreaterThan(2);
    for (let i = 1; i < lines.length; i++) {
      const advance = (lines[i]?.top ?? 0) - (lines[i - 1]?.top ?? 0);
      expect(advance, `advance into line ${String(i + 1)}`).toBeCloseTo(LINE, 0);
    }
    expect(text.getBoundingClientRect().height).toBeCloseTo(lines.length * LINE, 0);
  });

  it("indents only the first line, so lines 2+ run the full width", async () => {
    // The gutter this replaces: reserving the badge's width with
    // `padding-inline-start` costs that width on EVERY line, which on a phone is
    // most of the prompt. `text-indent` is charged to line 1 alone.
    const { text } = await turn("open", LONG);
    const lines = lineBoxes(text);
    expect(lines[0]?.left ?? 0).toBeGreaterThan(24);
    for (const line of lines.slice(1)) {
      expect(line.left).toBeCloseTo(0, 0);
    }
  });

  it("leaves an open turn's request unclamped, however long", async () => {
    // Coupled to the fold on purpose: while the turn is the one being read, the
    // whole request is visible and there is no second control for it.
    const { text } = await turn("open", LONG.repeat(3));
    expect(text.scrollHeight).toBeCloseTo(text.clientHeight, 0);
    expect(lineBoxes(text).length).toBeGreaterThan(4);
  });

  it("clamps a folded turn's request to four lines, so folded rows stay scannable", async () => {
    // Folded rows are the session's navigation surface. Without the clamp one
    // pasted stack trace renders hundreds of lines as a "collapsed" turn and
    // pushes every neighbouring row off screen.
    const { header, text } = await turn("folded", LONG.repeat(3));
    expect(text.clientHeight).toBeCloseTo(4 * LINE, 0);
    expect(text.scrollHeight).toBeGreaterThan(text.clientHeight);
    expect(header.getBoundingClientRect().height).toBeCloseTo(4 * LINE + CHROME_FOLDED, 0);
  });
});

describe("the out-of-flow controls keep a pointer-sized box at both tiers", () => {
  // Both opt out of `--hit-floor`'s coarse value, and being out of flow neither
  // can move the header's height — so the heights above hold whether the opt-out
  // works or not, and a red check proved they do: dropping `min-height: 0` left
  // every assertion in this file green. What the opt-out actually buys is that a
  // 44px box does not hang below a 30px header and capture clicks in the turn
  // body, so the box itself is the thing to measure.
  //
  // `.turn-fold-toggle`'s existing floor test (`disclosure-row-css.test.ts`)
  // asserts `>= 24`, which a 44px box also satisfies; these pin it from above.
  for (const cls of ["turn-fold-toggle", "turn-copy-req"] as const) {
    it(`.${cls} is 24px square on fine and on coarse`, async () => {
      for (const tier of ["fine", "coarse"] as const) {
        document.documentElement.setAttribute("data-pointer", tier);
        const { card } = await turn("open", "Fix the flaky test");
        const btn = card.querySelector<HTMLElement>(`.${cls}`);
        if (btn === null) {
          throw new Error(`no .${cls}`);
        }
        const box = btn.getBoundingClientRect();
        expect(box.width, `${cls} width on ${tier}`).toBeCloseTo(24, 0);
        expect(box.height, `${cls} height on ${tier}`).toBeCloseTo(24, 0);
      }
      document.documentElement.removeAttribute("data-pointer");
    });

    it(`.${cls} stays inside the header it sits in`, async () => {
      // The turn body starts immediately below, so a box hanging past the
      // header's edge is a control sitting over the reply.
      document.documentElement.setAttribute("data-pointer", "coarse");
      const { card, header } = await turn("open", "Fix the flaky test");
      const btn = card.querySelector<HTMLElement>(`.${cls}`);
      if (btn === null) {
        document.documentElement.removeAttribute("data-pointer");
        throw new Error(`no .${cls}`);
      }
      // Measured while the coarse tier is still in force: reading the box after
      // the attribute comes off measures the fine tier, where the floor this
      // guards against does not apply and the assertion cannot fail.
      const overhang = btn.getBoundingClientRect().bottom - header.getBoundingClientRect().bottom;
      document.documentElement.removeAttribute("data-pointer");

      expect(overhang, `${cls} overhang below the header`).toBeLessThanOrEqual(0.5);
    });
  }
});

describe("the band is the fold's hit target", () => {
  it("puts no control under a point in the middle of the band", async () => {
    // The chevron escapes the coarse hit floor because the target already exists
    // somewhere bigger — `wireRowToggle` forwards the whole band. That trade is
    // only sound if the band really is hittable, and it is not sound if the copy
    // button's absolute box swallows the click at rest: `opacity: 0` still hit
    // tests, which is why it carries `pointer-events: none` there.
    document.documentElement.setAttribute("data-pointer", "coarse");
    const { header } = await turn("open", "Fix the flaky test");
    const box = header.getBoundingClientRect();
    const hit = document.elementFromPoint(box.right - 20, box.top + box.height / 2);
    document.documentElement.removeAttribute("data-pointer");

    expect(hit).not.toBeNull();
    expect(hit?.closest(".turn-copy-req")).toBeNull();
    expect(header.contains(hit)).toBe(true);
  });
});
