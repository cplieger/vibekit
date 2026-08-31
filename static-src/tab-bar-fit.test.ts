// Real-layout tests (Browser Mode): fitTabBar decides label-vs-icon mode by
// measuring truncation, so these tests build a genuinely constrained bar and
// assert the class the CSS keys on.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fitTabBar } from "./tab-bar-fit.js";

const ICONS = "tab-bar-icons";

function buildBar(width: string, labels: readonly string[]): HTMLElement {
  const host = document.createElement("div");
  host.style.width = width;
  const bar = document.createElement("nav");
  bar.className = "settings-tab-bar";
  bar.style.display = "flex";
  for (const label of labels) {
    const btn = document.createElement("button");
    btn.className = "settings-tab";
    // The production rules that make overflow show as truncation rather
    // than wrapping or growth; the stylesheet is not loaded in the runner,
    // so the load-bearing declarations are inlined.
    btn.style.flex = "1";
    btn.style.minWidth = "0";
    btn.style.overflow = "hidden";
    btn.style.whiteSpace = "nowrap";
    const span = document.createElement("span");
    span.className = "settings-tab-label";
    span.textContent = label;
    btn.appendChild(span);
    bar.appendChild(btn);
  }
  host.appendChild(bar);
  document.body.appendChild(host);
  return bar;
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("fitTabBar", () => {
  it("keeps labels when every label fits", () => {
    const bar = buildBar("600px", ["Changes", "Pull requests", "Sources"]);
    fitTabBar(bar);
    expect(bar.classList.contains(ICONS)).toBe(false);
  });

  it("switches the whole bar to icons when any label would truncate", () => {
    const bar = buildBar("120px", ["Steering", "Skills", "Agents", "Specs", "Hooks", "Workflows"]);
    fitTabBar(bar);
    expect(bar.classList.contains(ICONS)).toBe(true);
  });

  it("returns to labels when the bar grows wide enough", async () => {
    const bar = buildBar("120px", ["General", "Tools", "Permissions"]);
    fitTabBar(bar);
    expect(bar.classList.contains(ICONS)).toBe(true);

    // Widen the container; the ResizeObserver re-measures. RO callbacks are
    // delivered async (before paint), so poll briefly.
    (bar.parentElement as HTMLElement).style.width = "600px";
    await vi.waitFor(() => {
      expect(bar.classList.contains(ICONS)).toBe(false);
    });
  });

  it("re-measures in label mode so icon mode does not latch", async () => {
    // A bar measured while in icons mode has display:none labels (in the real
    // CSS) and would never report overflow again; the measure resets to label
    // mode first. Simulate the CSS effect: hide labels whenever the class is
    // present, via a scoped style element.
    const style = document.createElement("style");
    style.textContent = `.${ICONS} .settings-tab-label { display: none; }`;
    document.head.appendChild(style);
    try {
      const bar = buildBar("120px", ["General", "Tools", "Permissions"]);
      fitTabBar(bar);
      expect(bar.classList.contains(ICONS)).toBe(true);

      (bar.parentElement as HTMLElement).style.width = "600px";
      await vi.waitFor(() => {
        expect(bar.classList.contains(ICONS)).toBe(false);
      });

      // And shrink again: labels must yield back to icons.
      (bar.parentElement as HTMLElement).style.width = "120px";
      await vi.waitFor(() => {
        expect(bar.classList.contains(ICONS)).toBe(true);
      });
    } finally {
      style.remove();
    }
  });

  it("stays in icon mode when an icon-mode bar shrinks further", async () => {
    // The measure resets to label mode BEFORE reading: without that, a bar
    // already in icons mode measures its hidden labels (zero width, no
    // overflow) and falls back to labels at a width where they cannot fit.
    const style = document.createElement("style");
    style.textContent = `.${ICONS} .settings-tab-label { display: none; }`;
    document.head.appendChild(style);
    try {
      const bar = buildBar("120px", ["General", "Tools", "Permissions"]);
      fitTabBar(bar);
      expect(bar.classList.contains(ICONS)).toBe(true);

      (bar.parentElement as HTMLElement).style.width = "90px";
      // Outwait the whole pipeline (RO delivery, then the deferred
      // rAF measure) before asserting ONCE: a waitFor on the already-true
      // class would pass at t=0 and never see a wrong flip. Frame order per
      // spec: rAF callbacks run BEFORE RO delivery within a frame, so the
      // measure queued by frame N's RO runs in frame N+1 — four frames
      // covers it with margin.
      for (let i = 0; i < 4; i++) {
        await new Promise((r) => requestAnimationFrame(r));
      }
      expect(bar.classList.contains(ICONS)).toBe(true);
    } finally {
      style.remove();
    }
  });
});
