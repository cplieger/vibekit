// The site audit as an assertion, and the guard that keeps its list CLOSED.
//
// The rule, once: duration text on the turn axis is NEVER painted at rest, and every
// reader — pointer, keyboard, touch, assistive technology — reaches the same value
// through the turn footer's ledger disclosure. What keeps a new slot from being
// hover-gated instead is an enumeration plus a sweep that fails on a slot nobody
// added to it.
//
// Every rest-state claim is measured TWICE: against the bundle as shipped, and
// against one with the hover query stripped, which is what a device answering
// `any-hover: none` computes. A test page cannot answer a query it does not match
// and this provider exports no CDP seam, so dropping the blocks is the emulation.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";

import { manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: Browser Mode links for real
  // rather than reading properties off a namespace object, and no path here opens a
  // diff.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: undefined,
}));

const { buildTurnFooter, hasTurnSummary } = await import("./fundamentals/turn-footer.js");

const REVEAL_QUERY = "any-hover: hover";
/** Four hours, the gap the clean-turn case discloses. */
const GAP_MS = 14_400_000;

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

/** The bundle a device with no hover computes: every `@media (any-hover: hover)`
 *  block dropped, nested ones included. Comments go first, so a rule's own prose
 *  naming the query cannot be mistaken for the query. */
