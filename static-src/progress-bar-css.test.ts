// The two native <progress> bars, measured against the real assembled cascade.
//
// A UA-DRAWN CONTROL IS THE THING THIS FILE EXISTS FOR. `<progress>` renders from
// the engine's own stylesheet, so a skin that names only one engine's parts is a
// default control everywhere else — and the two engines split the work
// differently: Gecko paints the track from the ELEMENT's own `background` and the
// fill from `::-moz-progress-bar`, Blink and WebKit paint the track from
// `::-webkit-progress-bar` and the fill from `::-webkit-progress-value`. Three
// declarations for two visual parts, and dropping any one of them regresses
// exactly one engine, silently, on a surface nobody screenshots.
//
// WHAT THIS CAN AND CANNOT READ, measured rather than assumed. In Chromium 151
// `getComputedStyle(bar, "::-webkit-progress-bar")` and `::-webkit-progress-value`
// both return the ORIGINATING ELEMENT's computed style — the fill probe reports
// the element's own background, the element's full width, and `all 0s` for a
// declared `width` transition — and `::-moz-progress-bar` returns an empty
// declaration block. So neither engine's parts are reachable through computed
// style, and `::-moz-progress-bar` is not reachable from this engine at all. The
// three pseudo-element rules are therefore pinned as SOURCE facts, which is what
// `css-rules.ts`'s readers exist for, and the assertions that CAN be made against
// real layout (the element's own box, its appearance, the track colour Gecko
// paints, the determinate/indeterminate contract, the accessible name) are made
// there.
//
// Painted pixels were tried and rejected: `page.screenshot` writes an artifact
// into the package on every run and its base64 did not decode to an image here.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
});

/** A declaration's rendered value, read off a real element rather than restated.
 *  This is what makes "the fill colour RESOLVES" a measurement: a token with no
 *  declaration behind it is valid CSS text that computes to nothing, which is the
 *  phantom-token class `vibekit-ui.md` bans. */
function resolved(prop: "background-color" | "border-radius", expr: string): string {
  const probe = document.createElement("div");
  probe.style.setProperty(prop === "background-color" ? "background" : "border-radius", expr);
  document.body.appendChild(probe);
  const value = getComputedStyle(probe).getPropertyValue(prop);
  probe.remove();
  return value;
}

/** The upload row as `static/index.html` builds it: the bar, the label and the
 *  cancel button in one flex row, so `flex: 1` has something to divide. */
function mountUploadRow(): HTMLProgressElement {
  const row = document.createElement("div");
  row.id = "upload-progress";
  row.className = "upload-progress";
  row.style.inlineSize = "600px";
  const bar = document.createElement("progress");
  bar.id = "upload-progress-bar";
  bar.className = "upload-progress-bar";
  bar.max = 100;
  bar.value = 0;
  bar.setAttribute("aria-label", "Upload progress");
  const label = document.createElement("span");
  label.className = "upload-progress-label";
  label.textContent = "Uploading...";
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "upload-progress-cancel";
  row.append(bar, label, cancel);
  document.body.appendChild(row);
  return bar;
}

/** The knowledge row's progress wrap as `knowledge.ts` builds it. */
function mountKnowledgeRow(pct: number): HTMLProgressElement {
  const row = document.createElement("div");
  row.className = "list-row knowledge-row knowledge-indexing";
  row.style.inlineSize = "600px";
  const wrap = document.createElement("span");
  wrap.className = "knowledge-progress";
  const bar = document.createElement("progress");
  bar.className = "knowledge-bar";
  bar.setAttribute("aria-label", "Indexing");
  bar.max = 100;
  bar.value = pct;
  const text = document.createElement("span");
  text.className = "knowledge-progress-text";
  text.textContent = `Indexing… ${String(pct)}%`;
  wrap.append(bar, text);
  row.appendChild(wrap);
  document.body.appendChild(row);
  return bar;
}

