// The outcome marks and the tab dot report the same thing on two surfaces, so one
// geometry is pinned across both: a settled row's mark takes the dot's footprint,
// and a failure takes the dot's own silhouette.
//
// Two defects this catches, neither visible to a type check. A mark drawn to fill
// the `ic-ui` BOX rather than the dot's footprint, which is what shipped — a solid
// 13.33px disc beside an 8px dot reporting the same state, and about three times
// the ink of the stroked glyph it replaces in that slot. And a second failure
// silhouette: `icons.ts` drew an apex-up triangle while `12-tabs.css` drew a
// diamond, so one reader learned two marks for one outcome.
//
// Every number is READ out of the stylesheets, so a token change either moves both
// surfaces or fails here.

import { describe, it, expect } from "vitest";
import { outcomeIcon } from "./icons.js";
import { allRules, loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** Every glyph's viewBox is 24 units square (`icons.ts` `svg`). */
const UNITS = 24;
const PX_PER_REM = 16;

const tokens = loadCSS("01-tokens.css");
const tabs = loadCSS("12-tabs.css");

/** A `--name: <n>rem` token, in rem. */
function remToken(name: string): number {
  const hit = new RegExp(`${name}:\\s*([\\d.]+)rem`).exec(tokens);
  if (hit?.[1] === undefined) {
    throw new Error(`no rem token ${name} in 01-tokens.css`);
  }
  return Number(hit[1]);
}

const ICON_REM = remToken("--icon-ui");
const DOT_REM = remToken("--dot-size");
const DOT_SM_REM = remToken("--dot-size-sm");

/** A painted length in rem, expressed in the viewBox units that paint it. */
function units(rem: number): number {
  return (rem / ICON_REM) * UNITS;
}

/** The subpath after the disc: `d` is disc-then-bar, and a `z` closes each. */
function barSubpath(glyph: string): string {
  const d = /d="([^"]+)"/.exec(glyph)?.[1];
  if (d === undefined) {
    throw new Error("glyph carries no path data");
  }
  const parts = d.split("z").filter((p) => p !== "");
  expect(parts, "disc plus exactly one bar").toHaveLength(2);
  return parts[1] ?? "";
}

function points(subpath: string): [number, number][] {
  const nums = [...subpath.matchAll(/-?[\d.]+/g)].map((m) => Number(m[0]));
  expect(nums.length % 2, "coordinates come in pairs").toBe(0);
  const out: [number, number][] = [];
  for (let i = 0; i < nums.length; i += 2) {
    out.push([nums[i] ?? 0, nums[i + 1] ?? 0]);
  }
  return out;
}

const dist = (a: [number, number], b: [number, number]): number =>
  Math.hypot(a[0] - b[0], a[1] - b[1]);

describe("the outcome mark and the tab dot are one vocabulary", () => {
  it("draws the disc at the tab dot's own diameter", () => {
    const r = units(DOT_REM) / 2;
    // The two-semicircle spelling `icons.ts` uses, re-derived rather than quoted.
    expect(outcomeIcon("ok")).toContain(
      `d="M12 ${12 - r}a${r} ${r} 0 1 0 0 ${2 * r}a${r} ${r} 0 1 0 0-${2 * r}z"`,
    );
  });

  it("draws a failure as the tab strip's diamond, by the same construction", () => {
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    // The strip's own three facts: the small token, a corner, and the 45 degrees.
    expect(failed.body).toContain("var(--dot-size-sm)");
    expect(failed.body).toContain("transform: rotate(45deg)");
    const cornerPx = Number(/border-radius:\s*([\d.]+)px/.exec(failed.body)?.[1]);
    expect(cornerPx, "the strip's diamond declares a corner radius").toBeGreaterThan(0);

    const side = units(DOT_SM_REM);
    const rx = units(cornerPx / PX_PER_REM);
    const offset = (UNITS - side) / 2;
    expect(outcomeIcon("fail")).toContain(
      `<rect x="${offset}" y="${offset}" width="${side}" height="${side}" rx="${rx}" transform="rotate(45 12 12)"`,
    );
  });

  it("gives the stop and the refusal one bar at two angles, both inside the rim", () => {
    const r = units(DOT_REM) / 2;

    // `warn` writes an axis-aligned bar as absolute-move then h/v.
    const stop = /^M([\d.]+) ([\d.]+)h([\d.]+)v([\d.]+)h-([\d.]+)$/.exec(
      barSubpath(outcomeIcon("warn")),
    );
    expect(stop, "the stop's bar is one h/v rectangle").not.toBeNull();
    const length = Number(stop?.[3]);
    const thickness = Number(stop?.[4]);
    expect(Number(stop?.[5]), "the bar closes on itself").toBe(length);

    // Inside the rim: the disc's half-chord at the bar's edge clears its half-length.
    const halfChord = Math.sqrt(r ** 2 - (thickness / 2) ** 2);
    expect(halfChord, "the stop's bar leaves the outline unbroken").toBeGreaterThan(length / 2);

    // `denied` is the SAME rectangle at 45 degrees, so its sides measure the same
    // and only its angle differs. The pair used to differ in length as well, which
    // made the angle the weaker of two channels.
    const corners = points(barSubpath(outcomeIcon("denied")));
    expect(corners, "the refusal's bar is four corners").toHaveLength(4);
    const sides = corners.map((p, i) => dist(p, corners[(i + 1) % 4] ?? p));
    expect(sides[0]).toBeCloseTo(thickness, 2);
    expect(sides[1]).toBeCloseTo(length, 2);
    expect(sides[2]).toBeCloseTo(thickness, 2);
    expect(sides[3]).toBeCloseTo(length, 2);

    const centre: [number, number] = [UNITS / 2, UNITS / 2];
    for (const c of corners) {
      expect(dist(c, centre), "the refusal's bar leaves the outline unbroken").toBeLessThan(r);
    }
  });
});

