// The audit walkers, driven against real fixtures in a real engine.
//
// Two things this file is for, and the second is the reason the module lives in
// the app at all. It pins what each walker REPORTS, so the number a ui-qa runner
// prints is a number a test asserted. And it pins the INJECTION CONTRACT: the
// same walker run through `pageSource` — the string `cdp.mjs` hands to
// `Runtime.evaluate` — has to answer exactly what the direct call answers, or the
// vitest gate and the live runner are testing two different programs.
//
// Every case is scoped to its own fixture by class prefix, never to a total,
// because the walkers scan the whole document and the runner's page is not empty.
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { WALKERS, pageSource } from "./walkers.js";

interface Row {
  readonly el: string;
  readonly h: number;
}
interface ControlHeight {
  readonly floor: number;
  readonly floorSource: string;
  readonly pointerCoarse: boolean;
  readonly scanned: number;
  readonly rows: number;
  readonly mismatches: readonly {
    readonly spread: number;
    readonly box: string;
    readonly count: number;
    readonly members: readonly Row[];
  }[];
  readonly undersize: readonly (Row & { readonly kind: string; readonly count: number })[];
  readonly viewportHidden: readonly (Row & { readonly count: number })[];
  readonly expanded: readonly (Row & { readonly kind: string; readonly count: number })[];
  readonly inlineExempt: readonly { readonly el: string; readonly count: number }[];
  readonly unmeasurable: readonly {
    readonly el: string;
    readonly why: string;
    readonly count: number;
  }[];
  readonly passes: readonly { readonly members: readonly Row[]; readonly count: number }[];
}
interface RadiusFinding {
  readonly verdict: string;
  readonly childRadius: number;
  readonly ideal: number | null;
  readonly insetH: number;
  readonly insetV: number;
  readonly parentRadius: number;
  readonly parentBorder: number;
  readonly corners: string;
  readonly childPaints: boolean;
  readonly child: string;
  readonly parent: string;
  readonly note: string;
  readonly count: number;
}
interface RadiusResult {
  readonly rounded: number;
  readonly pairs: number;
  readonly findings: readonly RadiusFinding[];
  readonly unmeasurable: readonly { readonly child: string; readonly why: string }[];
  readonly exempt: readonly {
    readonly child: string;
    readonly parent: string;
    readonly why: string;
  }[];
}
interface ContrastRow {
  readonly ratio: number;
  readonly floor: number;
  readonly painted: string;
  readonly bg: string;
  readonly sel: string;
  readonly count: number;
}
interface Contrast {
  readonly theme: string;
  readonly palette: string;
  readonly inkToken: string | null;
  readonly rootInk: string | null;
  readonly leafInk: string | null;
  readonly scanned: number;
  readonly findings: readonly (ContrastRow & {
    readonly size: number;
    readonly opacity: number;
    readonly opacityFrom: readonly string[];
    readonly bgFrom: string;
  })[];
  readonly dimmed: readonly {
    readonly ratio: number;
    readonly floor: number;
    readonly opacityFrom: readonly string[];
    readonly sel: string;
    readonly count: number;
  }[];
  readonly unresolved: readonly {
    readonly sel: string;
    readonly bgFrom: string;
    readonly count: number;
  }[];
  readonly inactive: readonly { readonly sel: string; readonly count: number }[];
  readonly backdrops: readonly string[];
  readonly passes: readonly ContrastRow[];
}
interface Reveal {
  readonly revealed: number;
  readonly passes: number;
}

/** Call a walker the way a test does: directly, with its real options. */
function run<T>(name: string, opts: Record<string, unknown> = {}): T {
  const fn = WALKERS[name];
  if (fn === undefined) {
    throw new Error(`no walker named ${name}`);
  }
  return (fn as unknown as (o: Record<string, unknown>) => T)(opts);
}

/** Call a walker the way a RUNNER does: through the serialized page source.
 *
 *  `new Function` rather than `eval` because that is the shape `Runtime.evaluate`
 *  has — one expression, its own scope, no access to this module's bindings. A
 *  helper the prelude forgot to emit therefore throws here exactly as it would in
 *  the page, which is the whole point of running the walkers this way. */
function runViaSource<T>(name: string, opts: Record<string, unknown> = {}): T {
  const src = pageSource(name, opts);
  return new Function(`return ${src}`)() as T;
}

let host: HTMLElement;
let sheet: HTMLStyleElement;

/** A fixed, opaque, top-of-stack host: `targetExtent` and `reveal` both hit-test
 *  through `elementFromPoint`, so a fixture behind the runner's own chrome would
 *  answer for the wrong element. */
function mount(html: string, css = ""): void {
  host.innerHTML = html;
  sheet.textContent = css;
}

beforeEach(() => {
  host = document.createElement("div");
  host.id = "walkers-fixture";
  host.style.cssText =
    "position:fixed;inset-block-start:0;inset-inline-start:0;z-index:2147483646;background:#fff";
  sheet = document.createElement("style");
  document.body.append(sheet, host);
});

afterEach(() => {
  // MEASURED on vitest 5.0.0: Browser Mode isolates per FILE and does NOT clear
  // `document.body` between tests — a module-scope host stays connected for every
  // case and per-test appends accumulate. So this teardown is load-bearing rather
  // than tidy: these walkers scan the WHOLE document, so a fixture left behind is
  // a fixture the next case finds. It cost one red run, where an earlier test's
  // `.fx-square` satisfied a later test asserting that class produced nothing.
  host.remove();
  sheet.remove();
  document.documentElement.style.removeProperty("--hit-floor");
  document.documentElement.style.removeProperty("--c-text-primary");
});

/** Does this `describeEl` descriptor carry exactly this class?
 *
 *  A substring test is not good enough and cost a red run: `fx-slid` is a
 *  substring of every fixture class starting with it, so a two-fixture case
 *  silently matched both. `describeEl` writes `tag#id.a.b[role=x]`, so splitting
 *  on the dot yields the class tokens. */
