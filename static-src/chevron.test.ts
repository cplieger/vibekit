// ---------------------------------------------------------------------------
// The disclosure chevron is ONE vocabulary, pinned in both directions.
//
// It was three techniques across eight sites (an SVG swapped in JS, a pair of
// rotated borders, and five `▸`/`▾` font glyphs) disagreeing on the resting
// direction, on whether a glyph appeared when collapsed at all, and on the
// rotation. Convergence is only worth doing once, so these tests guard the two
// ways it rots: a builder that stops using `chevronEl()`, and a stylesheet that
// grows a fourth technique.
//
// The DOM half runs the real builders. The SOURCE half reads the shipped
// stylesheets, because the test page loads no app stylesheet — see
// __test-helpers__/css-rules.ts.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect } from "vitest";
import { loadCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));

import { chevronEl } from "./chevron.js";
import { buildToolGroupShell } from "./tool-group.js";
import { buildSubagentContainer } from "./fundamentals/subagent-block.js";
import { buildReasoning } from "./fundamentals/reasoning.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { buildTurnFooter } from "./fundamentals/turn-footer.js";

/** Every stylesheet that styles a disclosure.
 *
 * `27-run-card.css` and `31-exec-view.css` were missing until 2026-09-03, and
 * that omission is why the exec view shipped a COMPOSED rotation: `.ev-twist`
 * turned the wrapper -90deg while the chevron inside it already carried its own
 * closed -90deg, so a collapsed row pointed UP and an expanded one RIGHT. This
 * suite's angle check reads only the sheets named here, so it saw none of it. */
const SHEETS = [
  "10-shell-app.css",
  "14-tools.css",
  "17-settings.css",
  "22-git-multirepo.css",
  "27-run-card.css",
  "29-turns.css",
  "31-exec-view.css",
  "61-mcp-tools.css",
];

function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//gu, "");
}

describe("chevronEl", () => {
  it("is one span carrying the shared class, one svg, and no accessible name", () => {
    const c = chevronEl();
    expect(c.tagName).toBe("SPAN");
    expect(c.classList.contains("disclosure-chevron")).toBe(true);
    // The control around it already carries the expanded state; a named glyph
    // would announce a second control.
    expect(c.getAttribute("aria-hidden")).toBe("true");
    expect(c.querySelectorAll("svg")).toHaveLength(1);
    // No text: a font triangle is what this replaced.
    expect(c.textContent).toBe("");
  });
});