// The mark's INK and its SIZE, on the two surfaces that render the shared set
// beside their own state rings. Both were forks rather than decisions: the exec
// view took green from the status palette and red and yellow from the
// destructive-action one, so `ok` agreed with a tool card and `fail` and `warn`
// did not; and its in-flight rings measured 11px against the marks' 8, which made
// the state a reader is waiting on the largest thing in the pane.
describe("the mark reads one ink and one size wherever it renders", () => {
  const tools = loadCSS("14-tools.css");
  const exec = loadCSS("31-exec-view.css");
  // The THIRD surface reading the shared vocabulary: the composer band's run bar,
  // one line per live run. It copied `.ev-state`'s column deliberately rather than
  // `.run-step-glyph`'s, which hard-codes a length where this one reads the token,
  // so it belongs in both sweeps below or the copy can drift silently.
  const dock = loadCSS("26-dock.css");

  /** The single `color:` token a state's rule names. */
  function inkOf(css: string, selector: string): string {
    const rule = ruleContaining(css, selector, "top");
    const hit = /color:\s*var\((--c-[a-z-]+)\)/.exec(rule.body);
    if (hit?.[1] === undefined) {
      throw new Error(`${selector} names no --c-* colour`);
    }
    return hit[1];
  }

  const evState = (state: string): string =>
    `.ev-row[data-state="${state}"] > .ev-row-main > .ev-state`;

  it("spells one status trio on a tool card and in the exec tree", () => {
    expect(inkOf(tools, ".tool-icon.is-ok")).toBe("--c-green");
    expect(inkOf(tools, ".tool-icon.is-fail")).toBe("--c-red");
    expect(inkOf(tools, ".tool-icon.is-warn")).toBe("--c-yellow");
    expect(inkOf(tools, ".tool-icon.is-denied")).toBe("--c-yellow");

    expect(inkOf(exec, evState("ok"))).toBe("--c-green");
    expect(inkOf(exec, evState("fail"))).toBe("--c-red");
    expect(inkOf(exec, evState("warn"))).toBe("--c-yellow");
    expect(inkOf(exec, evState("input"))).toBe("--c-yellow");

    // The run bar renders FOUR states and no more (`rows()` keeps a run only while
    // its state is unfetched or live), so an unanswered ask is the one state there
    // that carries an ink at all, and a settled trio would be unreachable CSS.
    expect(inkOf(dock, '.run-bar-row[data-state="input"] .run-bar-glyph')).toBe("--c-yellow");
  });

  it("keeps the destructive-action palette out of the exec view entirely", () => {
    // Not just off the mark: the header glyph, the detail word, the timeline bar,
    // the alert accent and the failure box are all keyed on the same `data-state`,
    // so one of them left behind is the fork surviving somewhere a reader still
    // sees it. Comments are stripped, or this file's own explanation matches.
    const declarations = exec.replace(/\/\*[\s\S]*?\*\//g, " ");
    expect(declarations).not.toContain("--c-danger");
    expect(declarations).not.toContain("--c-warning");
  });

  it("sizes every ring in the state column off the dot token", () => {
    const rings = allRules(exec).filter(
      (r) => r.selector.includes(".ev-state::before") && r.body.includes("inline-size"),
    );
    // running + waiting share one rule, pending has its own.
    expect(rings.length, "the ring rules that size themselves").toBeGreaterThanOrEqual(2);
    for (const ring of rings) {
      expect(ring.body, ring.selector).toContain("inline-size: var(--dot-size)");
      expect(ring.body, ring.selector).toContain("block-size: var(--dot-size)");
    }
  });

  it("sizes the run bar's own rings off the same token", () => {
    const rings = allRules(dock).filter(
      (r) => r.selector.includes(".run-bar-glyph::before") && r.body.includes("inline-size"),
    );
    // running + waiting share the sizing rule; the two per-state rules that follow
    // it only set a border, so one sizing rule is the whole population.
    expect(rings.length, "the run bar's ring rules that size themselves").toBeGreaterThanOrEqual(1);
    for (const ring of rings) {
      expect(ring.body, ring.selector).toContain("inline-size: var(--dot-size)");
      expect(ring.body, ring.selector).toContain("block-size: var(--dot-size)");
    }
  });
});
