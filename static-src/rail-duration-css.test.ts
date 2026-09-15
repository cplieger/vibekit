// The site audit as an assertion, and the guard that keeps its list CLOSED.
//
// The rule: a TURN CARD's duration is reachable with no gesture — always in WORDS in
// its info panel, and in the footer's fact slot when the clock is the turn's LEAD
// fact. The RAIL's copy is the one gesture-gated duration, because it answers a
// different read: every turn's time in one column, which no footer can. So the sweep
// at the bottom is this file's durable half — an enumeration plus a scan that fails on
// a duration-shaped class nobody ruled on.
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

const { buildTurnFooter } = await import("./fundamentals/turn-footer.js");

const REVEAL_QUERY = "any-hover: hover";

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

/** A chat-area container at a stated inline size, which is what gates site 4. */
function mountChatArea(px: number): HTMLElement {
  const area = document.createElement("div");
  area.style.containerType = "inline-size";
  area.style.containerName = "chat-area";
  area.style.inlineSize = `${String(px)}px`;
  host.replaceChildren(area);
  return area;
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
    // WITHHELD rather than shown, unlike the footer's copy: there is no gesture to
    // reveal it with, and the turn card's own info panel carries the value on every
    // device — the PANEL rather than the row, because the row paints one fact and a
    // turn that did anything leads with that instead of its clock.
    await withNoHoverCSS(() => {
      expect(getComputedStyle(mountMarkerTime(1200)).display).toBe("none");
    });
  });
});

describe("the durable channel", () => {
  // What makes site 4's withholding acceptable: the value it hides is reachable by
  // pointer, keyboard, touch and assistive technology on the turn card. The INFO
  // PANEL is that channel and it is unconditional — the row's fact slot is not, since
  // it paints one fact and only leads with the clock on a turn that did nothing else
  // (`turn-fact-css.test.ts` measures the slot's own layout).
  it("carries a delegate's own duration in the delegate footer's panel", () => {
    // One shared builder, so the delegate footer gains the rows with no second
    // mechanism — which matters because a leaf delegate's head is a link, so a
    // hover-gated readout there would have no non-navigating way in.
    const footer = buildTurnFooter({ elapsedMs: 12_000 });
    const rows = [...footer.querySelectorAll(".turn-info-row")].map((r) => r.textContent);
    expect(rows).toContain("Wall clock12.0s");
  });
});

/** A class name shaped like a duration slot. Deliberately wider than the audit's own
 *  vocabulary, because the guard's job is catching the NEXT slot rather than the ones
 *  the audit already names. */
const DURATION_SHAPED = /elapsed|duration|timings|dur|time|gap/iu;

/** The audited list, CLOSED. Every member carries the row that rules on it, so adding a
 *  class here without a ruling is visibly the wrong move. */
const RULED = new Map<string, string>([
  // Site 1 was the rail's dashed pause BAND, `.rail-seam`. The band went in 2026-09 and
  // the pause REPORTING went with it — no rail clause, no footer row, no computation —
  // so there is no selector and no duration left to rule on.
  // Sites 2 and 3 are the footer's fact slot, `.turn-fact`, in two footers: painted at
  // rest with no gesture, and not duration-shaped by name, so it needs no entry.
  // Site 4, the one gesture-gated duration left, and the reason is at the rule.
  ["rail-marker-time", "site 4 — hover or focus only, and only above 70rem"],
  // A tool call's own duration was site 5 and is GONE: the label measured ACP
  // create-frame to terminal-frame wall clock, printed only past a 1000ms floor so
  // siblings disagreed about having one at all, and vanished on reload because
  // nothing persisted the client's own clock. `duration_ms` still travels — the turn
  // ledger and the delegate footer sum it — and no surface paints it per call.
  // Named OUT of scope: each is a property of a run or a workflow step, and the
  // reader opened that card to read exactly it.
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

  it("finds no duration-bearing selector outside the list", () => {
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
