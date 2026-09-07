// ---------------------------------------------------------------------------
// A tool group's HEADER and the member rows inside it are the same height, and a
// BARE group renders as a plain card.
//
// Both halves are numeric because both were reported by eye and neither is visible
// in source. The heights came from two DIFFERENT tokens — the header `--btn-h`, the
// member rows `--ctl-h-dense` — so a box's own header sat 4px taller than every row
// inside it on both pointer tiers (36/32 fine, 44/40 coarse). The member floor is
// deleted now, so the two sides read ONE declaration
// (`.tool-header { min-height: var(--btn-h) }`) and the equality is structural
// rather than two numbers kept in step.
//
// EVERY FIXTURE HERE IS BUILT BY THE PRODUCTION BUILDERS, and that is not
// convenience. A hand-rolled card with an empty icon slot and a short title measures
// under both floors, so it reports 36px in a bare group AND 36px standalone —
// hiding a real 6px gap, because the member row's `padding-block: var(--sp-1)` was
// still in force while bare. A real card's content clears the floor (40px of line
// box), which is what surfaced it. Do not replace `buildToolCard` with markup here.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

/** Every case here awaits `rendered()`'s three frames per `run()`, and the widest
 *  builds two groups, so six frames is the worst case. That is fine at 60Hz and
 *  6.1s once this browser throttles rAF to 1Hz partway through a full run
 *  (`__test-helpers__/frame-budget.ts`) — which is what failed the four
 *  two-group cases against vitest's 5s default while every one-group case passed. */
const GROUP_TIMEOUT_MS = testTimeoutFor(framesBudgetMs(6));

// The card builder's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the imports are evaluated,
// which is why the imports below are dynamic.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
const { buildToolCard } = await import("./tool-card.js");

/** A transcript-width column, IN the viewport. It was parked at -9999px, which
 *  is no longer a place a `.tool-call` can be measured from — see `rendered()`. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

const FILES = ["auth.go", "runtime.go", "translate.go"];

/** A settled read card, as `messages-tools.ts` builds one. */
function card(i: number): HTMLDivElement {
  return buildToolCard({
    id: `t${String(i)}`,
    title: "Read File",
    kind: "read",
    status: "completed",
    live: false,
    input: { path: `internal/agent/${FILES[i] ?? "x.go"}` },
  });
}

/**
 * Wait for Chromium to decide the mounted cards are near the viewport.
 *
 * `.tool-call` carries `content-visibility: auto` with a `contain-intrinsic-size`
 * fallback (14-tools.css), so until that decision lands a card's OWN box IS the
 * fallback — 40px, whatever its contents measure. A layout query on a DESCENDANT
 * still reports real geometry, because Chromium lays a locked subtree out on
 * demand, and that asymmetry is what makes the failure so quiet: every row height
 * here stayed correct while the two cases that read a card's outer box started
 * reading a constant. The cost was not one red case. With the cards unrendered,
 * the bare-vs-standalone case below compares two copies of the same 40px estimate
 * and stays GREEN with the exact defect it exists to catch planted — measured, by
 * putting the group ROW's tighter `padding-block` back on a bare member: 42 against
 * 42 unrendered, 38 against 42 rendered.
 *
 * Measured in this Chromium: the decision lands on the second frame after the
 * mount, and never at all while the host is off-screen — which is why the host
 * above is in the viewport. Three passes for margin, and it is a count of
 * lifecycle passes rather than a wall-clock wait, so load does not move it.
 */
async function rendered(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
}

/** A real group shell with `n` real members, mounted, header refreshed — which is
 *  what writes the bare class, so no case here sets it by hand — and RENDERED, so
 *  a card's own box is its content and not a containment estimate. */
async function run(n: number): Promise<HTMLElement> {
  const g = buildToolGroupShell();
  for (let i = 0; i < n; i++) {
    groupBody(g).appendChild(card(i));
  }
  host.appendChild(g);
  refreshGroupHeader(g);
  await rendered();
  return g;
}