const hasClass = (descriptor: string, cls: string): boolean => descriptor.split(".").includes(cls);

const mismatchesFor = (
  r: ControlHeight,
  cls: string,
): readonly ControlHeight["mismatches"][number][] =>
  r.mismatches.filter((m) => hasClass(m.box, cls));
const undersizeFor = (
  r: ControlHeight,
  cls: string,
): readonly ControlHeight["undersize"][number][] => r.undersize.filter((u) => hasClass(u.el, cls));
const findingFor = (r: Contrast, cls: string): Contrast["findings"][number] | undefined =>
  r.findings.find((f) => hasClass(f.sel, cls));
const findingsFor = (r: RadiusResult, cls: string): readonly RadiusFinding[] =>
  r.findings.filter((f) => hasClass(f.child, cls));

describe("control-height", () => {
  it("reports a row whose controls disagree, with the spread and both members", () => {
    mount(
      `<div class="fx-mixed" style="display:flex;gap:4px">
         <button class="fx-tall" style="height:32px;width:60px">a</button>
         <button class="fx-short" style="height:24px;width:60px">b</button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    const hits = mismatchesFor(r, "fx-mixed");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.spread).toBe(8);
    expect(hits[0]?.members.map((m) => m.h).sort()).toEqual([24, 32]);
  });

  it("reports an agreeing row as a pass and files no mismatch for it", () => {
    mount(
      `<div class="fx-agree" style="display:flex;gap:4px">
         <button style="height:32px;width:60px">a</button>
         <button style="height:32px;width:60px">b</button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(mismatchesFor(r, "fx-agree")).toHaveLength(0);
    expect(r.passes.some((p) => p.members.every((m) => m.h === 32) && p.members.length === 2)).toBe(
      true,
    );
  });

  it("stays silent on a spread inside the tolerance", () => {
    mount(
      `<div class="fx-tol" style="display:flex;gap:4px">
         <button style="height:32px;width:60px">a</button>
         <button style="height:31px;width:60px">b</button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(mismatchesFor(r, "fx-tol")).toHaveLength(0);
  });

  it("splits one flex parent into a row per BAND, so a wrapped line is its own row", () => {
    mount(
      `<div class="fx-wrap" style="display:flex;flex-wrap:wrap;width:130px;gap:0;align-items:flex-start">
         <button style="height:32px;width:60px">a</button>
         <button style="height:30px;width:60px">b</button>
         <button style="height:26px;width:60px">c</button>
         <button style="height:26px;width:60px">d</button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    // The SPREAD is what proves the split: line 1 disagrees by 2 and line 2
    // agrees, so a walker that folded both lines into one band would report the
    // same single finding at a spread of 6.
    const hits = mismatchesFor(r, "fx-wrap");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.spread).toBe(2);
    expect(r.passes.some((p) => p.members.length === 2 && p.members.every((m) => m.h === 26))).toBe(
      true,
    );
  });

  it("refuses to measure a small control something else answers for at its centre", () => {
    // A control inside a closed `<details>` reports a client rect — Chromium hides
    // that content with `content-visibility` on `::details-content` rather than by
    // removing it from layout — while the hit test answers the `<summary>` painted
    // over it. Reading the painted box as the target instead reported 12 of these as
    // undersize by exactly the width of their own expander.
    mount(
      `<details class="fx-closed"><summary style="height:24px">s</summary>
         <button class="fx-buriedbtn" style="height:12px;width:12px"></button>
       </details>`,
    );
    const buried = host.querySelector(".fx-buriedbtn");
    // The premise: it really does report a box, or this case proves nothing.
    expect(buried?.getClientRects().length).toBeGreaterThan(0);

    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(undersizeFor(r, "fx-buriedbtn")).toHaveLength(0);
    expect(r.unmeasurable.find((u) => hasClass(u.el, "fx-buriedbtn"))?.why).toContain(
      "answers at its centre",
    );
  });

  it("reports a control under the floor whose TARGET is under it too", () => {
    mount(`<button class="fx-tiny" style="height:12px;width:12px"></button>`);
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    const hits = undersizeFor(r, "fx-tiny");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.h).toBeLessThan(24);
  });

  it("does NOT report a small control that grew its target with an expander", () => {
    mount(
      `<button class="fx-expanded" style="height:3px;width:60px;position:relative;border:0;padding:0"></button>`,
      `.fx-expanded::after{content:"";position:absolute;inset-inline:0;inset-block:-11px;}`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(undersizeFor(r, "fx-expanded")).toHaveLength(0);
  });

  it("takes a deliberately-small control OUT of its row and reports it as expanded", () => {
    // The two live rows this closes: a control painted under the floor that grows
    // its target with an expander has declared itself visually small, so it is not
    // claiming the row's height and a sibling at the tier is not a mismatch.
    mount(
      `<div class="fx-chiprow" style="display:flex;gap:4px;align-items:center">
         <button class="fx-tier" style="height:24px;width:24px"></button>
         <button class="fx-chip" style="height:20px;width:60px;position:relative;border:0;padding:0"></button>
       </div>`,
      `.fx-chip::after{content:"";position:absolute;inset-inline:0;inset-block:-2px;}`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(mismatchesFor(r, "fx-chiprow")).toHaveLength(0);
    expect(r.expanded.some((e) => hasClass(e.el, "fx-chip"))).toBe(true);
    // The tier-sized sibling is untouched and still a participant.
    expect(r.expanded.some((e) => hasClass(e.el, "fx-tier"))).toBe(false);
  });

  it("still reports the SAME row when the small control grows no target", () => {
    // The negative half, and what keeps the rule the row rule exists for: a
    // control merely short is a mismatch, exactly as before.
    mount(
      `<div class="fx-shortrow" style="display:flex;gap:4px;align-items:center">
         <button class="fx-tier2" style="height:24px;width:24px"></button>
         <button class="fx-bare" style="height:20px;width:60px;border:0;padding:0"></button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(mismatchesFor(r, "fx-shortrow")).toHaveLength(1);
    expect(mismatchesFor(r, "fx-shortrow")[0]?.spread).toBe(4);
    expect(r.expanded.some((e) => hasClass(e.el, "fx-bare"))).toBe(false);
  });

  it("still checks an EXPANDED control's target, so a broken expander is caught", () => {
    // Being out of the row does not buy it out of the floor: the undersize half
    // runs for both populations, so an expander that reaches nothing is reported.
    mount(
      `<button class="fx-fakeexp" style="height:10px;width:10px;position:relative;border:0;padding:0"></button>`,
      // Absolutely positioned, so it reads as the idiom, but it grows nothing.
      `.fx-fakeexp::after{content:"";position:absolute;inset:0;}`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.expanded.some((e) => hasClass(e.el, "fx-fakeexp"))).toBe(true);
    expect(undersizeFor(r, "fx-fakeexp")).toHaveLength(1);
  });

  it("keeps a control AT the floor in its row even when it carries an expander", () => {
    // The exemption is for a control painted UNDER the floor. One already at the
    // tier is claiming the row's height whatever pseudo-elements it has, so a
    // taller sibling is still a mismatch.
    mount(
      `<div class="fx-atfloor" style="display:flex;gap:4px;align-items:center">
         <button class="fx-big" style="height:32px;width:60px"></button>
         <button class="fx-exactly" style="height:24px;width:60px;position:relative;border:0;padding:0"></button>
       </div>`,
      `.fx-exactly::after{content:"";position:absolute;inset:0;}`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(mismatchesFor(r, "fx-atfloor")).toHaveLength(1);
    expect(mismatchesFor(r, "fx-atfloor")[0]?.spread).toBe(8);
    expect(r.expanded.some((e) => hasClass(e.el, "fx-exactly"))).toBe(false);
  });

  it("requires the pseudo-element to be POSITIONED, so a glyph does not exempt", () => {
    // A decorative `::after` in flow grows no target, so a control carrying one is
    // an ordinary short control and its row is still a finding.
    mount(
      `<div class="fx-decor" style="display:flex;gap:4px;align-items:center">
         <button class="fx-tier3" style="height:24px;width:24px"></button>
         <button class="fx-glyphonly" style="height:20px;width:60px;border:0;padding:0"></button>
       </div>`,
      `.fx-glyphonly::after{content:"x";}`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.expanded.some((e) => hasClass(e.el, "fx-glyphonly"))).toBe(false);
    expect(mismatchesFor(r, "fx-decor")).toHaveLength(1);
  });

  it("reports a stylesheet-hidden control as viewport-hidden rather than measuring it", () => {
    mount(`<button class="fx-gone" style="display:none">x</button>`);
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.viewportHidden.some((v) => hasClass(v.el, "fx-gone"))).toBe(true);
    expect(undersizeFor(r, "fx-gone")).toHaveLength(0);
  });

  it("refuses to measure a control under a rotated ancestor, and names the ancestor", () => {
    mount(
      `<div class="fx-rot" style="transform:rotate(12deg)">
         <button class="fx-tilted" style="height:9px;width:9px"></button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    const hit = r.unmeasurable.find((u) => hasClass(u.el, "fx-tilted"));
    expect(hit?.why).toContain("fx-rot");
    expect(undersizeFor(r, "fx-tilted")).toHaveLength(0);
  });

  it("refuses to measure under a scaled ancestor, which Chromium keeps OFF `transform`", () => {
    mount(
      `<div class="fx-scaled" style="scale:1.4">
         <button class="fx-grown" style="height:9px;width:9px"></button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.unmeasurable.find((u) => hasClass(u.el, "fx-grown"))?.why).toContain("fx-scaled");
  });

  it("measures under a TRANSLATED ancestor, in both spellings", () => {
    // Both, because Chromium keeps the standalone `translate` property OFF the
    // computed `transform` — so one fixture exercises the matrix path and the
    // other the property path, and a rule that rejected either would be caught.
    mount(
      `<div class="fx-moved" style="transform:translate(6px, 4px)">
         <button class="fx-slid" style="height:9px;width:9px"></button>
       </div>
       <div class="fx-moved2" style="translate:6px 4px">
         <button class="fx-slidprop" style="height:9px;width:9px"></button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.unmeasurable.some((u) => hasClass(u.el, "fx-slid"))).toBe(false);
    expect(undersizeFor(r, "fx-slid")).toHaveLength(1);
    expect(undersizeFor(r, "fx-slidprop")).toHaveLength(1);
  });

  it("names the two unmeasurable reasons apart, so a runner prints the right one", () => {
    // The runners print this string verbatim. They used to hardcode "scaled
    // ancestor" in front of it, which read as a lie over an off-screen control —
    // the walker owns the whole sentence for that reason.
    mount(
      `<div class="fx-far" style="position:fixed;inset-block-start:4000px;inset-inline-start:0">
         <button class="fx-away" style="height:9px;width:9px"></button>
       </div>
       <div class="fx-near" style="transform:scale(1.3)">
         <button class="fx-warped" style="height:9px;width:9px"></button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.unmeasurable.find((u) => hasClass(u.el, "fx-away"))?.why).toContain(
      "outside the viewport",
    );
    expect(r.unmeasurable.find((u) => hasClass(u.el, "fx-warped"))?.why).toContain(
      "transformed ancestor",
    );
  });

  it("excludes a target IN A SENTENCE, whatever its tag, and says it did", () => {
    // Both spellings the app ships: a prose `a[href]`, and the inline-flex BUTTON
    // `linkify.ts` emits for a file path. Keying on the tag missed the second and
    // reported 51 undersize findings against a chip whose stylesheet documents the
    // exception.
    mount(
      `<p>text <a class="fx-inline" href="#x">link</a> more</p>
       <pre>build failed at <button class="fx-chipinprose" style="display:inline-flex;height:16px;font-size:10px">a.go:1</button> and stopped</pre>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    for (const cls of ["fx-inline", "fx-chipinprose"]) {
      expect(undersizeFor(r, cls)).toHaveLength(0);
      expect(r.viewportHidden.some((v) => hasClass(v.el, cls))).toBe(false);
      expect(r.inlineExempt.some((e) => hasClass(e.el, cls))).toBe(true);
    }
  });

  it("keeps an inline control with NO surrounding text in the population", () => {
    // The text sibling is the "in a sentence" half. Without it an inline-level
    // control is an ordinary control that happens to be inline-level, which is most
    // of this app's buttons — so it is still floored and still in its row.
    mount(
      `<div class="fx-nolinetext" style="display:flex">
         <button class="fx-loneinline" style="display:inline-flex;height:12px;width:12px"></button>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.inlineExempt.some((e) => hasClass(e.el, "fx-loneinline"))).toBe(false);
    expect(undersizeFor(r, "fx-loneinline")).toHaveLength(1);
  });

  it("requires INLINE-LEVEL display, so a block control beside text is still floored", () => {
    // The other half of the predicate. Surrounding text alone is not the exception —
    // a block-level control is not constrained by a line height, so it owns its own
    // reach and the floor applies.
    mount(
      `<div class="fx-blockintext">some text
         <button class="fx-blocktiny" style="display:block;height:12px;width:12px"></button>
         more text</div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.inlineExempt.some((e) => hasClass(e.el, "fx-blocktiny"))).toBe(false);
    expect(undersizeFor(r, "fx-blocktiny")).toHaveLength(1);
  });

  it("keeps a labelled FIELD in its row even though the standard exempts its floor", () => {
    // An `<input>` beside its label is inline-level with text around it, so WCAG
    // exempts its target — and the app's one-height-per-row rule still applies,
    // because that rule is about what a reader SEES and is the population the rule
    // was written for. Two different questions, so two different gates.
    mount(
      `<div class="fx-formrow" style="display:flex;gap:4px;align-items:center">
         <button style="height:32px;width:60px">save</button>
         <label>Name <input class="fx-shortfield" style="height:22px;width:80px" /></label>
       </div>`,
    );
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.inlineExempt.some((e) => hasClass(e.el, "fx-shortfield"))).toBe(true);
    expect(undersizeFor(r, "fx-shortfield")).toHaveLength(0);
    expect(mismatchesFor(r, "fx-formrow")).toHaveLength(1);
  });

  it("takes the floor from `--hit-floor` when the page declares one", () => {
    // On `:root`, which is where `01-tokens.css` declares it and the only place
    // the probe can inherit it from: `tokenPx` measures a box it appends to
    // `document.body`, so a token declared on some inner element is invisible.
    document.documentElement.style.setProperty("--hit-floor", "44px");
    mount(`<button class="fx-floored" style="height:30px;width:60px">x</button>`);
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    expect(r.floorSource).toBe("--hit-floor");
    expect(r.floor).toBe(44);
  });

  it("prefers an explicit floor over every token", () => {
    const r = run<ControlHeight>("control-height", { tol: 1.5, floor: 60 });
    expect(r.floorSource).toBe("--floor");
    expect(r.floor).toBe(60);
  });

  it("dedupes identical findings into one row carrying a count", () => {
    const row = `<div class="fx-dupe" style="display:flex">
        <button style="height:32px;width:60px">a</button>
        <button style="height:24px;width:60px">b</button>
      </div>`;
    mount(row + row);
    const r = run<ControlHeight>("control-height", { tol: 1.5 });
    const hits = mismatchesFor(r, "fx-dupe");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.count).toBe(2);
  });
});

describe("radius", () => {
  // 12px parent radius, 1px border, 4px inset -> ideal child radius 7.
  //
  // The child is 40px tall on purpose. The stadium exemption fires at half the
  // child's SHORT side, so on a 20px-tall child every radius from 9.25px up is
  // read as a deliberate shape and the too-round case becomes unreachable —
  // measured, and it is the fixture that was wrong rather than the rule.
  const nest = (childRadius: string, cls: string): string =>
    `<div class="fx-parent" style="border:1px solid #000;border-radius:12px;padding:4px;width:80px">
       <div class="${cls}" style="border-radius:${childRadius};background:#eee;height:40px"></div>
     </div>`;

  it("passes a child at the concentric radius", () => {
    mount(nest("7px", "fx-ok"));
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-ok")).toHaveLength(0);
  });

  it("calls a child short of the concentric radius too-square, and states the ideal", () => {
    mount(nest("2px", "fx-undersquare"));
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    const hits = findingsFor(r, "fx-undersquare");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.verdict).toBe("too-square");
    expect(hits[0]?.ideal).toBe(7);
    expect(hits[0]?.corners).toBe("tl,tr,br,bl");
  });

  it("calls a child past the concentric radius too-round", () => {
    mount(nest("11px", "fx-round"));
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-round")[0]?.verdict).toBe("too-round");
  });

  it("exempts a stadium rather than judging it as a nesting", () => {
    mount(nest("999px", "fx-stadium"));
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-stadium")).toHaveLength(0);
    expect(r.exempt.some((e) => hasClass(e.child, "fx-stadium") && e.why.includes("stadium"))).toBe(
      true,
    );
  });

  it("measures the stadium threshold against the CHILD's short side, not the parent's", () => {
    // Same 11px radius, same parent, two child heights. On the short child the
    // radius is already half the box, so it is a shape; on the tall one it is a
    // nesting decision and gets judged. Anything reading the parent's box here
    // would answer the same for both.
    // One parent each: two children stacked in one parent do not share a
    // vertical inset, so the second would read anisotropic rather than round.
    mount(
      `<div class="fx-parent" style="border:1px solid #000;border-radius:12px;padding:4px;width:80px">
         <div class="fx-shortkid" style="border-radius:11px;background:#eee;height:20px"></div>
       </div>
       <div class="fx-parent" style="border:1px solid #000;border-radius:12px;padding:4px;width:80px">
         <div class="fx-tallkid" style="border-radius:11px;background:#eee;height:40px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-shortkid")).toHaveLength(0);
    expect(r.exempt.some((e) => hasClass(e.child, "fx-shortkid"))).toBe(true);
    expect(findingsFor(r, "fx-tallkid")[0]?.verdict).toBe("too-round");
  });

  it("exempts a child sitting flush inside a clipping parent, and only a CLIPPING one", () => {
    // The negative half is what makes this behavioral: flushness alone is not
    // the exemption, so the same child in a parent that does not clip is judged.
    mount(
      `<div class="fx-clip" style="border-radius:12px;overflow:hidden;padding:0;width:80px">
         <div class="fx-flush" style="border-radius:3px;background:#eee;height:20px"></div>
       </div>
       <div class="fx-noclip" style="border-radius:12px;padding:0;width:80px">
         <div class="fx-unclipped" style="border-radius:3px;background:#eee;height:20px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-flush")).toHaveLength(0);
    expect(r.exempt.some((e) => hasClass(e.child, "fx-flush") && e.why.includes("clipping"))).toBe(
      true,
    );
    expect(findingsFor(r, "fx-unclipped")).toHaveLength(1);
    expect(findingsFor(r, "fx-unclipped")[0]?.ideal).toBe(12);
  });

  it("calls an anisotropic inset unsatisfiable and offers NO ideal radius", () => {
    mount(
      `<div class="fx-aniso" style="border-radius:12px;padding:8px 4px;width:80px;background:#ddd">
         <div class="fx-skewed" style="border-radius:6px;background:#eee;height:20px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    const hits = findingsFor(r, "fx-skewed");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.verdict).toBe("anisotropic");
    expect(hits[0]?.ideal).toBeNull();
    expect(hits[0]?.note).toContain("unsatisfiable");
  });

  it("charges the corner's OWN border, so a one-sided rule is anisotropic", () => {
    // Equal insets, unequal borders: the concentric rule subtracts inset PLUS
    // that edge's border, so this corner is 5 in vertically and 8 horizontally.
    mount(
      `<div class="fx-rail" style="border-block:1px solid #000;border-inline-start:4px solid #000;border-inline-end:4px solid #000;border-radius:12px;padding:4px;width:80px">
         <div class="fx-railchild" style="border-radius:7px;background:#eee;height:20px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    const hits = findingsFor(r, "fx-railchild");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.verdict).toBe("anisotropic");
    expect(hits[0]?.note).toContain("5 and 8");
  });

  it("reports whether the child paints at rest, so a hover-only fill is not exempted", () => {
    mount(
      `<div class="fx-parent" style="border-radius:12px;padding:4px;width:80px">
         <div class="fx-bare" style="border-radius:2px;height:20px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    const hits = findingsFor(r, "fx-bare");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.childPaints).toBe(false);
  });

  it("refuses to measure a rounded child under a rotated ancestor", () => {
    mount(
      `<div class="fx-rrot" style="transform:rotate(9deg)">
         ${nest("2px", "fx-rtilted")}
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-rtilted")).toHaveLength(0);
    expect(r.unmeasurable.find((u) => hasClass(u.child, "fx-rtilted"))?.why).toContain("fx-rrot");
  });

  it("judges only the corners where the child actually meets the parent's arc", () => {
    // A narrow child at a wide parent's leading edge. Its TRAILING corners sit
    // 356px in, well past where the parent's 6px arc ends, so they are not a
    // nesting and must not be judged — without that gate both come back
    // "anisotropic" and this shape is most of a real page.
    mount(
      // `display:flex` on the parents is load-bearing: a block parent puts an
      // inline-block child on a text baseline, which leaves ~1px of descender
      // space under it and makes the bottom inset differ from the top by more
      // than the tolerance — a real asymmetry the walker is right to report, and
      // not the thing this case is about.
      `<div class="fx-wide" style="border-radius:6px;padding:4px;width:400px;background:#ddd;display:flex">
         <button class="fx-lead-ok" style="border-radius:2px;width:40px;height:20px"></button>
       </div>
       <div class="fx-wide" style="border-radius:6px;padding:4px;width:400px;background:#ddd;display:flex">
         <button class="fx-lead-bad" style="border-radius:5px;width:40px;height:20px"></button>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-lead-ok")).toHaveLength(0);
    // The wrong one is still caught, and names ONLY the leading pair.
    const bad = findingsFor(r, "fx-lead-bad");
    expect(bad).toHaveLength(1);
    expect(bad[0]?.corners).toBe("tl,bl");
    expect(bad[0]?.verdict).toBe("too-round");
  });

  it("does not judge a corner the child OVERFLOWS, which a negative inset marks", () => {
    // The app's expandable pill card opens upward and sits above its parent's top
    // edge, so its top insets are negative. A child outside the parent's box is
    // not nested in that corner, so only the corners it is actually inside count.
    // The child's radius stays well under half its short side on purpose: at 12px
    // on a 20px-tall box the stadium exemption fires first and the gate is never
    // reached, which is how the first version of this case passed vacuously.
    mount(
      `<div class="fx-overflowed" style="border-radius:12px;padding:4px;width:80px;background:#ddd;position:relative">
         <div class="fx-escapee" style="border-radius:4px;background:#eee;height:40px;position:absolute;inset-block-start:-30px;inset-inline:4px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-escapee")).toHaveLength(0);
  });

  it("examines only radius-BEARING children, which is the rule's own scope", () => {
    // A square child inside a rounded parent is the ordinary case, not a
    // violation, so it never enters the pair set at all.
    mount(
      `<div class="fx-sqparent" style="border-radius:12px;padding:4px;width:80px;background:#ddd">
         <div class="fx-square" style="border-radius:0;background:#eee;height:20px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    expect(findingsFor(r, "fx-square")).toHaveLength(0);
    expect(r.exempt.some((e) => hasClass(e.child, "fx-square"))).toBe(false);
  });

  it("ignores a child whose nearest rounded ancestor has no radius on that corner", () => {
    mount(
      `<div class="fx-topless" style="border-radius:0 0 12px 12px;padding:4px;width:80px;background:#ddd">
         <div class="fx-half" style="border-radius:7px 7px 0 0;background:#eee;height:20px"></div>
       </div>`,
    );
    const r = run<RadiusResult>("radius", { tol: 0.75 });
    // Only the two bottom corners are a pair; the child is square there, so the
    // finding names exactly those and never the top pair.
    expect(findingsFor(r, "fx-half")[0]?.corners).toBe("br,bl");
  });
});

describe("reveal", () => {
  it("gives a stylesheet-hidden control a box", () => {
    mount(
      `<div class="fx-panel" style="display:none">
         <button class="fx-buried" style="height:32px;width:60px">x</button>
       </div>`,
    );
    const before = host.querySelector(".fx-buried")?.getClientRects().length;
    expect(before).toBe(0);

    const r = run<Reveal>("reveal");
    expect(r.revealed).toBeGreaterThanOrEqual(1);
    expect(host.querySelector(".fx-buried")?.getClientRects().length).toBeGreaterThan(0);
  });

  it("reaches a control whose own rule hides it INSIDE a hidden container", () => {
    // Two passes' worth of work in one call: unhiding the container is what makes
    // the child's own `display: none` reachable.
    mount(
      `<div class="fx-outer" style="display:none">
         <button class="fx-inner">x</button>
       </div>`,
      `.fx-inner{display:none}`,
    );
    const r = run<Reveal>("reveal");
    expect(r.passes).toBeGreaterThanOrEqual(2);
    expect(host.querySelector(".fx-inner")?.getClientRects().length).toBeGreaterThan(0);
  });

  it("leaves a CLOSED dialog untouched and counts nothing for it", () => {
    // The COUNT is the assertion that can fail. A box assertion cannot: revert
    // lands on the UA `dialog:not([open])` rule, so the dialog stays hidden with
    // the skip or without it — measured, and it is why the skip's stated reason
    // is an honest `revealed` rather than modality.
    mount(`<dialog class="fx-dlg"><button class="fx-in-dlg">x</button></dialog>`);
    const r = run<Reveal>("reveal");
    expect(r).toEqual({ revealed: 0, passes: 1 });
    expect(host.querySelector<HTMLDialogElement>(".fx-dlg")?.open).toBe(false);
    expect(host.querySelector(".fx-in-dlg")?.getClientRects().length).toBe(0);
  });

  it("is idempotent: a second call reveals nothing and stops after one pass", () => {
    mount(`<div style="display:none"><button>x</button></div>`);
    run<Reveal>("reveal");
    const second = run<Reveal>("reveal");
    expect(second).toEqual({ revealed: 0, passes: 1 });
  });

  it("counts an element it CANNOT unhide once, rather than once per pass", () => {
    // A `<datalist>` is hidden by the UA on its tag, and `display: revert` rolls
    // back to exactly that — measured. So it is the witness for the marker: with
    // no marker the loop re-touches it every pass and reports 3 reveals over 3
    // passes for one element that never moved.
    mount(`<datalist class="fx-dl"><option>x</option></datalist>`);
    const r = run<Reveal>("reveal");
    expect(r.revealed).toBe(1);
    expect(r.passes).toBe(2);
    expect(host.querySelector(".fx-dl")?.getClientRects().length).toBe(0);
  });
});

describe("contrast", () => {
  it("passes black on white and fails grey on white, at the 4.5:1 floor", () => {
    mount(
      `<div class="fx-c-ok" style="background:#fff;color:#000;font-size:14px">readable</div>
       <div class="fx-c-bad" style="background:#fff;color:#999;font-size:14px">too pale</div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-bad")?.floor).toBe(4.5);
    expect(findingFor(r, "fx-c-bad")?.ratio).toBeLessThan(4.5);
    expect(findingFor(r, "fx-c-ok")).toBeUndefined();
    expect(r.passes.some((p) => hasClass(p.sel, "fx-c-ok"))).toBe(true);
  });

  it("reports the ratio to two decimals, against the published value for #767676", () => {
    // #767676 on white is WCAG's own worked boundary case: 4.54:1, the darkest grey
    // that passes. A ratio computed with the wrong luminance curve misses it.
    mount(`<div class="fx-c-edge" style="background:#fff;color:#767676;font-size:14px">x</div>`);
    const r = run<Contrast>("contrast", {});
    expect(r.passes.find((p) => hasClass(p.sel, "fx-c-edge"))?.ratio).toBe(4.54);
  });

  it("weights the channels per WCAG, which only a coloured ink can prove", () => {
    // Every grey fixture in this file passes with the channels weighted equally,
    // because R=G=B cancels the weights. These are the published ratios on white:
    // pure blue 8.59:1 (blue carries 0.0722 of the luminance) and pure green 1.37:1
    // (green carries 0.7152). Equal thirds put BOTH at 2.74.
    mount(
      `<div class="fx-c-blue" style="background:#fff;color:#0000ff;font-size:14px">blue</div>
       <div class="fx-c-green" style="background:#fff;color:#00ff00;font-size:14px">green</div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(r.passes.find((p) => hasClass(p.sel, "fx-c-blue"))?.ratio).toBe(8.59);
    expect(findingFor(r, "fx-c-green")?.ratio).toBe(1.37);
  });

  it("takes the 3:1 floor for large text, by SIZE and by bold WEIGHT", () => {
    mount(
      `<div class="fx-c-big" style="background:#fff;color:#949494;font-size:24px">big</div>
       <div class="fx-c-bold" style="background:#fff;color:#949494;font-size:19px;font-weight:700">bold</div>
       <div class="fx-c-small" style="background:#fff;color:#949494;font-size:19px;font-weight:400">small</div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(r.passes.find((p) => hasClass(p.sel, "fx-c-big"))?.floor).toBe(3);
    expect(r.passes.find((p) => hasClass(p.sel, "fx-c-bold"))?.floor).toBe(3);
    // Same ink, same surface, one point under the bold threshold: 4.5 applies and
    // it fails. That pair is what proves the floor is chosen rather than constant.
    expect(findingFor(r, "fx-c-small")?.floor).toBe(4.5);
  });

  it("composites a SEMI-TRANSPARENT surface onto the layer beneath it", () => {
    // The whole reason a rendered check exists beside the token gate: the ink is
    // measured against #808080, which no declaration in the page states.
    mount(
      `<div class="fx-c-under" style="background:#000">
         <div class="fx-c-wash" style="background:rgba(255,255,255,0.5);color:#fff;font-size:14px">washed</div>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    const hit = findingFor(r, "fx-c-wash") ?? r.passes.find((p) => hasClass(p.sel, "fx-c-wash"));
    expect(hit?.bg).toBe("#808080");
  });

  it("resolves an oklch surface and ink, which computed style hands back unparsed", () => {
    mount(
      `<div class="fx-c-oklch" style="background:oklch(1 0 0);color:oklch(0 0 0);font-size:14px">x</div>`,
    );
    const r = run<Contrast>("contrast", {});
    const hit = r.passes.find((p) => hasClass(p.sel, "fx-c-oklch"));
    expect(hit?.bg).toBe("#ffffff");
    expect(hit?.painted).toBe("#000000");
    expect(hit?.ratio).toBe(21);
  });

  it("falls through a transparent chain to the UA's white canvas, and says so", () => {
    // Appended to the BODY rather than the fixture host, which is opaque so the hit
    // tests elsewhere in this file resolve — inside it no chain can be transparent,
    // and the walker correctly names the host instead.
    mount("");
    const bare = document.createElement("div");
    bare.className = "fx-c-bare";
    bare.style.cssText = "color:#999;font-size:14px";
    bare.textContent = "x";
    document.body.append(bare);
    try {
      const r = run<Contrast>("contrast", {});
      const hit = findingFor(r, "fx-c-bare");
      expect(hit?.bg).toBe("#ffffff");
      expect(hit?.bgFrom).toBe("the UA canvas");
    } finally {
      bare.remove();
    }
  });

  it("folds an ancestor OPACITY into the ink, which no static gate can see", () => {
    // Black on white at 0.3 is grey on white: the token graph reads 21:1 and the
    // reader sees 3.5:1.
    mount(
      `<div class="fx-c-dimbox" style="background:#fff;opacity:0.3">
         <div class="fx-c-dimtext" style="color:#000;font-size:14px">faint</div>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    const hit = findingFor(r, "fx-c-dimtext");
    expect(hit?.opacity).toBe(0.3);
    expect(hit?.ratio).toBeLessThan(5);
    expect(hit?.opacityFrom.some((f) => f.includes("fx-c-dimbox"))).toBe(true);
  });

  it("lists a PASSING dimmed string separately rather than in the findings", () => {
    mount(
      `<div class="fx-c-lightdim" style="background:#fff;opacity:0.9">
         <div class="fx-c-stillok" style="color:#000;font-size:14px">fine</div>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-stillok")).toBeUndefined();
    expect(r.dimmed.some((d) => hasClass(d.sel, "fx-c-stillok"))).toBe(true);
    // And never in both lists, so one element is never two rows.
    expect(r.passes.some((p) => hasClass(p.sel, "fx-c-stillok"))).toBe(false);
  });

  it("refuses to judge text over a background-image and names the layer", () => {
    mount(
      `<div class="fx-c-img" style="background-image:linear-gradient(#000,#fff)">
         <span class="fx-c-ontop" style="color:#888;font-size:14px">unknowable</span>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-ontop")).toBeUndefined();
    const un = r.unresolved.find((u) => hasClass(u.sel, "fx-c-ontop"));
    expect(un?.bgFrom).toContain("fx-c-img");
  });

  it("measures a DIRECT text child only, so one string is judged once", () => {
    mount(
      `<div class="fx-c-wrap" style="background:#fff;color:#999;font-size:14px">
         <span class="fx-c-inner">nested</span>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    // The wrapper's own text is whitespace, so it is not scanned; the span is.
    expect(findingFor(r, "fx-c-inner")).toBeDefined();
    expect(findingFor(r, "fx-c-wrap")).toBeUndefined();
  });

  it("skips a screen-reader-only string, which is painted and unreadable", () => {
    mount(
      `<div style="background:#fff">
         <span class="fx-c-sr" style="position:absolute;width:1px;height:1px;overflow:hidden;color:#fefefe;font-size:14px">for AT only</span>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-sr")).toBeUndefined();
    expect(r.passes.some((p) => hasClass(p.sel, "fx-c-sr"))).toBe(false);
  });

  it("skips text at a folded opacity of ZERO, which is not painted at all", () => {
    // The ordinary state of a collapsed disclosure body and an unexpanded pill card.
    // Measured on the live app before this: 10 of 15 violations were 1:1 rows for
    // text nobody could look at, one of them 82 elements deep in a closed tool
    // region. `--reveal` is how those surfaces get audited.
    mount(
      `<div class="fx-c-collapsed" style="background:#fff;opacity:0">
         <span class="fx-c-unseen" style="color:#fff;font-size:14px">invisible</span>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-unseen")).toBeUndefined();
    expect(r.dimmed.some((d) => hasClass(d.sel, "fx-c-unseen"))).toBe(false);
    expect(r.passes.some((p) => hasClass(p.sel, "fx-c-unseen"))).toBe(false);
  });

  it("exempts text in a DISABLED control, which 1.4.3 excuses outright", () => {
    // "Text that is part of an inactive user interface component has no contrast
    // requirement." This app dims a disabled control to 0.4, so its label fails on
    // the numbers — measured live on a mid-turn Rewind at 1.84:1.
    mount(
      `<div style="background:#fff">
         <button class="fx-c-off" disabled style="opacity:0.4;color:#999;font-size:11px">
           <span class="fx-c-offlabel">Rewind</span>
         </button>
         <button class="fx-c-on" style="opacity:0.4;color:#999;font-size:11px">
           <span class="fx-c-onlabel">Live</span>
         </button>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-offlabel")).toBeUndefined();
    expect(r.inactive.some((i) => hasClass(i.sel, "fx-c-offlabel"))).toBe(true);
    // The enabled twin is identical but for `disabled`, so it still fails — which is
    // what makes the exemption a reading of the attribute rather than of the dimming.
    expect(findingFor(r, "fx-c-onlabel")).toBeDefined();
  });

  it("skips a hidden string rather than measuring the surface behind it", () => {
    mount(
      `<div style="background:#fff">
         <span class="fx-c-invis" style="visibility:hidden;color:#fefefe;font-size:14px">gone</span>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(findingFor(r, "fx-c-invis")).toBeUndefined();
  });

  it("names the ink token it found and reports the palette consistent", () => {
    document.documentElement.style.setProperty("--c-text-primary", "#111111");
    mount(`<div class="fx-c-tok" style="background:#fff;color:#000;font-size:14px">x</div>`);
    const r = run<Contrast>("contrast", {});
    expect(r.inkToken).toBe("--c-text-primary");
    expect(r.rootInk).toBe("#111111");
    expect(r.palette).toBe("consistent");
  });

  it("calls the palette MIXED when a descendant resolves a different ink", () => {
    // The theme-flip failure the runner's `--theme-settle` exists for: a subtree
    // still on the previous palette. A custom property inherits, so a leaf can only
    // disagree when something between it and the root re-declares the token.
    document.documentElement.style.setProperty("--c-text-primary", "#111111");
    mount(
      `<div class="fx-c-stale" style="--c-text-primary:#eeeeee;background:#fff">
         <span class="fx-c-leaf" style="color:#000;font-size:14px">x</span>
       </div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(r.palette).toBe("mixed");
    expect(r.leafInk).toBe("#eeeeee");
  });

  it("reports the palette UNAVAILABLE when no ink token resolves to a colour", () => {
    mount(`<div class="fx-c-notok" style="background:#fff;color:#000;font-size:14px">x</div>`);
    const r = run<Contrast>("contrast", {});
    expect(r.inkToken).toBeNull();
    expect(r.palette).toBe("unavailable");
  });

  it("takes the caller's ink token, and refuses one that is not a colour", () => {
    document.documentElement.style.setProperty("--my-ink", "#222222");
    document.documentElement.style.setProperty("--not-a-colour", "12px");
    mount(`<div style="background:#fff;color:#000;font-size:14px">x</div>`);
    expect(run<Contrast>("contrast", { inkToken: "--my-ink" }).inkToken).toBe("--my-ink");
    expect(run<Contrast>("contrast", { inkToken: "--not-a-colour" }).palette).toBe("unavailable");
    document.documentElement.style.removeProperty("--my-ink");
    document.documentElement.style.removeProperty("--not-a-colour");
  });

  it("lists the distinct backdrops it measured against", () => {
    mount(
      `<div class="fx-c-b1" style="background:#ffffff;color:#000;font-size:14px">a</div>
       <div class="fx-c-b2" style="background:#000000;color:#fff;font-size:14px">b</div>`,
    );
    const r = run<Contrast>("contrast", {});
    expect(r.backdrops).toContain("#ffffff");
    expect(r.backdrops).toContain("#000000");
  });
});

describe("the injection contract", () => {
  it("answers identically through `pageSource` and through a direct call", () => {
    mount(
      `<div class="fx-parity" style="display:flex;gap:4px;border-radius:12px;padding:4px">
         <button style="height:32px;width:60px;border-radius:2px">a</button>
         <button style="height:24px;width:60px;border-radius:7px">b</button>
         <button class="fx-p-tiny" style="height:10px;width:10px"></button>
       </div>`,
    );
    for (const [name, opts] of [
      ["control-height", { tol: 1.5 }],
      ["radius", { tol: 0.75 }],
      ["contrast", {}],
    ] as const) {
      expect(runViaSource(name, opts)).toEqual(run(name, opts));
    }
  });

  it("carries its options by value, so a runner's `--tol` reaches the walker", () => {
    mount(
      `<div class="fx-optflow" style="display:flex">
         <button style="height:32px;width:60px">a</button>
         <button style="height:26px;width:60px">b</button>
       </div>`,
    );
    const strict = runViaSource<ControlHeight>("control-height", { tol: 1.5 });
    const loose = runViaSource<ControlHeight>("control-height", { tol: 8 });
    expect(mismatchesFor(strict, "fx-optflow")).toHaveLength(1);
    expect(mismatchesFor(loose, "fx-optflow")).toHaveLength(0);
  });

  it("emits every helper a walker calls, so no page-side call is unbound", () => {
    // The prelude binds by STRING, which no type checker can see: a helper
    // renamed in `HELPERS` and not at its call sites throws only here and in the
    // page. `new Function` gives it the page's scope, so the throw is the same.
    expect(() => runViaSource("radius", { tol: 0.75 })).not.toThrow();
    expect(() => runViaSource("control-height", { tol: 1.5 })).not.toThrow();
  });

  it("refuses a walker it does not have, naming the ones it does", () => {
    expect(() => pageSource("no-such-walker")).toThrow(/unknown walker/u);
    expect(() => pageSource("no-such-walker")).toThrow(/control-height/u);
  });

  it("exposes exactly the four walkers the runners ask for", () => {
    expect(Object.keys(WALKERS).sort()).toEqual(["contrast", "control-height", "radius", "reveal"]);
  });
});