describe("the upload bar's own box", () => {
  it("suppresses the UA control and takes the track's colour, height and radius", () => {
    const bar = mountUploadRow();
    const cs = getComputedStyle(bar);

    // Without this the engine draws its own control and every rule below is moot.
    expect(cs.appearance).toBe("none");
    // The element's own background IS Gecko's track. It also has to resolve to a
    // real colour rather than to nothing.
    const track = resolved("background-color", "var(--c-bg-tertiary)");
    expect(track).not.toBe("");
    expect(track).not.toBe("rgba(0, 0, 0, 0)");
    expect(cs.backgroundColor).toBe(track);
    expect(cs.borderRadius).toBe(resolved("border-radius", "var(--r-sm)"));
    expect(cs.overflow).toBe("hidden");
    expect(bar.getBoundingClientRect().height).toBeCloseTo(6, 1);
  });

  it("grows to fill the row beside the label and the cancel button", () => {
    const bar = mountUploadRow();
    const row = bar.parentElement;
    expect(row).not.toBeNull();
    const barBox = bar.getBoundingClientRect();
    const rowBox = row?.getBoundingClientRect() ?? new DOMRect();
    const label = row?.querySelector(".upload-progress-label")?.getBoundingClientRect();
    // `flex: 1` means the bar takes the space the two fixed-size siblings leave,
    // so it is wide and it does not reach the label.
    expect(barBox.width).toBeGreaterThan(rowBox.width / 2);
    expect(barBox.right).toBeLessThanOrEqual((label?.left ?? 0) + 0.5);
  });
});

describe("the knowledge bar's own box", () => {
  it("suppresses the UA control and takes the track's colour, height and radius", () => {
    const bar = mountKnowledgeRow(42);
    const cs = getComputedStyle(bar);

    expect(cs.appearance).toBe("none");
    const track = resolved(
      "background-color",
      "color-mix(in srgb, var(--c-accent) 20%, transparent)",
    );
    expect(track).not.toBe("");
    expect(track).not.toBe("rgba(0, 0, 0, 0)");
    expect(cs.backgroundColor).toBe(track);
    expect(cs.borderRadius).toBe(resolved("border-radius", "var(--r-sm)"));
    expect(cs.overflow).toBe("hidden");
    expect(bar.getBoundingClientRect().height).toBeCloseTo(4, 1);
  });

  it("stays inside its declared inline-size band beside the percentage text", () => {
    const bar = mountKnowledgeRow(42);
    const width = bar.getBoundingClientRect().width;
    // 3rem..10rem at the app's 16px root. A bar that ignored the band would eat
    // the row, which is what the min/max pair exists to prevent.
    expect(width).toBeGreaterThanOrEqual(48);
    expect(width).toBeLessThanOrEqual(160);
  });
});