/** Border-box height in whole pixels, which is the unit every claim here is made
 *  in ("exactly the header's height", "exactly 1px taller").
 *
 *  `offsetHeight` rather than `getBoundingClientRect().height`, and not for
 *  convenience: a rect's `height` is a float SUBTRACTION of two absolute
 *  positions, so a box sitting at a fractional y reports its own height off by a
 *  float epsilon — and both inputs to that are live here, since the group carries
 *  an entry animation that translates it and the root font size is fluid, so
 *  `--btn-h` is 35.999996px at some viewport widths. Measured: the group header
 *  and a member row, both resolving `min-height: var(--btn-h)`, read 36 and
 *  35.999996 in the same pass, which turns an exact-equality case into a
 *  coin-flip on what else is on the page. `offsetHeight` is rounded and ignores
 *  transforms, so it answers the question the assertions actually ask. Both
 *  planted mutants (a 2px hairline, a bare member keeping the row's padding) are
 *  still caught — the regressions this file guards are 1px and 4px, not 0.5px. */
function h(e: Element | null | undefined): number {
  return e instanceof HTMLElement ? e.offsetHeight : -1;
}

function headerOf(g: Element): HTMLElement {
  return g.querySelector<HTMLElement>(":scope > .tool-group-header")!;
}

function members(g: Element): HTMLElement[] {
  return [...g.querySelectorAll<HTMLElement>(":scope > .tool-group-body > .tool-call")];
}

describe(
  "a group header and its member rows resolve to one height",
  { timeout: GROUP_TIMEOUT_MS },
  () => {
    it.each(["fine", "coarse"] as const)("declare the SAME floor on a %s pointer", async (tier) => {
      // The floor is the thing that regressed, so it is asserted directly: the header
      // and every member row must resolve `min-height` to one value. A substitution
      // back to `--ctl-h-dense` fails here at both tiers, where the RENDERED equality
      // below can only see it at one.
      document.documentElement.dataset["pointer"] = tier;
      const g = await run(3);
      const floor = getComputedStyle(headerOf(g)).minHeight;
      expect(
        parseFloat(floor),
        "the header reads a real control-height token",
      ).toBeGreaterThanOrEqual(24);
      for (const [i, m] of members(g).entries()) {
        expect(
          getComputedStyle(m.querySelector(".tool-header")!).minHeight,
          `member ${String(i)} must read the header's own floor, not a dense-tier one`,
        ).toBe(floor);
      }
    });

    it("RESPONDS to the pointer tier, which is what catches a literal", async () => {
      // A hand-tuned literal on either side would satisfy the equality above at one
      // tier and fail here.
      document.documentElement.dataset["pointer"] = "fine";
      const fine = await run(2);
      const fineFloor = getComputedStyle(headerOf(fine)).minHeight;
      const fineRowFloor = getComputedStyle(
        members(fine)[0]!.querySelector(".tool-header")!,
      ).minHeight;
      host.replaceChildren();

      document.documentElement.dataset["pointer"] = "coarse";
      const coarse = await run(2);
      const coarseFloor = getComputedStyle(headerOf(coarse)).minHeight;
      const coarseRowFloor = getComputedStyle(
        members(coarse)[0]!.querySelector(".tool-header")!,
      ).minHeight;

      expect(parseFloat(coarseFloor), "the header follows the tier").toBeGreaterThan(
        parseFloat(fineFloor),
      );
      expect(parseFloat(coarseRowFloor), "and so does the member row").toBeGreaterThan(
        parseFloat(fineRowFloor),
      );
    });

    it("RENDERS a header and its member rows at one height on a fine pointer", async () => {
      // The desktop defect, in the units it was reported in: 36 against 32.
      document.documentElement.dataset["pointer"] = "fine";
      const g = await run(3);
      const head = h(headerOf(g));
      expect(head).toBeGreaterThanOrEqual(24);
      for (const [i, m] of members(g).entries()) {
        expect(
          h(m.querySelector(".tool-header")),
          `member ${String(i)}'s row must be exactly the header's height, not 4px short of it`,
        ).toBe(head);
      }
    });

    it("lets a COARSE pointer grow a member row past that shared floor, because its file chip is a 44px touch target", async () => {
      // Not a regression and not a tolerance: `.tool-file-link` is a real `<button>`,
      // so `61-mcp-tools.css`'s zero-specificity hit-target floor gives it 44px on a
      // coarse pointer and the row has to contain it. The HEADER holds only spans, so
      // nothing pushes it off its floor. Stated here so the difference is not "fixed"
      // by shrinking a touch target — which is what item 1 refused to do to the header.
      document.documentElement.dataset["pointer"] = "coarse";
      const g = await run(1);
      const row = members(g)[0]!.querySelector<HTMLElement>(".tool-header")!;
      const chip = row.querySelector<HTMLElement>("button.tool-file-link")!;
      expect(h(chip), "the chip is at the coarse hit floor").toBeGreaterThanOrEqual(44);
      expect(h(row), "so the row clears the chip").toBeGreaterThanOrEqual(h(chip));
    });

    it("leaves the member's OUTER box exactly 1px taller than its row: the separator hairline", async () => {
      // Named rather than absorbed into a tolerance, so the one legitimate difference
      // between the two boxes is documented by the assertion instead of hidden by it.
      // Measured against the ROW rather than the header, so it holds at both tiers.
      for (const tier of ["fine", "coarse"] as const) {
        document.documentElement.dataset["pointer"] = tier;
        host.replaceChildren();
        const g = await run(3);
        for (const [i, m] of members(g).entries()) {
          expect(h(m), `${tier}: member ${String(i)}'s card is its row plus the separator`).toBe(
            h(m.querySelector(".tool-header")) + 1,
          );
          expect(getComputedStyle(m).borderTopWidth).toBe("1px");
        }
      }
    });
  },
);

