// ---------------------------------------------------------------------------
// A TOOL GROUP'S HEADER ALWAYS CARRIES A MARK, including while it runs.
//
// The header's verdict slot is a reserved 14px square that `paintGroupOutcome`
// fills with a shared silhouette on settle and writes NO node into while the group
// is running. That left a chevron, a 14px hole with a `--sp-2` gap either side, then
// the count — reported as a blank space that looks strange beside a settled group's
// dot. `14-tools.css` now draws a hollow ring there, and every assertion below is
// numeric because none of it is visible in source: the mark is a pseudo-element, so
// the DOM test beside this one (`tool-group.test.ts`) can only see that the slot
// holds no SVG.
//
// EVERY FIXTURE IS BUILT BY THE PRODUCTION BUILDERS, for the reason
// `tool-group-height.test.ts` records: the state classes come from each member's own
// `data-outcome`, which `buildToolCard` writes through `applyOutcome`, so a
// hand-rolled card would be asserting against a class this suite set itself.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, beforeEach, afterAll, afterEach } from "vitest";

// The card builder's import graph reaches the shared DOM registry, which throws on
// a missing app root, so these ids exist before the imports are evaluated.
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

/** A transcript-width column, off-screen. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

// Per test, not once: the diameter case below drives both pointer tiers, and a case
// inheriting the previous one's tier would pass for a reason it did not state.
beforeEach(() => {
  document.documentElement.dataset["pointer"] = "fine";
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

type Status = "in_progress" | "completed" | "failed";

function card(i: number, status: Status): HTMLDivElement {
  return buildToolCard({
    id: `t${String(i)}`,
    title: "Execute Bash",
    kind: "execute",
    status,
    live: status === "in_progress",
    input: { command: "go build ./..." },
  });
}

/** A real two-member group, mounted, header refreshed — which is what writes the
 *  state class. Two members because a one-member shell is BARE and its header is
 *  `display: none`, so there would be no slot on screen to measure. */
function group(...statuses: Status[]): HTMLElement {
  const g = buildToolGroupShell();
  statuses.forEach((s, i) => {
    groupBody(g).appendChild(card(i, s));
  });
  host.appendChild(g);
  refreshGroupHeader(g);
  return g;
}

function slotOf(g: Element): HTMLElement {
  return g.querySelector<HTMLElement>(".tool-group-header .tool-group-icon")!;
}

const running = (): HTMLElement => slotOf(group("completed", "in_progress"));
const settled = (): HTMLElement => slotOf(group("completed", "completed"));
const broken = (): HTMLElement => slotOf(group("completed", "failed"));

