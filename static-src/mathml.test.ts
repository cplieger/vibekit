// mathml.ts — the LaTeX-subset to MathML converter.
//
// Two things are asserted everywhere and both are the point of the module:
// the tree is in the MathML NAMESPACE (an XHTML `math` element has the same
// tagName and renders as text), and anything outside the subset degrades to
// null so the caller keeps the raw string it already has.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { latexToMathML, MATHML_NS } from "./mathml.js";

/** A compact `tag(child,child)` rendering of the tree, so a case can state the
 *  shape it expects without a nest of expect() calls. */
function shape(el: Element): string {
  const kids = [...el.children];
  const name = el.tagName.toLowerCase();
  if (kids.length === 0) {
    return `${name}[${el.textContent}]`;
  }
  return `${name}(${kids.map(shape).join(",")})`;
}

function convert(src: string, display = false): Element {
  const out = latexToMathML(src, display);
  expect(out).not.toBeNull();
  return out as Element;
}

describe("latexToMathML: the supported subset", () => {
  const cases: { name: string; src: string; display?: boolean; want: string }[] = [
    // The atoms.
    { name: "a number is mn", src: "42", want: "math(mn[42])" },
    { name: "a decimal stays one number", src: "3.14", want: "math(mn[3.14])" },
    { name: "a letter is mi", src: "x", want: "math(mi[x])" },
    { name: "an operator is mo", src: "+", want: "math(mo[+])" },
    {
      name: "a run is an mrow in order",
      src: "a + 1",
      want: "math(mrow(mi[a],mo[+],mn[1]))",
    },
    // Superscripts and subscripts, braced and bare.
    { name: "bare superscript", src: "x^2", want: "math(msup(mi[x],mn[2]))" },
    { name: "braced superscript", src: "x^{2n}", want: "math(msup(mi[x],mrow(mn[2],mi[n])))" },
    { name: "bare subscript", src: "a_i", want: "math(msub(mi[a],mi[i]))" },
    {
      name: "both, sub first",
      src: "x_i^2",
      want: "math(msubsup(mi[x],mi[i],mn[2]))",
    },
    {
      name: "both, sup first, same element",
      src: "x^2_i",
      want: "math(msubsup(mi[x],mi[i],mn[2]))",
    },
    // Fractions.
    { name: "frac with braces", src: "\\frac{a}{b}", want: "math(mfrac(mi[a],mi[b]))" },
    // TeX's one-token argument rule: `\frac12` is `\frac{1}{2}`, not
    // `\frac{12}{?}`. The tokenizer groups digit runs, so parseArg splits.
    { name: "frac without braces", src: "\\frac12", want: "math(mfrac(mn[1],mn[2]))" },
    {
      name: "a bare superscript takes one digit",
      src: "x^12",
      want: "math(mrow(msup(mi[x],mn[1]),mn[2]))",
    },
    { name: "dfrac is a frac", src: "\\dfrac{1}{2}", want: "math(mfrac(mn[1],mn[2]))" },
    {
      name: "nested frac",
      src: "\\frac{\\frac{a}{b}}{c}",
      want: "math(mfrac(mfrac(mi[a],mi[b]),mi[c]))",
    },
    // Roots.
    { name: "sqrt", src: "\\sqrt{x}", want: "math(msqrt(mi[x]))" },
    { name: "nth root", src: "\\sqrt[3]{x}", want: "math(mroot(mi[x],mn[3]))" },
    // Sums: stacked in display, beside in inline. Same source, two shapes.
    {
      name: "sum limits stack in display mode",
      src: "\\sum_{i=1}^{n}",
      display: true,
      want: "math(munderover(mo[\u2211],mrow(mi[i],mo[=],mn[1]),mi[n]))",
    },
    {
      name: "sum limits sit beside the operator inline",
      src: "\\sum_{i=1}^{n}",
      display: false,
      want: "math(msubsup(mo[\u2211],mrow(mi[i],mo[=],mn[1]),mi[n]))",
    },
    {
      name: "lim takes an under-script in display mode",
      src: "\\lim_{x}",
      display: true,
      want: "math(munder(mo[lim],mi[x]))",
    },
    {
      name: "an integral keeps side limits even in display mode",
      src: "\\int_a^b",
      display: true,
      want: "math(msubsup(mo[\u222b],mi[a],mi[b]))",
    },
    // Greek letters and named symbols.
    { name: "lowercase greek", src: "\\alpha", want: "math(mi[\u03b1])" },
    { name: "uppercase greek", src: "\\Omega", want: "math(mi[\u03a9])" },
    { name: "infinity is an identifier", src: "\\infty", want: "math(mi[\u221e])" },
    { name: "a relation is an operator", src: "\\leq", want: "math(mo[\u2264])" },
    {
      name: "greek in an expression",
      src: "\\pi r^2",
      want: "math(mrow(mi[\u03c0],msup(mi[r],mn[2])))",
    },
    // Functions and text.
    {
      name: "a function name is an upright multi-char mi",
      src: "\\log n",
      want: "math(mrow(mi[log],mi[n]))",
    },
    { name: "text is mtext", src: "\\text{if}", want: "math(mtext[if])" },
    { name: "mathrm is an upright mi", src: "\\mathrm{d}", want: "math(mi[d])" },
    // Fences and spacing.
    {
      name: "left/right fences",
      src: "\\left( x \\right)",
      want: "math(mrow(mo[(],mi[x],mo[)]))",
    },
    { name: "the null fence renders nothing", src: "\\left.", want: "math(mrow[])" },
    { name: "a thin space is mspace", src: "\\,", want: "math(mspace[])" },
    // A real expression an agent writes in a coding chat.
    {
      name: "a complexity bound",
      src: "O(n \\log n)",
      want: "math(mrow(mi[O],mo[(],mi[n],mi[log],mi[n],mo[)]))",
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(shape(convert(tc.src, tc.display ?? false))).toBe(tc.want);
    });
  }

  it("puts every node in the MathML namespace", () => {
    const math = convert("\\sum_{i=1}^{n} \\frac{1}{i^2} \\leq \\alpha", true);
    expect(math.namespaceURI).toBe(MATHML_NS);
    for (const node of math.querySelectorAll("*")) {
      expect(node.namespaceURI).toBe(MATHML_NS);
    }
  });

  it("marks display mode on the math element and nowhere else", () => {
    expect(convert("x", true).getAttribute("display")).toBe("block");
    expect(convert("x", false).hasAttribute("display")).toBe(false);
  });
});

