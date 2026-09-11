import { afterEach, describe, expect, it } from "vitest";

import { snapIcons } from "./icon-crisp.js";

const SVG_NS = "http://www.w3.org/2000/svg";

/** An `ic-ui` icon: the 24-unit grid at 16px, so a multiple-of-3 coordinate wants phase 0.5.
 *  Sized inline because `03-base.css` is not loaded here — the tier's SCALE is the input. */
function icon(): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.style.inlineSize = "16px";
  svg.style.blockSize = "16px";
  svg.style.display = "block";
  const p = document.createElementNS(SVG_NS, "path");
  p.setAttribute("d", "M6 9l6 6 6-6");
  svg.append(p);
  return svg;
}

/** Places the icon at a deliberately fractional offset, optionally inside a rotated box. */
function mount(rotateDeg: number | null): { host: HTMLElement; svg: SVGSVGElement } {
  const host = document.createElement("div");
  host.style.position = "absolute";
  // Fractional on BOTH axes and different per axis, so a correction applied to the wrong
  // one cannot accidentally satisfy the assertion.
  host.style.left = "10.3px";
  host.style.top = "20.7px";
  const svg = icon();
  if (rotateDeg === null) {
    host.append(svg);
  } else {
    const wrap = document.createElement("span");
    wrap.className = "disclosure-chevron";
    wrap.style.display = "grid";
    wrap.style.placeItems = "center";
    wrap.style.transform = `rotate(${String(rotateDeg)}deg)`;
    wrap.append(svg);
    host.append(wrap);
  }
  document.body.append(host);
  return { host, svg };
}

const frac = (v: number): number => ((v % 1) + 1) % 1;

/** Distance from the target phase, on the shorter way round. 0 is crisp. */
function offTarget(svg: SVGSVGElement): number {
  const r = svg.getBoundingClientRect();
  let worst = 0;
  for (const p of [frac(r.x), frac(r.y)]) {
    let d = Math.abs(p - 0.5);
    if (d > 0.5) {
      d = 1 - d;
    }
    worst = Math.max(worst, d);
  }
  return worst;
}

const hosts: HTMLElement[] = [];
function place(rotateDeg: number | null): SVGSVGElement {
  const { host, svg } = mount(rotateDeg);
  hosts.push(host);
  return svg;
}

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

describe("snapIcons", () => {
  it("puts an unrotated icon box on the phase its own scale implies", () => {
    const svg = place(null);
    expect(offTarget(svg)).toBeGreaterThan(0.1);
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("snaps an icon a rotated ancestor holds, measured in SCREEN space", () => {
    // The whole defect: `translate` composes INSIDE the wrapper's rotation, so a screen-space
    // correction written straight onto the box lands on the other axis. Only a screen-space
    // assertion can see it — the inline value looks plausible either way.
    const svg = place(-90);
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
  });

  it("snaps at every right angle, not just the two the app happens to use today", () => {
    for (const deg of [90, 180, 270, -180]) {
      const svg = place(deg);
      snapIcons();
      expect(offTarget(svg), `rotate(${String(deg)}deg)`).toBeLessThan(0.02);
    }
  });

  it("declines an icon no translate could make crisp", () => {
    // At 30deg the strokes cross the grid diagonally, so there is no phase that helps, and
    // the box's screen AABB is not its box — the phase read itself would be meaningless.
    const svg = place(30);
    snapIcons();
    expect(svg.style.translate).toBe("");
  });

  it("converges rather than chasing its own offset when a snapped icon moves", () => {
    // The offset in force is a SCREEN delta while the value written is a LOCAL one; reading
    // the box back means subtracting the screen one, or every later pass compounds the error.
    const svg = place(-90);
    snapIcons();
    const host = svg.closest("div");
    expect(host).not.toBeNull();
    if (host instanceof HTMLElement) {
      host.style.left = "44.85px";
    }
    snapIcons();
    expect(offTarget(svg)).toBeLessThan(0.02);
    // A second pass over an already-converged icon must move nothing at all.
    expect(snapIcons()).toBe(0);
  });
});