describe("the running group header's slot", () => {
  it("paints a ring, so it is never a blank gap between the chevron and the count", () => {
    const slot = running();
    expect(slot.classList.contains("is-running"), "the fixture reaches the running state").toBe(
      true,
    );
    const ring = getComputedStyle(slot, "::before");
    // The defect, in the three properties that make the mark exist at all.
    expect(ring.content, "the slot draws a mark while the group runs").not.toBe("none");
    expect(parseFloat(ring.width), "and that mark has a real box").toBeGreaterThan(0);
    expect(parseFloat(ring.borderTopWidth), "drawn as a ring").toBeGreaterThan(0);
  });

  it("keeps the ring HOLLOW, so the shape carries the difference from a settled verdict", () => {
    // All four settled marks are solid (a disc, a triangle, two knocked-out discs),
    // so a solid dot here would leave the TINT as the only channel separating
    // "running" from "succeeded" (WCAG 1.4.1). Two channels, asserted separately:
    // the running slot draws no silhouette, and its own ring is unfilled.
    const slot = running();
    const ring = getComputedStyle(slot, "::before");
    expect(slot.querySelector("svg"), "no silhouette while there is no verdict").toBeNull();
    expect(ring.backgroundColor, "the ring is unfilled").toBe("rgba(0, 0, 0, 0)");
    expect(ring.borderTopLeftRadius, "and it is a circle").toBe("50%");

    expect(settled().querySelector("svg"), "a settled group draws the silhouette").not.toBeNull();
  });

  it("does not spin: the running members below carry the motion", () => {
    // A ring turning here would be a second claim of work for one run of calls,
    // beside each running member's own `.tool-spinner` — the rule `.subagent-icon`
    // records for a container. The exec tree spins its equivalent ring, which is
    // exactly why this has to be pinned rather than left to the reader of that file.
    expect(getComputedStyle(running(), "::before").animationName).toBe("none");
  });

  it.each(["fine", "coarse"] as const)(
    "renders at the settled mark's own diameter on a %s pointer, so the header's mark does not resize on settle",
    (tier) => {
      // The two states share one slot, so a hand-picked length makes the mark change
      // size the moment the group settles. The oracle is the RENDERED silhouette, not
      // the ratio the stylesheet uses: the glyph's painted extent comes from its own
      // path bbox scaled by its viewBox, so nothing here restates the CSS arithmetic
      // and a wrong ratio on either side fails.
      document.documentElement.dataset["pointer"] = tier;
      const slot = running();
      const ring = getComputedStyle(slot, "::before");
      // `box-sizing: border-box` on the pseudo, so the declared size IS the outer
      // diameter — asserted, because the reset's `*` does not reach a pseudo-element
      // and the content-box default would put the border outside it.
      expect(ring.boxSizing).toBe("border-box");
      const outer = parseFloat(ring.width);
      // Read before the clear: a detached element measures 0, which would satisfy the
      // fits-the-slot assertion below for the wrong reason.
      const slotWidth = slot.getBoundingClientRect().width;
      host.replaceChildren();

      const mark = settled().querySelector("svg")!;
      const path = mark.querySelector("path")!;
      const units = parseFloat(mark.getAttribute("viewBox")!.split(/\s+/)[2]!);
      const painted = (path.getBBox().width / units) * mark.getBoundingClientRect().width;

      expect(outer).toBeCloseTo(painted, 2);
      expect(outer, "and it fits the slot it is centred in").toBeLessThanOrEqual(slotWidth);
    },
  );

  it("takes its colour from the slot, so the accent stays declared once", () => {
    // `currentColor` through `.tool-icon.is-running`. A literal or a second token
    // would drift from the slot's own colour, which is what this compares.
    const slot = running();
    const ring = getComputedStyle(slot, "::before");
    expect(ring.borderTopColor).toBe(getComputedStyle(slot).color);
    // And the in-flight tint is its own, not the settled one inherited.
    expect(getComputedStyle(slot).color).not.toBe(getComputedStyle(settled()).color);
  });
});

describe("the ring belongs to the running state alone", () => {
  // Built INSIDE the loop, and measured before the host is cleared: a detached
  // element's pseudo resolves `content` to the empty string rather than to `none`,
  // so a fixture built up front and read after a clear passes for the wrong reason.
  it.each([
    ["ok", () => settled()],
    ["fail", () => broken()],
  ] as const)(
    "draws no ring behind a settled %s verdict, so no group carries two marks",
    (_name, build) => {
      expect(
        getComputedStyle(build(), "::before").content,
        "the silhouette is the whole mark",
      ).toBe("none");
    },
  );

  it("leaves the slot's own box identical in both states, which is what keeps the count still", () => {
    // The reason the fix is a pseudo-element rather than sizing the slot to its
    // content: the square is declared, so the summary text does not shift at the
    // moment the group settles. Sizing the slot to the mark would pass every
    // assertion above and reintroduce the shift.
    const run = slotOf(group("completed", "in_progress")).getBoundingClientRect();
    host.replaceChildren();
    const done = slotOf(group("completed", "completed")).getBoundingClientRect();
    expect(run.width).toBe(done.width);
    expect(run.height).toBe(done.height);
    expect(run.width, "and it is a real box").toBeGreaterThan(0);
  });
});