function withoutHoverBlocks(css: string): string {
  const marker = `@media (${REVEAL_QUERY})`;
  let out = css.replace(/\/\*[\s\S]*?\*\//gu, " ");
  for (;;) {
    const at = out.indexOf(marker);
    if (at < 0) {
      return out;
    }
    const open = out.indexOf("{", at);
    let depth = 0;
    let end = out.length - 1;
    for (let i = open; i < out.length; i++) {
      if (out[i] === "{") {
        depth++;
      } else if (out[i] === "}") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    out = out.slice(0, at) + out.slice(end + 1);
  }
}

/** Run one case against that bundle instead of the shipped one. */
async function withNoHoverCSS(fn: () => Promise<void> | void): Promise<void> {
  const stripped = document.createElement("style");
  stripped.textContent = withoutHoverBlocks(style.textContent ?? "");
  style.remove();
  document.head.appendChild(stripped);
  try {
    await fn();
  } finally {
    stripped.remove();
    document.head.appendChild(style);
  }
}

/** The reveal is a 0.2s opacity transition, so the value one tick after focus is
 *  still the resting one. Poll for the settled end rather than the first frame. */
async function expectRevealed(slot: HTMLElement): Promise<void> {
  await vi.waitFor(() => {
    expect(getComputedStyle(slot).opacity).toBe("1");
  });
}

/** A chat-area container at a stated inline size, which is what gates site 4. */
function mountChatArea(px: number): HTMLElement {
  const area = document.createElement("div");
  area.style.containerType = "inline-size";
  area.style.containerName = "chat-area";
  area.style.inlineSize = `${String(px)}px`;
  host.replaceChildren(area);
  return area;
}

function mountSeam(): HTMLElement {
  const seam = document.createElement("div");
  seam.className = "rail-seam";
  seam.setAttribute("role", "separator");
  seam.setAttribute("aria-label", "2h pause between turn 1 and turn 2");
  host.replaceChildren(seam);
  return seam;
}

function mountMarkerTime(areaPx: number): HTMLElement {
  const area = mountChatArea(areaPx);
  const marker = document.createElement("button");
  marker.className = "rail-marker";
  marker.type = "button";
  const time = document.createElement("span");
  time.className = "rail-marker-time";
  time.textContent = "1m 32s";
  marker.appendChild(time);
  area.appendChild(marker);
  return time;
}

/** A turn card with the two footer parts the reveal reads: a focusable ledger
 *  button, and the time slot beside it. */
function mountTurn(): { footer: HTMLElement; slot: HTMLElement } {
  const card = document.createElement("div");
  card.className = "turn";
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  const summary = document.createElement("button");
  summary.className = "turn-ledger-summary";
  summary.type = "button";
  summary.textContent = "2 files";
  footer.appendChild(summary);
  const slot = document.createElement("time");
  slot.className = "turn-elapsed";
  slot.textContent = "1m 32s";
  footer.appendChild(slot);
  card.appendChild(footer);
  host.replaceChildren(card);
  return { footer, slot };
}

/** The same builder's footer, under `.subagent-foot` where there is no `.turn`
 *  ancestor — which is what made site 2 permanent before the rest state was
 *  unscoped. */
function mountDelegate(): { footer: HTMLElement; slot: HTMLElement } {
  const card = document.createElement("div");
  card.className = "subagent-block";
  const foot = document.createElement("div");
  foot.className = "subagent-foot";
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  const summary = document.createElement("button");
  summary.className = "turn-ledger-summary";
  summary.type = "button";
  summary.textContent = "1 file";
  footer.appendChild(summary);
  const slot = document.createElement("time");
  slot.className = "turn-elapsed";
  slot.textContent = "12.0s";
  footer.appendChild(slot);
  foot.appendChild(footer);
  card.appendChild(foot);
  host.replaceChildren(card);
  return { footer, slot };
}

function pseudoContent(el: HTMLElement): string[] {
  return ["::before", "::after"].map((p) => getComputedStyle(el, p).content);
}

describe("the reveal gate is live in this browser", () => {
  it("matches any-hover, so every shipped-bundle case below measures the gated rule", () => {
    // The premise. Under `(any-hover: none)` the two contexts would be the same one
    // and every pair below would pass for one reason instead of two.
    expect(window.matchMedia(`(${REVEAL_QUERY})`).matches).toBe(true);
  });

  it("and the emulated bundle really drops that query", () => {
    // The other premise: without it every `withNoHoverCSS` case measures the shipped
    // cascade twice and the second half of each pair proves nothing.
    const shipped = style.textContent ?? "";
    const stripped = withoutHoverBlocks(shipped);
    expect(shipped).toContain(`@media (${REVEAL_QUERY})`);
    expect(stripped).not.toContain(`@media (${REVEAL_QUERY})`);
    expect(stripped.length).toBeLessThan(shipped.length);
  });
});

describe("site 1 — the rail's seam", () => {
  it("paints no text, and none through a pseudo-element either", () => {
    // The band's channel is its accessible name (`rail-labels.ts` seamLabel); the
    // builder's side is pinned in turn-rail.test.ts, and this is the stylesheet's.
    const seam = mountSeam();
    expect(seam.textContent).toBe("");
    expect(pseudoContent(seam).every((c) => c === "none" || c === "normal")).toBe(true);
  });

  it("paints none on a device with no hover either", async () => {
    await withNoHoverCSS(() => {
      const seam = mountSeam();
      expect(pseudoContent(seam).every((c) => c === "none" || c === "normal")).toBe(true);
    });
  });
});

describe("site 2 — the delegate footer's copy", () => {
  it("is invisible at rest", () => {
    expect(getComputedStyle(mountDelegate().slot).opacity).toBe("0");
  });

  it("is invisible at rest on a device with no hover", async () => {
    await withNoHoverCSS(() => {
      expect(getComputedStyle(mountDelegate().slot).opacity).toBe("0");
    });
  });
});

describe("site 3 — the turn footer's copy", () => {
  it("is invisible at rest", () => {
    expect(getComputedStyle(mountTurn().slot).opacity).toBe("0");
  });

  it("is invisible at rest on a device with no hover", async () => {
    await withNoHoverCSS(() => {
      expect(getComputedStyle(mountTurn().slot).opacity).toBe("0");
    });
  });
});

describe("site 4 — the marker's duration pill", () => {
  it("is invisible at rest where the gutter can hold it", () => {
    const time = mountMarkerTime(1200);
    const cs = getComputedStyle(time);
    expect(cs.display).not.toBe("none");
    expect(cs.opacity).toBe("0");
  });

  it("is not rendered below 70rem of chat area", () => {
    expect(getComputedStyle(mountMarkerTime(800)).display).toBe("none");
  });

  it("is not rendered at all on a device with no hover", async () => {
    // Unlike the footer's copy this one is WITHHELD rather than shown: there is no
    // gesture to reveal it with, and the turn card's own footer carries the value.
    await withNoHoverCSS(() => {
      expect(getComputedStyle(mountMarkerTime(1200)).display).toBe("none");
    });
  });
});

describe("the focus reveal reaches both hover contexts", () => {
  it("lifts the turn footer's copy", async () => {
    const { footer, slot } = mountTurn();
    footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
    await expectRevealed(slot);
  });

  it("lifts the turn footer's copy with no hover", async () => {
    await withNoHoverCSS(async () => {
      const { footer, slot } = mountTurn();
      footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
      await expectRevealed(slot);
    });
  });

  it("lifts the delegate footer's copy", async () => {
    const { footer, slot } = mountDelegate();
    footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
    await expectRevealed(slot);
  });

  it("lifts the delegate footer's copy with no hover", async () => {
    await withNoHoverCSS(async () => {
      const { footer, slot } = mountDelegate();
      footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
      await expectRevealed(slot);
    });
  });
});

describe("the durable channel", () => {
  it("gives a CLEAN turn with a gap a footer at all", () => {
    // `hasTurnSummary` is what decides the footer is BUILT, so widening `expandable`
    // alone leaves this turn with no ledger — hence no keyboard or touch path to the
    // gap, and `.rail-seam`'s aria-label as the only channel, which is AT-only.
    expect(hasTurnSummary({ sinceMs: GAP_MS })).toBe(true);
  });

  it("and that footer's disclosure names the gap", () => {
    const footer = buildTurnFooter({ sinceMs: GAP_MS });
    const summary = footer.querySelector<HTMLButtonElement>(".turn-ledger-summary");
    expect(summary?.disabled).toBe(false);
    const row = footer.querySelector(".turn-ledger-files > .turn-ledger-timings");
    expect(row?.textContent).toContain("Started 4h 0m after the previous turn");
  });

  it("omits the gap sentence when there is no predecessor in the window", () => {
    // Absent is a different fact from a gap of zero, so the row states nothing
    // rather than claiming the turn followed its predecessor immediately.
    const footer = buildTurnFooter({ elapsedMs: 12_000 });
    const row = footer.querySelector(".turn-ledger-timings");
    expect(row?.textContent).toBe("TimingsTook 12.0s");
  });

  it("carries a delegate's own duration in the delegate footer's disclosure", () => {
    // One shared builder, so the delegate footer gains the row with no second
    // mechanism — which matters because a leaf delegate's head is a link, so
    // `:focus-within` there navigates away instead of revealing.
    const footer = buildTurnFooter({ elapsedMs: 12_000 });
    expect(footer.querySelector(".turn-timings-value")?.textContent).toContain("Took 12.0s");
  });
});

/** A class name shaped like a duration slot. Deliberately wider than the audit's own
 *  vocabulary, because the guard's job is catching the NEXT slot rather than the ones
 *  the audit already names. */
const DURATION_SHAPED = /elapsed|duration|timings|dur|time|gap/iu;

/** The audited list, CLOSED. Every member carries the row that rules on it, so adding a
 *  class here without a ruling is visibly the wrong move. */
const RULED = new Map<string, string>([
  // Rows 2 and 3 are one class in two footers: the rest state is unscoped and only
  // the hover reveal is gated.
  ["turn-elapsed", "sites 2 and 3 — hover or focus only"],
  // Row 4, already correct and unchanged.
  ["rail-marker-time", "site 4 — hover or focus only, and only above 70rem"],
  // Row 6, the durable channel every other site depends on.
  ["turn-ledger-timings", "site 6 — the ledger's timings row"],
  ["turn-timings-label", "site 6 — the ledger's timings row"],
  ["turn-timings-value", "site 6 — the ledger's timings row"],
  // Named OUT of scope: each is a property of a tool call, a run or a workflow step,
  // and the reader opened that card to read exactly it.
  ["tool-duration", "out of scope — a tool call's own duration"],
  ["run-step-dur", "out of scope — the run card"],
  ["ev-dur", "out of scope — the exec view"],
  ["ev-d-dur", "out of scope — the exec view"],
  ["ev-tl-dur", "out of scope — the exec view"],
  // Row 8's ruling applied elsewhere: an absolute timestamp is not a duration.
  ["sched-time", "wall clock, not a duration"],
]);

describe("the closed list stays closed", () => {
  it("sweeps every stylesheet the MANIFEST declares", () => {
    // The manifest is READ, never restated: a count asserted here would be wrong the
    // next time a slice is added, and the sweep would silently stop covering it.
    const sheets = manifestSheets();
    expect(sheets.length).toBeGreaterThan(1);
    expect(sheets.filter((s) => s.css !== "")).toHaveLength(sheets.length);
  });

  it("finds no duration-bearing selector outside §4.2's list", () => {
    const unruled: string[] = [];
    for (const { name, css } of manifestSheets()) {
      const text = css.replace(/\/\*[\s\S]*?\*\//gu, " ");
      for (const [, cls] of text.matchAll(/\.(-?[A-Za-z_][\w-]*)/gu)) {
        if (cls !== undefined && DURATION_SHAPED.test(cls) && !RULED.has(cls)) {
          unruled.push(`${name}: .${cls}`);
        }
      }
    }
    expect([...new Set(unruled)].sort()).toEqual([]);
  });

  it("keeps every ruled class in the bundle, so the list cannot rot", () => {
    // The other direction: a member whose selector is gone is a stale entry that
    // would keep passing a class nobody declares.
    const all = manifestSheets()
      .map((s) => s.css.replace(/\/\*[\s\S]*?\*\//gu, " "))
      .join("\n");
    const declared = new Set([...all.matchAll(/\.(-?[A-Za-z_][\w-]*)/gu)].map(([, c]) => c));
    expect([...RULED.keys()].filter((c) => !declared.has(c))).toEqual([]);
  });
});
