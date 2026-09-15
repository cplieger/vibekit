// ONE HOVER GATE FOR EVERY DISCLOSURE TRIGGER IN THE TRANSCRIPT.
//
// A disclosure trigger is the one control class where an ungated `:hover`
// LATCHES: the finger is still on the header when the tap ends, so the wash (or
// the ink lift) stays painted until the next tap elsewhere and a folded box
// reads as hovered for as long as the reader leaves it alone. `14-tools.css`
// states the rule and its reason at `.tool-group-header`; this file is what
// makes it true of the whole population at once, because it was stated at ONE
// rule and six of the nine members had been written against it by hand while
// the three native `<summary>` triggers in `13-messages.css` had not.
//
// `any-hover`, NEVER `hover` (web.md): those queries report only the PRIMARY
// input and iPadOS answers `hover: none` with a trackpad attached, so a
// `hover: hover` gate silently drops the rule on every touch-primary device —
// which is the one direction a gate must not fail in, since it takes the paint
// away from a reader who has a pointer.
//
// A SOURCE read rather than a computed one, for the reason
// `account-btn-css.test.ts` records: a synthetic hover drives no style recalc
// and `CSS.forcePseudoState` is a devtools protocol call, so `getComputedStyle`
// cannot answer a question about a `:hover` rule. The subject is the CASCADE
// anyway — which at-rule a rule sits inside.
//
// THE POPULATION IS A DECLARED TABLE, and that is a limit worth stating rather
// than dressing up. There is no mechanical predicate for "this selector is a
// disclosure trigger": the fact lives in the TypeScript that builds the header
// (a native `<summary>`, `createDisclosure`, `wireRowToggle`), and the CSS
// carries no marker for it — the shared box-header recipe (`var(--c-hover)`)
// is spent by ~21 sites app-wide including plain buttons and links, so keying
// on the declaration would sweep in controls this rule does not govern. What
// the table DOES buy: every row is checked for existence as well as for its
// gate, so a rename or a duplicated rule fails here instead of silently
// dropping coverage. A tenth trigger needs a row.
import { describe, it, expect } from "vitest";

import { manifestSheets } from "./__test-helpers__/css-rules.js";

interface Trigger {
  /** The stylesheet the rule lives in, as `css/MANIFEST` spells it. */
  readonly file: string;
  /** The selector EXACTLY as authored, so a member of a list is findable. */
  readonly selector: string;
  /** What the reader is hovering, for a failure message that names the surface. */
  readonly what: string;
}

const TRIGGERS: readonly Trigger[] = [
  {
    file: "13-messages.css",
    selector: ".reasoning-summary:hover",
    what: "the reasoning trace's <summary>",
  },
  {
    file: "13-messages.css",
    selector: ".code-refs-summary:hover",
    what: "the code-references footnote's <summary>",
  },
  {
    file: "13-messages.css",
    selector: ".compaction-head:hover",
    what: "the compaction break's <summary>",
  },
  {
    file: "14-tools.css",
    selector: ".tool-group-header:hover",
    what: "a tool group's header",
  },
  {
    file: "14-tools.css",
    selector: ".tool-summary.has-disclosure:hover",
    what: "a tool card's summary row",
  },
  {
    // The pipeline CONTAINER's header, a `role="button"` disclosure. Shares its
    // rule with the leaf below; both rows are listed because they are two
    // triggers, and a split that left one behind would still pass on the other.
    file: "14-tools.css",
    selector: ".subagent-container.has-disclosure > .subagent-header:hover",
    what: "a delegate pipeline container's header",
  },
  {
    file: "14-tools.css",
    selector: "a.subagent-header:hover",
    what: "a delegate leaf card's head",
  },
  {
    file: "27-run-card.css",
    selector: ".run-head:hover",
    what: "a run card's head",
  },
  {
    file: "27-run-card.css",
    selector: ".run-step-head:hover",
    what: "a run card's step row",
  },
  {
    file: "29-turns.css",
    selector: ".turn:not([data-running], [data-no-fold]) > .turn-header:hover",
    what: "the turn card's band",
  },
];

/** The gate every trigger must sit inside, and the one it must never sit inside. */
const ANY_HOVER = /^@media\s*\(\s*any-hover\s*:\s*hover\s*\)$/u;
const PRIMARY_ONLY = /\(\s*hover\s*:\s*hover\s*\)/u;

/** Comments blanked rather than deleted, so nothing inside one is read as CSS. */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//gu, (m) => " ".repeat(m.length));
}

/**
 * A selector list split at TOP-LEVEL commas only.
 *
 * `.turn:not([data-running], [data-no-fold]) > .turn-header:hover` is ONE
 * selector holding a comma, so a naive `split(",")` yields two fragments and
 * the row is never found — which is a silent pass, not a failure.
 */
function members(prelude: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < prelude.length; i++) {
    const ch = prelude[i];
    if (ch === "(" || ch === "[") {
      depth++;
    } else if (ch === ")" || ch === "]") {
      depth--;
    } else if (ch === "," && depth === 0) {
      out.push(prelude.slice(start, i).trim());
      start = i + 1;
    }
  }
  out.push(prelude.slice(start).trim());
  return out.filter((s) => s !== "");
}

/**
 * Every rule in the sheet whose selector LIST names `selector`, each with the
 * at-rule preludes enclosing it.
 *
 * A brace walk rather than the CSSOM, because the question is about the authored
 * source: an `@media` the engine does not match is absent from
 * `document.styleSheets`' matched rules, so a gate could only be observed by
 * reading the text.
 */
function rulesNaming(css: string, selector: string): { readonly gates: string[] }[] {
  const text = stripComments(css);
  const found: { gates: string[] }[] = [];
  const open: string[] = [];
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === "{") {
      const prelude = text.slice(start, i).trim();
      if (!prelude.startsWith("@") && members(prelude).includes(selector)) {
        found.push({ gates: open.filter((p) => p.startsWith("@")) });
      }
      open.push(prelude);
      start = i + 1;
    } else if (ch === "}") {
      open.pop();
      start = i + 1;
    } else if (ch === ";") {
      start = i + 1;
    }
  }
  return found;
}

describe("the disclosure trigger hover gate", () => {
  it("gates every transcript disclosure trigger on any-hover, never on hover", () => {
    const sheets = new Map(manifestSheets().map((s) => [s.name, s.css]));
    const offenders: string[] = [];

    for (const { file, selector, what } of TRIGGERS) {
      const css = sheets.get(file);
      if (css === undefined) {
        offenders.push(`${file}: not in css/MANIFEST (${what})`);
        continue;
      }

      const rules = rulesNaming(css, selector);
      if (rules.length !== 1) {
        // Zero means the selector moved or was renamed, so this row covers
        // nothing; more than one means two rules disagree about the gate.
        offenders.push(`${file} ${selector}: ${rules.length} rules name it (${what})`);
        continue;
      }

      const [only] = rules;
      const gates = only?.gates ?? [];
      if (!gates.some((g) => ANY_HOVER.test(g))) {
        offenders.push(
          `${file} ${selector}: NOT gated on @media (any-hover: hover) — ${what} latches its hover after a tap` +
            (gates.length > 0 ? ` (enclosed by ${gates.join(" / ")})` : " (at top level)"),
        );
      }
      if (gates.some((g) => PRIMARY_ONLY.test(g))) {
        offenders.push(
          `${file} ${selector}: gated on the PRIMARY-input query, which drops the rule on every touch-primary device (${what})`,
        );
      }
    }

    expect(offenders, offenders.join("\n")).toEqual([]);
  });
});
