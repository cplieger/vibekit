import { describe, it, expect } from "vitest";
import { formatTokens, formatMetering } from "./status-format.js";

describe("formatTokens", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.0M"],
    [1_500_000, "1.5M"],
    [10_000_000, "10.0M"],
  ];

  it.each(cases)("formatTokens(%d) === %s", (input, expected) => {
    expect(formatTokens(input)).toBe(expected);
  });
});

describe("formatMetering", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [42, "42"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.00M"],
    [1_500_000, "1.50M"],
    [0.5, "0.50"],
    [3.14, "3.14"],
    [99.9, "99.90"],
  ];

  it.each(cases)("formatMetering(%d) === %s", (input, expected) => {
    expect(formatMetering(input)).toBe(expected);
  });
});

// --- The model pill's label ---
//
// The pill names the model, and beside it the reasoning tier WHEN that tier is a
// departure from the model's own default. status.ts is the single writer of both
// spans; the departure test itself is effort.ts's (see effort.test.ts) and this
// module decides nothing about it.

/** Every element updateContextBar writes, so the registry's throwing getters
 *  resolve. The two under test are `ctx-model-pill` and `ctx-effort-pill`. */
function mountContextBar(): void {
  document.body.replaceChildren();
  const btn = document.createElement("button");
  btn.id = "switch-model-btn";
  for (const id of ["ctx-model-pill", "ctx-effort-pill"]) {
    const span = document.createElement("span");
    span.id = id;
    btn.appendChild(span);
  }
  document.body.appendChild(btn);
  for (const id of [
    "context-ring-fill",
    "context-label",
    "ctx-tokens",
    "ctx-credits",
    "ctx-turns",
    "ctx-last-turn",
    "ctx-msgs",
    "ctx-tools",
    "ctx-metering",
  ]) {
    const span = document.createElement("span");
    span.id = id;
    document.body.appendChild(span);
  }
}

/** updateContextBar coalesces into a rAF, so a read has to wait one frame. */
async function paint(model: string, effort: string): Promise<void> {
  const { updateContextBar } = await import("./status.js");
  updateContextBar({
    pct: 0,
    contextSize: 0,
    credits: 0,
    turnCount: 0,
    lastTurnMs: 0,
    model,
    effort,
  });
  await new Promise((r) => {
    requestAnimationFrame(() => {
      r(undefined);
    });
  });
}

describe("the model pill", () => {
  it("names the model alone when the tier is the model's default", async () => {
    mountContextBar();

    await paint("claude-opus-5", "");

    const { $ } = await import("./dom.js");
    expect($.ctxModelPill.textContent).toBe("claude opus 5");
    // Hidden rather than emptied: `.pill` is a flex row with a gap, so an empty
    // span would still pad the pill.
    expect($.ctxEffortPill.textContent).toBe("");
    expect($.ctxEffortPill.classList.contains("hidden")).toBe(true);
    expect($.switchModelBtn.getAttribute("aria-label")).toBe(
      "Switch model, currently claude opus 5",
    );
  });

  it("names the tier beside the model when it is not the default", async () => {
    mountContextBar();

    await paint("claude-opus-5", "max");

    const { $ } = await import("./dom.js");
    // Its OWN span, not appended to the model label: the label is capped at 10rem
    // with an ellipsis, so a concatenated tier is the half that gets clipped.
    expect($.ctxModelPill.textContent).toBe("claude opus 5");
    expect($.ctxEffortPill.textContent).toBe("· max");
    expect($.ctxEffortPill.classList.contains("hidden")).toBe(false);
  });

  it("names the current selection in the button's aria-label", async () => {
    mountContextBar();

    await paint("claude-opus-5", "max");

    // The button's aria-label wins over its own text, so this is the only path
    // the selection reaches assistive tech by. Spelled in words: the separator
    // above is read out.
    const { $ } = await import("./dom.js");
    expect($.switchModelBtn.getAttribute("aria-label")).toBe(
      "Switch model, currently claude opus 5 at max reasoning effort",
    );
  });

  it("clears the tier when a later paint carries none", async () => {
    mountContextBar();

    await paint("claude-opus-5", "max");
    await paint("claude-sonnet-5", "");

    // A model switch changes which default applies, so the span has to empty
    // again rather than keep the previous model's departure.
    const { $ } = await import("./dom.js");
    expect($.ctxModelPill.textContent).toBe("claude sonnet 5");
    expect($.ctxEffortPill.textContent).toBe("");
    expect($.ctxEffortPill.classList.contains("hidden")).toBe(true);
  });

  it("labels an empty model auto and still names a non-default tier", async () => {
    mountContextBar();

    await paint("", "high");

    const { $ } = await import("./dom.js");
    // Empty model = server-side default; "auto" rather than blank.
    expect($.ctxModelPill.textContent).toBe("auto");
    expect($.ctxEffortPill.textContent).toBe("· high");
  });
});