describe("a bare group renders as a plain tool card", { timeout: GROUP_TIMEOUT_MS }, () => {
  it("hides its header from the accessibility tree AND from tab order", async () => {
    document.documentElement.dataset["pointer"] = "fine";
    const bare = await run(1);
    // `display: none`, not `visibility`/`aria-hidden`: it is the only one of the
    // three that does both, so a hidden `role="button"` cannot become a dead tab
    // stop advertising a state nobody can change.
    expect(getComputedStyle(headerOf(bare)).display).toBe("none");
    host.replaceChildren();

    const real = await run(2);
    const head = headerOf(real);
    expect(getComputedStyle(head).display).toBe("flex");
    expect(head.getAttribute("aria-expanded")).toBe("true");
  });

  it("matches a standalone card on height, padding and all four chrome properties", async () => {
    document.documentElement.dataset["pointer"] = "fine";
    const standalone = card(0);
    host.appendChild(standalone);
    const bare = await run(1);
    const member = members(bare)[0]!;

    const want = getComputedStyle(standalone);
    const got = getComputedStyle(bare);
    // Read off the DOM, never hardcoded: the two boxes declare these from the same
    // tokens, so a token change must move both or neither.
    expect(got.backgroundColor).toBe(want.backgroundColor);
    expect(got.borderTopWidth).toBe(want.borderTopWidth);
    expect(got.borderTopColor).toBe(want.borderTopColor);
    expect(got.borderTopLeftRadius).toBe(want.borderTopLeftRadius);

    // The bare state drops BOTH of the group's row treatments: the separator
    // hairline (a stray line with no header to separate from) and the tighter
    // `padding-block` (the ROW idiom, which needs a list around it to mean
    // anything). Either one left in place makes a lone call render differently from
    // the card it is.
    expect(getComputedStyle(member).borderTopWidth).toBe("0px");
    const memberPad = getComputedStyle(member.querySelector(".tool-header")!);
    const standalonePad = getComputedStyle(standalone.querySelector(".tool-header")!);
    expect(memberPad.paddingBlockStart).toBe(standalonePad.paddingBlockStart);
    expect(memberPad.paddingBlockEnd).toBe(standalonePad.paddingBlockEnd);

    // Which is what makes the outer boxes agree. Measured with real cards: 42 and
    // 42, against 36 for a member of a real group.
    expect(h(bare)).toBe(h(standalone));
  });

  it("takes the ROW treatment back once a second member lands", async () => {
    // Both halves are dropped for the BARE state only. A two-member group needs the
    // hairline back, or its rows lose the rule that makes them read as a list, and
    // the tighter padding back, or a run of twelve reads as twelve cards.
    document.documentElement.dataset["pointer"] = "fine";
    const bare = await run(1);
    const loneRow = h(members(bare)[0]?.querySelector(".tool-header"));
    host.replaceChildren();

    const g = await run(2);
    for (const m of members(g)) {
      expect(getComputedStyle(m).borderTopWidth).toBe("1px");
    }
    expect(
      h(members(g)[0]?.querySelector(".tool-header")),
      "a member row is DENSER than the same card standing alone",
    ).toBeLessThan(loneRow);
  });
});