describe("every engine's parts are skinned", () => {
  // The three-rule requirement, per bar. Source facts because computed style
  // reports the element's own declarations for the two -webkit- pseudos and
  // nothing at all for the -moz- one (measured, see the file header).
  const cases = [
    { bar: ".upload-progress-bar", sheet: "19-files.css", track: "var(--c-bg-tertiary)" },
    {
      bar: ".knowledge-bar",
      sheet: "25-knowledge.css",
      track: "color-mix(in srgb, var(--c-accent) 20%, transparent)",
    },
  ] as const;

  // ONE spelling for both bars, which is why it is not a per-case field: each
  // kept its predecessor's word (`width` on the upload bar, `inline-size` on the
  // knowledge one) for the same idiom on the same UA-internal pseudo-element.
  // Physical, because the engine sizes this pseudo-element physically and a
  // progress fill is never in a vertical writing mode. A per-case field would
  // let one bar drift back and stay green on its own fixture.
  const fillProp = "width";

  for (const c of cases) {
    it(`${c.bar} paints the track on both engines' track parts`, () => {
      const css = loadCSS(c.sheet);
      // Gecko's track is the element's own background; Blink's is the pseudo.
      const own = ruleContaining(css, c.bar);
      expect(own.body).toContain(`background: ${c.track}`);
      // `border: 0` is Gecko's, and it is asserted HERE rather than through
      // computed style for the same reason `::-moz-progress-bar` is: Chromium
      // gives `<progress>` no border to begin with, so deleting the declaration
      // changes nothing measurable in this engine while Firefox draws its own
      // default border around the control. An unfalsifiable computed-style check
      // would have read as coverage; this one goes red on the deletion.
      expect(own.body, "Gecko's default border is reset").toContain("border: 0");
      const webkitTrack = ruleContaining(css, `${c.bar}::-webkit-progress-bar`);
      expect(webkitTrack.body).toContain(`background: ${c.track}`);
      expect(webkitTrack.body).toContain("border-radius: var(--r-sm)");
    });

    it(`${c.bar} paints the fill on BOTH engines' value parts`, () => {
      const css = loadCSS(c.sheet);
      for (const pseudo of ["::-webkit-progress-value", "::-moz-progress-bar"]) {
        const rule = ruleContaining(css, `${c.bar}${pseudo}`);
        expect(rule.body, `${pseudo} carries the accent fill`).toContain(
          "background: var(--c-accent)",
        );
        expect(rule.body, `${pseudo} keeps the track's radius`).toContain(
          "border-radius: var(--r-sm)",
        );
      }
      // WHERE the transition is declared, and nothing about whether it runs.
      // Measured: `getAnimations({ subtree: true })` on a bar whose value jumps
      // 0 -> 80 reports ZERO animations, so Chromium exposes a UA-internal
      // pseudo-element's transition through neither computed style nor the Web
      // Animations API — the animation itself is unverifiable from here, in both
      // engines. What this pins is that the declaration stays on the Blink rule
      // (the only one documented to honour it) rather than being "tidied" onto
      // the Gecko rule, where it would do nothing while reading as coverage.
      expect(
        ruleContaining(css, `${c.bar}::-webkit-progress-value`).body,
        "the transition is declared on the engine that honours it",
      ).toContain(`transition: ${fillProp} var(--dur-standard)`);
      expect(ruleContaining(css, `${c.bar}::-moz-progress-bar`).body).not.toContain("transition");
    });

    it(`${c.bar}'s accent fill resolves to a real colour`, () => {
      // A phantom token is valid CSS text that computes to nothing, so the source
      // read above cannot answer this and the pseudo-element cannot be probed.
      const accent = resolved("background-color", "var(--c-accent)");
      expect(accent).not.toBe("");
      expect(accent).not.toBe("rgba(0, 0, 0, 0)");
    });
  }
});

describe("the indeterminate spelling both bars rely on", () => {
  // A PREMISE, stated once, rather than three tests of the platform. It is what
  // makes `upload.ts`'s `removeAttribute("value")` the native replacement for the
  // `aria-valuenow` removal it used to do, and what makes `knowledge.ts`'s
  // `pct !== null` guard necessary rather than fussy: a valueless <progress> is
  // not a bar at zero, it is an animated bar claiming unknown progress. The
  // behavioural halves live with the code that decides them — `upload.test.ts`
  // and `knowledge.test.ts`.
  it("is an absent value attribute, and it is reversible", () => {
    const bar = mountUploadRow();
    bar.value = 42;
    expect(bar.position, "determinate reports the fraction").toBeCloseTo(0.42, 5);
    bar.removeAttribute("value");
    expect(bar.position, "valueless IS indeterminate, not zero").toBe(-1);
    bar.value = 70;
    expect(bar.position, "a later determinate tick restores a value").toBeCloseTo(0.7, 5);
  });
});

describe("each bar carries an accessible name", () => {
  // Through the ROLE query, so this is the computed accessible name rather than
  // the presence of an attribute. Native <progress> reports its own value, so the
  // name is the only ARIA either bar authors.
  it("names the upload bar", async () => {
    const bar = mountUploadRow();
    bar.setAttribute("aria-label", "Uploading 3 file(s)");
    await expect
      .element(page.getByRole("progressbar", { name: "Uploading 3 file(s)" }))
      .toBeInTheDocument();
    expect(page.getByRole("progressbar", { name: "Uploading 3 file(s)" }).elements()).toHaveLength(
      1,
    );
  });

  it("names the knowledge bar", async () => {
    mountKnowledgeRow(42);
    await expect.element(page.getByRole("progressbar", { name: "Indexing" })).toBeInTheDocument();
  });
});