describe("every disclosure builder emits the shared chevron", () => {
  it("tool group header", () => {
    const g = buildToolGroupShell();
    expect(g.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    // Present in BOTH states. The `content: "▸ "` it replaced existed only on a
    // collapsed header, so an expanded group advertised nothing.
    g.classList.add("tool-group-collapsed");
    expect(g.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
  });

  // A delegate's CARD is not here because it is no longer a disclosure: it renders
  // none of its delegate's output, so there is nothing to fold. The pipeline
  // container is the surviving delegated-work disclosure.
  it("pipeline container, and it survives a status flip", async () => {
    const sa = buildSubagentContainer("orchestrate", "in_progress");
    // With a stage to reveal: a container whose body is empty withdraws its whole
    // control, chevron included, so there is no glyph to be the shared one.
    sa.body.appendChild(document.createElement("div")).textContent = "stage";
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    expect(sa.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    sa.setStatus("completed");
    expect(sa.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
  });

  it("reasoning summary, and sealing rewrites the LABEL not the summary", () => {
    const r = buildReasoning("thinking about it", true, true);
    expect(r.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    // The word count is the summary's other sibling of the label, so it is the
    // second thing a `summary.textContent = …` would delete.
    expect(r.root.querySelectorAll(".reasoning-count")).toHaveLength(1);
    // The defect this guards: `summary.textContent = …` would delete the glyph.
    r.seal();
    expect(r.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("3 words");
  });

  it("turn header fold toggle", () => {
    const h = buildTurnHeader({
      n: 4,
      outcome: "completed",
      ts: Date.now(),
      request: "converge the chevrons",
      attachments: [],
    });
    expect(h.querySelectorAll(".turn-fold-toggle > .disclosure-chevron")).toHaveLength(1);
  });

  // THE TURN FOOTER IS DELIBERATELY NOT IN THIS POPULATION. Its trigger is an `i`
  // (`turn-footer.ts`), because the panel it opens is turn INFORMATION rather than
  // the rest of the row, so it never enters the chevron vocabulary this file
  // governs. Asserted as an ABSENCE, or nothing stops a chevron reappearing beside
  // the `i` — which is exactly what shipped for one commit.
  it("the turn footer carries no chevron at all", () => {
    const f = buildTurnFooter({ commands: 1, reads: 2, changedFiles: {} });
    expect(f.querySelectorAll(".disclosure-chevron")).toHaveLength(0);
    expect(f.querySelectorAll(".turn-ledger-info")).toHaveLength(1);
  });
});

// POSITION CARRIES THE INTERACTION TYPE, and nothing but this asserts it: a
// disclosure chevron leads its header, a navigating one trails. Rotation cannot
// carry the distinction on its own, because a closed disclosure and a navigation
// glyph resolve to the same angle — and a tool card's region is born closed, so
// nearly every card in a transcript shows one. Before the rule, a delegate leaf's
// navigating head and a tool card's closed disclosure were the same glyph at the
// same angle in the same trailing slot.
//
// Asserted on DOM ORDER rather than on geometry, which is what makes it a cheap
// guard on the builders: the boxes measure differently (a centred divider, an
// absolutely positioned tool chevron) and a rect assertion would be a layout test
// wearing a convention's name.
describe("position carries the interaction type", () => {
  /** Which end of `header` the chevron sits at, by child index. */
  function chevronEnd(header: Element): "leading" | "trailing" | "absent" {
    const kids = [...header.children];
    const i = kids.findIndex(
      (k) =>
        k.querySelector(".disclosure-chevron") !== null ||
        k.classList.contains("disclosure-chevron"),
    );
    if (i === -1) {
      return "absent";
    }
    return i === 0 ? "leading" : i === kids.length - 1 ? "trailing" : "absent";
  }

  it("the tool group's disclosure leads", () => {
    const g = buildToolGroupShell();
    expect(chevronEnd(g.querySelector(".tool-group-header")!)).toBe("leading");
  });

  it("the pipeline container's disclosure leads", async () => {
    const sa = buildSubagentContainer("orchestrate", "in_progress");
    sa.body.appendChild(document.createElement("div")).textContent = "stage";
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    expect(chevronEnd(sa.root.querySelector(".subagent-header")!)).toBe("leading");
  });

  it("a delegate LEAF's navigation chevron trails, in the same header class", async () => {
    const { buildSubagentCard } = await import("./fundamentals/subagent-block.js");
    const leaf = buildSubagentCard("context-gatherer", "completed", {
      open: { href: "/chat/c-1/subagent/s-1", open: () => undefined },
    });
    const head = leaf.root.querySelector(".subagent-header")!;
    // Same class as the container's above: the position is the only thing telling a
    // reader that this one opens a page and that one expands in place.
    expect(head.tagName).toBe("A");
    expect(chevronEnd(head)).toBe("trailing");
  });

  it("the run card's disclosure leads", async () => {
    const { buildRunCard } = await import("./fundamentals/run-card.js");
    const rc = buildRunCard("wf-1", "recipe", () => undefined);
    expect(chevronEnd(rc.root.querySelector(".run-head")!)).toBe("leading");
  });

  it("the reasoning trace's disclosure leads", () => {
    const r = buildReasoning("thinking", false, false);
    expect(chevronEnd(r.root.querySelector(".reasoning-summary")!)).toBe("leading");
  });

  // The turn FOLD leads. The turn FOOTER used to be asserted here beside it and is
  // not in this population any more: its trigger is an `i`, so it has no chevron to
  // place. The absence is pinned in the builders block above instead.
  it("the turn fold leads", () => {
    const h = buildTurnHeader({
      n: 4,
      outcome: "completed",
      ts: Date.now(),
      request: "converge the chevrons",
      attachments: [],
    });
    // The header's own children are the meta row and `.turn-req`, so the end to
    // ask about is the ROW's — which is where the toggle sits.
    expect(chevronEnd(h.querySelector(".turn-head-row")!)).toBe("leading");
  });

  it("the tool card's disclosure leads, by CSS rather than by DOM order", async () => {
    // The one site where DOM order cannot answer: the button is appended last (it is
    // built and withdrawn as the card's output comes and goes) and placed by
    // `inset-inline-start`, so the stylesheet is where the rule lives. Read off the
    // shipped sheet, since this page loads no app stylesheet.
    const body = stripComments(loadCSS("14-tools.css"));
    const rule = /\.tool-disclosure\s*\{([^}]*)\}/u.exec(body)?.[1] ?? "";
    expect(rule).toMatch(/inset-inline-start:/u);
    expect(rule).not.toMatch(/inset-inline-end:/u);
  });
});

describe("the stylesheets carry exactly one chevron technique", () => {
  it("declares the base rule once, in the components layer", () => {
    const hits = SHEETS.filter((s) =>
      /^\s*\.disclosure-chevron\s*\{/mu.test(stripComments(loadCSS(s))),
    );
    expect(hits).toEqual(["10-shell-app.css"]);
  });

  it("has no font-glyph triangle left as CSS content", () => {
    const found: string[] = [];
    for (const sheet of SHEETS) {
      const body = stripComments(loadCSS(sheet));
      // `▸ ▾ ▴ ◂` and their \25Bx / \25Cx escapes, as a `content` value.
      if (/content:[^;}]*(?:[\u25B0-\u25CF]|\\25[BC][0-9A-Fa-f])/u.test(body)) {
        found.push(sheet);
      }
    }
    expect(found).toEqual([]);
  });

  it("draws no chevron from a pair of rotated borders", () => {
    const found: string[] = [];
    for (const sheet of SHEETS) {
      const body = stripComments(loadCSS(sheet));
      // The retired shape: adjacent border-right + border-bottom on a tiny box.
      if (
        /border-right:[^;}]*solid currentcolor;\s*border-bottom:[^;}]*solid currentcolor/u.test(
          body,
        )
      ) {
        found.push(sheet);
      }
    }
    expect(found).toEqual([]);
  });

  it("states the open angle as 0deg everywhere, and the closed angle only once", () => {
    const opens: string[] = [];
    let closedDecls = 0;
    for (const sheet of SHEETS) {
      const body = stripComments(loadCSS(sheet));
      for (const m of body.matchAll(/--chev-turn:\s*(-?[\d.]+deg)/gu)) {
        const v = m[1];
        if (v === "0deg") {
          opens.push(sheet);
        } else if (v === "-90deg") {
          closedDecls++;
        } else {
          opens.push(`${sheet}:UNEXPECTED ${String(v)}`);
        }
      }
    }
    expect(opens.filter((o) => o.includes("UNEXPECTED"))).toEqual([]);
    // Every site flips to the same open angle...
    expect(opens.length).toBeGreaterThanOrEqual(6);
    // ...and the closed angle is the base rule's single declaration.
    expect(closedDecls).toBe(1);
  });
});
