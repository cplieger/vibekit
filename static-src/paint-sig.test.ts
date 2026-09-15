// The signature guard: the two answers, plus the one way an implementation gets the
// question wrong — a separator inside a component making two states compare equal.

import { describe, it, expect } from "vitest";
import { paintIfChanged, sigChanged, wireSignature } from "./paint-sig.js";

function host(): HTMLElement {
  return document.createElement("div");
}

/** A build thunk that records how many times it ran, so "did not paint" is
 *  observable as construction that never happened rather than only as unchanged
 *  output — an unchanged subtree has to cost nothing, not merely look the same. */
function counting(text: string): { build: () => Node[]; calls: () => number } {
  let calls = 0;
  return {
    build: () => {
      calls++;
      const span = document.createElement("span");
      span.textContent = text;
      return [span];
    },
    calls: () => calls,
  };
}

describe("paintIfChanged", () => {
  it("paints a host that has never been painted", () => {
    const h = host();
    const b = counting("one");

    expect(paintIfChanged(h, ["a"], b.build)).toBe(true);
    expect(b.calls()).toBe(1);
    expect(h.textContent).toBe("one");
  });

  it("skips a repeat, and does not even construct the children", () => {
    const h = host();
    const b = counting("one");
    paintIfChanged(h, ["a", "b"], b.build);
    const child = h.firstElementChild;

    expect(paintIfChanged(h, ["a", "b"], b.build)).toBe(false);

    expect(b.calls()).toBe(1);
    // Identity, not content: a rebuilt child holding the same text is what the guard
    // exists to prevent, and content cannot tell the two apart.
    expect(h.firstElementChild).toBe(child);
  });

  it("paints again once any part moves", () => {
    const h = host();
    paintIfChanged(h, ["a", "b"], counting("one").build);
    const child = h.firstElementChild;
    const b = counting("two");

    expect(paintIfChanged(h, ["a", "c"], b.build)).toBe(true);

    expect(b.calls()).toBe(1);
    expect(h.firstElementChild).not.toBe(child);
    expect(h.textContent).toBe("two");
  });

  // The reason this goes through keyenc rather than a template literal. With a "|"
  // separator both of these render as `a|b|c`, so the second state reads as the first
  // and the subtree never repaints — a stale row with nothing to explain it.
  it("cannot be fooled by a component that contains the separator", () => {
    const h = host();
    paintIfChanged(h, ["a|b", "c"], counting("one").build);
    const b = counting("two");

    expect(paintIfChanged(h, ["a", "b|c"], b.build)).toBe(true);
    expect(b.calls()).toBe(1);
  });

  // Same property with keyenc's OWN separator, which is the one an implementation
  // detail could plausibly leak.
  it("cannot be fooled by a component containing keyenc's separator either", () => {
    const h = host();
    paintIfChanged(h, ["a:b", "c"], counting("one").build);
    const b = counting("two");

    expect(paintIfChanged(h, ["a", "b:c"], b.build)).toBe(true);
    expect(b.calls()).toBe(1);
  });

  it("treats an empty part as a value, not as an absent one", () => {
    const h = host();
    paintIfChanged(h, ["", "x"], counting("one").build);
    const b = counting("two");

    expect(paintIfChanged(h, ["x", ""], b.build)).toBe(true);
    expect(b.calls()).toBe(1);
  });
});

describe("sigChanged", () => {
  // For a caller whose repaint is not one `replaceChildren` — it writes several
  // regions, or swaps one child in place — so the guard sits at the top of its own
  // function rather than wrapping a node list.
  it("answers the same question and paints nothing", () => {
    const h = host();
    h.appendChild(document.createElement("span"));

    expect(sigChanged(h, ["a"])).toBe(true);
    expect(sigChanged(h, ["a"])).toBe(false);
    expect(sigChanged(h, ["b"])).toBe(true);
    // Untouched throughout: recording a signature is not a paint.
    expect(h.children.length).toBe(1);
  });

  it("shares its record with paintIfChanged, so the two cannot disagree", () => {
    const h = host();
    paintIfChanged(h, ["a"], counting("one").build);

    expect(sigChanged(h, ["a"])).toBe(false);
  });
});

describe("wireSignature", () => {
  // Total by construction, which is the point: enumerating a wire type's fields by
  // hand is the fragile half of a signature guard, and a wire type grows without the
  // guard being touched.
  it("signs two decodings of the same record identically", () => {
    const bytes = '{"number":7,"title":"add a thing","draft":false}';
    const a = JSON.parse(bytes) as object;
    const b = JSON.parse(bytes) as object;

    expect(wireSignature(a)).toBe(wireSignature(b));
  });

  it("signs differently for any field that moved, named or not", () => {
    const base = JSON.parse('{"number":7,"title":"t","draft":false}') as Record<string, unknown>;
    const moved = JSON.parse('{"number":7,"title":"t","draft":true}') as Record<string, unknown>;
    // A field the guard's author never enumerated, which is the case it exists for.
    const grown = JSON.parse('{"number":7,"title":"t","draft":false,"new_field":1}') as Record<
      string,
      unknown
    >;

    expect(wireSignature(moved)).not.toBe(wireSignature(base));
    expect(wireSignature(grown)).not.toBe(wireSignature(base));
  });
});
