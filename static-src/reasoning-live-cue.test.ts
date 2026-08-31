// ---------------------------------------------------------------------------
// The cues a live thinking trace carries, and the one it carries once sealed.
//
// While it streams: it mounts EXPANDED with text growing into
// `.reasoning-body` and its label reads "Thinking…" — that is the whole live
// affordance. There is deliberately NO pulsing disc and NO animation on any
// reasoning selector: a pulsing dot is reserved to TABS and workflow-agent
// surfaces, which report work the reader cannot see, and a live trace is
// already on screen (user ruling, `vibekit-ui.md` "GPU compositing").
// `buildReasoning` still sets the `streaming` class for a live trace (state
// bookkeeping `seal()` removes); no stylesheet may hang an animation off it.
// Once sealed the disclosure folds shut and the label becomes "Thinking
// completed", so the collapsed row is all the reader is left with.
//
// The word count is what fills that row. It is a SIBLING of the label rather
// than part of it, so every assertion here on the label's exact text has to keep
// holding — that is the property the two elements exist to give.
//
// The DOM half runs the real builder; a SOURCE half reads the shipped
// stylesheet to pin that no reasoning selector carries an animation (see
// __test-helpers__/css-rules.ts).
//
// The last describe is a third kind and says why it has to be: two of the
// count's declarations are not source facts at all, so it injects the sheet and
// measures.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import fc from "fast-check";
import { allRules, loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));

import { buildReasoning } from "./fundamentals/reasoning.js";

describe("a live reasoning block's cues", () => {
  it("mounts open, labelled, marked streaming, and counted", () => {
    const r = buildReasoning("weighing the options", true);
    expect(r.root.open).toBe(true);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking…");
    // State bookkeeping only: the class marks a live trace and seal() removes
    // it. No stylesheet may animate off it — pinned by the source describe
    // below.
    expect(r.root.classList.contains("streaming")).toBe(true);
    // The count rides BESIDE the label, never inside it: the label is the state
    // and stays exactly the state (see "the summary's word count" below).
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("3 words");
  });

  it("drops the streaming mark when the trace seals", () => {
    const r = buildReasoning("weighing the options", true);
    r.seal();
    // `[open]` flips back to true whenever the reader re-expands a finished
    // trace, which is why live state hangs off this class instead.
    expect(r.root.classList.contains("streaming")).toBe(false);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
  });

  it("never marks a replayed trace, which mounts settled and collapsed", () => {
    const r = buildReasoning("weighing the options", false);
    expect(r.root.classList.contains("streaming")).toBe(false);
    expect(r.root.open).toBe(false);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Reasoning");
  });
});

