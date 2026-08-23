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
import { buildSubagentBlock } from "./fundamentals/subagent-block.js";
import { buildReasoning } from "./fundamentals/reasoning.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { buildTurnFooter } from "./fundamentals/turn-footer.js";

/** Every stylesheet that styles a disclosure. */
const SHEETS = [
  "10-shell-app.css",
  "14-tools.css",
  "17-settings.css",
  "22-git-multirepo.css",
  "29-turns.css",
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

  it("subagent card, and it survives a status flip", () => {
    const sa = buildSubagentBlock("context-gatherer", "in_progress");
    expect(sa.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    sa.setStatus("completed");
    expect(sa.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
  });

  it("reasoning summary, and sealing rewrites the LABEL not the summary", () => {
    const r = buildReasoning("thinking about it", true);
    expect(r.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    // The defect this guards: `summary.textContent = …` would delete the glyph.
    r.seal();
    expect(r.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
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

  it("turn footer ledger caret", () => {
    const f = buildTurnFooter({ commands: 1, reads: 2, changedFiles: {} });
    expect(f.querySelectorAll(".disclosure-chevron.turn-ledger-caret")).toHaveLength(1);
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