describe("latexToMathML: degradation to the raw string", () => {
  // Null is the signal the caller keeps the LaTeX it already rendered as text.
  // Each of these is a real construct an agent writes, so each is a case a
  // reader will actually see degrade.
  const rejected: { name: string; src: string }[] = [
    { name: "a matrix environment", src: "\\begin{pmatrix} a & b \\end{pmatrix}" },
    { name: "an alignment separator", src: "a & b" },
    { name: "a line break", src: "a \\\\ b" },
    { name: "an unknown command", src: "\\overbrace{x}" },
    { name: "an accent this converter does not carry", src: "\\hat{x}" },
    { name: "an unbalanced open brace", src: "\\frac{a}{b" },
    { name: "a stray close brace", src: "a}" },
    { name: "a frac missing its second argument", src: "\\frac{a}" },
    { name: "a script with no base", src: "^2" },
    { name: "a macro parameter", src: "#1" },
    { name: "a comment character", src: "50% of x" },
    { name: "a trailing lone backslash", src: "x \\" },
    { name: "an empty expression", src: "" },
    { name: "whitespace only", src: "   " },
    { name: "a left with no delimiter", src: "\\left" },
    { name: "a text command with no argument", src: "\\text x" },
  ];

  for (const tc of rejected) {
    it(`rejects ${tc.name}`, () => {
      expect(latexToMathML(tc.src, false)).toBeNull();
    });
  }

  it("rejects an expression longer than the cap rather than converting it", () => {
    expect(latexToMathML("x".repeat(5000), false)).toBeNull();
  });

  it("rejects deeply nested groups instead of overflowing the stack", () => {
    const deep = "{".repeat(200) + "x" + "}".repeat(200);
    expect(latexToMathML(deep, false)).toBeNull();
  });

  it("degrades the WHOLE expression, never half of it", () => {
    // The supported prefix must not survive on its own: a formula that renders
    // its first half and silently drops the rest is worse than a raw string.
    expect(latexToMathML("\\frac{a}{b} + \\begin{cases} x \\end{cases}", false)).toBeNull();
  });
});

// A converter is a parser, so it gets a property test with a real invariant
// rather than a crash-only sweep: it either returns a namespaced <math> or it
// returns null, and it never throws on arbitrary input.
describe("latexToMathML fuzz", () => {
  const latexish = fc.stringMatching(/^[a-zA-Z0-9\\{}^_+\-*/=().,[\]|&#% ]*$/);

  it("never throws, and every result is a namespaced math element", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(latexish, fc.boolean(), (src, display) => {
        const out = latexToMathML(src, display);
        if (out === null) {
          return true;
        }
        if (out.namespaceURI !== MATHML_NS || out.tagName.toLowerCase() !== "math") {
          return false;
        }
        for (const node of out.querySelectorAll("*")) {
          if (node.namespaceURI !== MATHML_NS) {
            return false;
          }
        }
        return true;
      }),
      { numRuns: 600 },
    );
    expect(result.failed).toBe(false);
  });

  it("never throws on arbitrary unicode", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string({ minLength: 0, maxLength: 400 }), (src) => {
        latexToMathML(src, false);
        return true;
      }),
      { numRuns: 400 },
    );
    expect(result.failed).toBe(false);
  });

  it("converts a rendered expression back to text that carries every atom", () => {
    // Structural invariant rather than a shape assertion: whatever tree comes
    // out, its text must contain each number the source named. A converter that
    // dropped an argument would pass a "did not throw" check and fail this.
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.array(fc.integer({ min: 0, max: 999 }), { minLength: 1, maxLength: 6 }),
        (nums) => {
          const src = nums.map(String).join(" + ");
          const out = latexToMathML(src, false);
          if (out === null) {
            return false;
          }
          const text = out.textContent;
          return nums.every((n) => text.includes(String(n)));
        },
      ),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });
});