describe("the summary's word count", () => {
  function countOf(r: { root: HTMLElement }): string | undefined {
    return r.root.querySelector(".reasoning-count")?.textContent ?? undefined;
  }

  it("grows with the trace, and is a sibling of the label rather than part of it", () => {
    const r = buildReasoning("", true);
    // Zero words renders NOTHING. "0 words" on a trace that has not started is
    // a measurement of nothing dressed up as information.
    expect(countOf(r)).toBe("");
    r.append("weighing");
    expect(countOf(r)).toBe("1 word");
    // This boundary falls INSIDE a word, and the next chunk finishes it. Adding
    // each delta's own count blindly would reach 1 + 2 + 2 = 5 by the end; the
    // fold carries whether the trace ends mid-word and reaches the 4 that are
    // actually there. The exhaustive version of this case is the last describe.
    r.append(" the opt");
    expect(countOf(r)).toBe("3 words");
    r.append("ions carefully");
    expect(countOf(r)).toBe("4 words");
    // Sealing rewrites the label and leaves the number alone.
    r.seal();
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
    expect(countOf(r)).toBe("4 words");
  });

  it("recounts on the replace-to-full path, which is the one streaming uses", () => {
    // messages-blocks.ts drives a live block through setText from a signal, not
    // through append, so the count has to track that path too.
    const r = buildReasoning("one", true);
    expect(countOf(r)).toBe("1 word");
    r.setText("one two three");
    expect(countOf(r)).toBe("3 words");
    // setText early-returns on a value no longer than what is already rendered,
    // so the count holds instead of following the shorter string down.
    r.setText("one two");
    expect(countOf(r)).toBe("3 words");
  });

  it("counts a replayed trace at mount and groups the thousands", () => {
    const words = Array.from({ length: 1204 }, (_, i) => `w${String(i)}`).join(" ");
    const r = buildReasoning(words, false);
    // Computed rather than a "1,204" literal: the separator belongs to the
    // platform formatter, and what this pins is that one is used at all.
    expect(countOf(r)).toBe(`${(1204).toLocaleString()} words`);
  });

  it("renders nothing for a trace that is only whitespace", () => {
    // Reachable on replay: the mount guard only skips an EMPTY settled block.
    expect(countOf(buildReasoning("  \n  ", false))).toBe("");
  });

  it("counts a long single token as the one word it is", () => {
    // The unit never switches, and this is the case that decided it. A
    // characters-per-token threshold (added to serve scripts with no inter-word
    // spaces, then removed) cannot tell those apart from an English trace holding
    // one long token, so a trace that was a single URL reported its character
    // count as though it were a different language.
    expect(countOf(buildReasoning("https://github.com/cplieger/vibekit/pull/1234", false))).toBe(
      "1 word",
    );
    expect(countOf(buildReasoning("uncharacteristically", false))).toBe("1 word");
    expect(countOf(buildReasoning("weighing the options carefully", false))).toBe("4 words");
  });

  it("reads a space-less script as one word, which is the measure's known floor", () => {
    // Pinned rather than left unwritten: a whitespace count genuinely cannot see
    // inside a Chinese trace, so this reads "1 word" at any length. Accepted over
    // a threshold that misreads the common case above; the real fix is
    // `Intl.Segmenter` and it is separate work.
    expect(countOf(buildReasoning("先考虑各种可能的做法然后逐一排除掉行不通的那些", false))).toBe(
      "1 word",
    );
  });
});

describe("the count and the summary's accessible name", () => {
  /**
   * The text of `summary` as an assistive technology reads it: `aria-hidden`
   * subtrees skipped.
   *
   * A source-shaped stand-in for the platform's accessible-name computation,
   * which no in-page API exposes. It is the same walk the name computation does
   * over a `<summary>` with no `aria-label` and no `aria-labelledby` — content
   * from its descendants, minus the ones excluded from the tree.
   */
  function accessibleName(summary: Element): string {
    let out = "";
    for (const node of summary.childNodes) {
      if (node.nodeType === Node.TEXT_NODE) {
        out += node.textContent ?? "";
        continue;
      }
      if (node instanceof Element && node.getAttribute("aria-hidden") !== "true") {
        out += node.textContent ?? "";
      }
    }
    return out.trim();
  }

  it("keeps the count out of the name for the whole life of the block", () => {
    // <summary> is focusable and takes its accessible name from its descendants,
    // so a number changing on every chunk would rewrite the name of a focusable
    // control for the length of the stream. `aria-hidden` never comes off: an
    // attribute removed at seal() would rename the row one last time, under a
    // reader whose focus may already be sitting on it.
    const r = buildReasoning("weighing", true);
    const count = r.root.querySelector(".reasoning-count");
    expect(count?.getAttribute("aria-hidden")).toBe("true");
    r.append(" the options");
    expect(count?.getAttribute("aria-hidden")).toBe("true");
    r.seal();
    expect(count?.getAttribute("aria-hidden")).toBe("true");
    // The number is still ON the row — hidden from the name, not withheld from
    // the reader who can see it.
    expect(count?.textContent).toBe("3 words");
  });

  it("hides it on a replayed trace too, which mounts settled", () => {
    const r = buildReasoning("weighing the options", false);
    expect(r.root.querySelector(".reasoning-count")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("leaves the LABEL in the name, which is the half that must be announced", () => {
    // The point of hiding the count: what is left is exactly the state. If this
    // ever reads "Thinking… 3 words" the churn is back; if it reads "" the label
    // was hidden by mistake and the control has no name at all.
    const r = buildReasoning("weighing the options", true);
    const summary = r.root.querySelector(".reasoning-summary");
    expect(summary).not.toBeNull();
    expect(accessibleName(summary as Element)).toBe("Thinking…");
    expect(r.root.querySelector(".reasoning-label")?.hasAttribute("aria-hidden")).toBe(false);
    r.seal();
    expect(accessibleName(summary as Element)).toBe("Thinking completed");
  });
});

describe("13-messages.css keeps reasoning still and flush", () => {
  const css = loadCSS("13-messages.css");

  it("hangs no animation off any reasoning selector", () => {
    // The ruling this pins: a pulsing dot is reserved to TABS and
    // workflow-agent surfaces. A live trace is on screen already — it mounts
    // open with its text growing and its label reading "Thinking…" — so no
    // reasoning rule may carry an animation, a ::before disc included.
    const reasoningRules = allRules(css).filter((r) => r.selector.includes("reasoning"));
    expect(reasoningRules.length).toBeGreaterThan(5);
    for (const r of reasoningRules) {
      expect(r.body, `animation in rule "${r.selector}"`).not.toMatch(/animation/u);
    }
  });

  it("draws no accent bar and no indent on the block", () => {
    // The purple border-inline-start and the body's inline padding were
    // removed by user ruling (2026-08-30): the trace sits flush on the
    // turn body's content edge like every other block.
    expect(ruleContaining(css, ".reasoning-block").body).not.toMatch(/border/u);
    const body = ruleContaining(css, ".reasoning-body").body;
    expect(body).toMatch(/padding:\s*var\(--sp-2\)\s+0/u);
  });
});

describe("the summary row gives up the count before the label", () => {
  const css = loadCSS("13-messages.css");

  it("pins the label's width and lets the count shrink", () => {
    // The priority this change chose deliberately: a narrow row ellipsizes the
    // footnote and never the state.
    expect(ruleContaining(css, ".reasoning-label").body).toMatch(/flex-shrink:\s*0/u);
    const count = ruleContaining(css, ".reasoning-count").body;
    expect(count).toMatch(/flex-shrink:\s*1/u);
    // `overflow: hidden` is what lets a flex item shrink past its own content,
    // so without it the ellipsis never arrives and the row overflows instead.
    expect(count).toMatch(/overflow:\s*hidden/u);
    expect(count).toMatch(/text-overflow:\s*ellipsis/u);
    // The row is one line: the label cannot wrap under the chevron and the count
    // cannot wrap under the label.
    expect(count).toMatch(/white-space:\s*nowrap/u);
    expect(ruleContaining(css, ".reasoning-summary").body).not.toMatch(/flex-wrap/u);
  });

  it("takes its ink and step from the sheet's own metadata vocabulary", () => {
    // Borrowed rather than invented: no hardcoded colour and no literal size on
    // a footnote that already has tokens for both.
    const count = ruleContaining(css, ".reasoning-count").body;
    expect(count).toMatch(/color:\s*var\(--c-text-tertiary\)/u);
    expect(count).toMatch(/font-size:\s*var\(--fs-xs\)/u);
    expect(count).not.toMatch(/#[0-9a-f]{3,8}\b/iu);
    expect(count).not.toMatch(/\b(?:rgb|hsl|oklch)\(/u);
  });

  it("leaves the chevron out of the squeeze", () => {
    // Pinning the label redirects shrinkage onto the remaining shrinkable items,
    // so the glyph having its own `flex-shrink: 0` is what keeps the count the
    // only one. It lives in another file, which is why it is asserted and not
    // assumed.
    expect(ruleContaining(loadCSS("10-shell-app.css"), ".disclosure-chevron").body).toMatch(
      /flex-shrink:\s*0/u,
    );
  });
});

// ---------------------------------------------------------------------------
// The two declarations in `.reasoning-count` that a source read cannot check,
// asserted COMPUTED instead.
//
// `margin-inline-start: auto` is the whole of "right-aligned", and
// `font-variant-numeric: tabular-nums` is what the rule's own comment calls
// load-bearing against per-chunk row twitch. A `toMatch` on the rule body would
// pin the two strings without establishing that either reaches the element, and
// neither is a fact about the SOURCE: the auto margin only right-aligns because
// a DIFFERENT rule in the same sheet makes `.reasoning-summary` a flex
// container, and free-space distribution is something only a layout engine can
// answer.
//
// So these three run against a real cascade: the browser project is a headless
// Chromium at a fixed viewport, and the shipped sheet is injected into the page
// (the pattern store-blocks.test.ts uses for the row-collapse rule). Injected
// WHOLE rather than rule-by-rule on purpose — hand-picking the three rules
// involved would be measuring a cascade assembled here instead of the one that
// ships, and the flex context is exactly the part that would go missing.
// `01-tokens.css` rides along because the count's own step and ink are tokens.
// ---------------------------------------------------------------------------
describe("the count's alignment and digit metrics, computed", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement;

  beforeEach(() => {
    style = document.createElement("style");
    style.textContent = [loadCSS("01-tokens.css"), loadCSS("13-messages.css")].join("\n");
    document.head.appendChild(style);
    host = document.createElement("div");
    // A width wide enough that "4 words" cannot reach the right edge on its own,
    // so arriving there is the margin's doing and not the text's.
    host.style.width = "420px";
    document.body.appendChild(host);
  });

  afterEach(() => {
    style.remove();
    host.remove();
  });

  /** A live block, mounted in the styled host. */
  function row(): { summary: DOMRect; label: DOMRect; count: HTMLElement } {
    const r = buildReasoning("weighing the options carefully", true);
    host.appendChild(r.root);
    const summary = r.root.querySelector(".reasoning-summary");
    const label = r.root.querySelector(".reasoning-label");
    const count = r.root.querySelector(".reasoning-count");
    expect(summary).not.toBeNull();
    expect(label).not.toBeNull();
    expect(count).not.toBeNull();
    return {
      summary: (summary as HTMLElement).getBoundingClientRect(),
      label: (label as HTMLElement).getBoundingClientRect(),
      count: count as HTMLElement,
    };
  }

  it("pushes the count to the row's far edge", () => {
    const { summary, count } = row();
    expect(count.textContent).toBe("4 words");
    expect(summary.right - count.getBoundingClientRect().right).toBeLessThan(1);
  });

  it("gets it there by pushing rather than by filling the row", () => {
    // The half that makes the assertion above load-bearing. A count wide enough
    // to fill the row would sit at the right edge with no margin at all, so the
    // GAP is what says the free space went to the margin. Measured with the
    // declaration deleted: the count lands 287px short of the edge and this gap
    // collapses to the row's own 0.3rem.
    const { label, count } = row();
    expect(count.getBoundingClientRect().left - label.right).toBeGreaterThan(100);
  });

  it("gives the digits a fixed advance so a growing number cannot twitch the row", () => {
    // Computed rather than measured off two rendered numbers: whether
    // proportional digits differ in width at all is a property of whichever font
    // the runner resolved, so a width comparison would pass for free under a font
    // that is already tabular. Deleting the declaration reads "normal" here.
    expect(getComputedStyle(row().count).fontVariantNumeric).toBe("tabular-nums");
  });
});

describe("the shared breathe keyframe", () => {
  it("exists in 03-base.css for its consumers", () => {
    // The tab dot, the run pip, the streaming code fence, and friends. One
    // keyframe, many rules — none of them a reasoning selector (pinned above).
    expect(loadCSS("03-base.css")).toContain("@keyframes vk-breathe");
  });
});

// ---------------------------------------------------------------------------
// The count is folded in per delta rather than recounted from the whole trace,
// which buys the app the largest content bucket in it back (thinking is 1.2 MB
// over 3133 blocks in the real chat files, and the recount was O(length squared)
// per block, re-driven on every repaint for COLLAPSED traces nobody was looking
// at). What it costs is that the answer now depends on where the chunk
// boundaries fell, so the boundaries are what these tests are about.
//
// The oracle is the recount the fold replaced: `text.split(/\s+/u)` over the
// whole accumulated string. Every case below asserts the two agree, so the
// property is not "the number looks right" but "chunking changed nothing".
//
// Deliberately driven through `buildReasoning` and read off `.reasoning-count`.
// `foldWords` is not exported and must not become exported for a test: the
// number the READER sees is the claim, and it reaches them through the mount
// seed, `append` and `setText` — three call sites that each fold, and any one of
// which could be the one that gets it wrong.
// ---------------------------------------------------------------------------
describe("chunk boundaries cannot change the count", () => {
  /** The whole-string recount the incremental fold has to match. */
  function recount(text: string): number {
    return text.split(/\s+/u).filter((w) => w !== "").length;
  }

  /** What the row renders for a given number of words. */
  function rendered(words: number): string {
    if (words === 0) {
      return "";
    }
    return `${words.toLocaleString()} ${words === 1 ? "word" : "words"}`;
  }

  /** Mount with the first chunk, append the rest, read the row. */
  function countAfterAppends(chunks: readonly string[]): string {
    const r = buildReasoning(chunks[0] ?? "", true);
    for (const c of chunks.slice(1)) {
      r.append(c);
    }
    return r.root.querySelector(".reasoning-count")?.textContent ?? "";
  }

  /** The same trace assembled the way a live block really is: replace-to-full. */
  function countAfterSetText(chunks: readonly string[]): string {
    const r = buildReasoning("", true);
    let acc = "";
    for (const c of chunks) {
      acc += c;
      r.setText(acc);
    }
    return r.root.querySelector(".reasoning-count")?.textContent ?? "";
  }

  // Every feature that can make a boundary matter, in one string: leading and
  // trailing whitespace, a run of several spaces, a tab, a blank line, and two
  // words that a split can cut in half.
  const TRICKY = "  weighing\tthe   options\n\ncarefully  ";

  it("agrees with a whole-string recount at every possible split of a tricky trace", () => {
    // Exhaustive over three-way splits, which subsumes the one- and two-chunk
    // cases because a split point may sit at either end and produce an empty
    // chunk. That empty chunk is not filler — it is the zero-length delta the
    // early return in `append` exists for, so this covers it at every position.
    const expected = rendered(recount(TRICKY));
    for (let i = 0; i <= TRICKY.length; i++) {
      for (let j = i; j <= TRICKY.length; j++) {
        const chunks = [TRICKY.slice(0, i), TRICKY.slice(i, j), TRICKY.slice(j)];
        expect(countAfterAppends(chunks), `append split at ${String(i)}/${String(j)}`).toBe(
          expected,
        );
        expect(countAfterSetText(chunks), `setText split at ${String(i)}/${String(j)}`).toBe(
          expected,
        );
      }
    }
  });

  it("agrees for arbitrary traces cut into arbitrary chunks", () => {
    // The exhaustive case pins one string; this one pins the rule. The alphabet
    // is two letters and four kinds of whitespace, because what the fold can get
    // wrong is which side of a boundary a space fell on — not which letter it is.
    const chunk = fc
      .array(fc.constantFrom("a", "b", " ", "  ", "\n", "\t"), { minLength: 0, maxLength: 6 })
      .map((a) => a.join(""));
    fc.assert(
      fc.property(fc.array(chunk, { minLength: 0, maxLength: 10 }), (chunks) => {
        const expected = rendered(recount(chunks.join("")));
        expect(countAfterAppends(chunks)).toBe(expected);
        expect(countAfterSetText(chunks)).toBe(expected);
      }),
      { numRuns: 300 },
    );
  });

  it("counts a word split across three deltas once", () => {
    // The case the fold exists to get right, spelled out rather than left to the
    // generators: a boundary INSIDE a word, twice over. Three deltas, one word.
    const r = buildReasoning("unchar", true);
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("1 word");
    r.append("acteristic");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("1 word");
    r.append("ally");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("1 word");
  });

  it("does not join two words when the boundary lands on the space between them", () => {
    // The mirror of the case above, and the one a fold that always subtracted
    // would break: the trace ends on whitespace, so the next chunk starts a NEW
    // word rather than continuing the previous one.
    const r = buildReasoning("weighing ", true);
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("1 word");
    r.append("the");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("2 words");
  });

  it("holds the count across a delta that is only whitespace", () => {
    // Such a delta ends the open word without adding one, so both halves of the
    // fold's state have to move: the count stays and `openWord` clears.
    const r = buildReasoning("weighing", true);
    r.append("   \n");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("1 word");
    r.append("the");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("2 words");
  });
});
