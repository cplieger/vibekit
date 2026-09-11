// The depth ladder as RENDERED, over markup the production builders produced.
// `chat-depth.node.test.ts` reads the declarations; this reads what the browser
// paints, which is the half that catches a later stylesheet repainting a box onto
// the card's rung.
//
// The measurement goes through a canvas because Chromium resolves a var-driven
// `oklch()` to the authored `oklch(...)` form in `getComputedStyle`, so the string
// cannot be compared numerically, while `fillStyle` parses it and `getImageData`
// hands back the sRGB bytes it really painted.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

// The builders' import graph reaches the shared DOM registry, which throws on a
// missing app root, so these ids exist before the dynamic imports below.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
const { buildToolCard } = await import("./tool-card.js");

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-theme");
});

afterEach(() => {
  host.replaceChildren();
  document.documentElement.removeAttribute("data-theme");
});

/** Dark is the unattributed default; light is the one keyed block. */
function setTheme(theme: "dark" | "light"): void {
  if (theme === "dark") {
    document.documentElement.removeAttribute("data-theme");
    return;
  }
  document.documentElement.dataset["theme"] = theme;
}

const canvas = document.createElement("canvas");
canvas.width = 1;
canvas.height = 1;
const ctx = canvas.getContext("2d", { willReadFrequently: true });

/** The sRGB bytes Chromium paints for a computed colour string. */
function bytes(colour: string): [number, number, number] {
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  ctx.clearRect(0, 0, 1, 1);
  ctx.fillStyle = colour;
  // A string the parser rejects leaves fillStyle at its previous value, so a
  // typo would silently measure the last colour instead of failing.
  expect(ctx.fillStyle, `Chromium parses ${colour}`).not.toBe("#000000");
  ctx.fillRect(0, 0, 1, 1);
  const d = ctx.getImageData(0, 0, 1, 1).data;
  return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0];
}

/** WCAG relative luminance. */
function luminance(colour: string): number {
  const lin = bytes(colour).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * (lin[0] ?? 0) + 0.7152 * (lin[1] ?? 0) + 0.0722 * (lin[2] ?? 0);
}

function bg(el: Element): string {
  return getComputedStyle(el).backgroundColor;
}

/** A token read off `:root`, as the theme in force resolves it. */
function token(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/** A settled read card, as `messages-tools.ts` builds one. */
function card(i: number): HTMLElement {
  return buildToolCard({
    id: `t${String(i)}`,
    title: "Run Command",
    kind: "execute",
    status: "completed",
    live: false,
    input: { command: `go build ./cmd/x${String(i)}` },
  });
}

/** A real group with two real members, header refreshed, mounted in the host. */
function group(): HTMLElement {
  const shell = buildToolGroupShell();
  const body = groupBody(shell);
  body.append(card(0), card(1));
  host.append(shell);
  refreshGroupHeader(shell);
  return shell;
}

describe("the rendered transcript ladder", () => {
  for (const theme of ["dark", "light"] as const) {
    it(`separates the page, the turn card and a box inside it in ${theme}`, () => {
      setTheme(theme);
      const page = luminance(token("--c-bg-primary"));
      const cardRung = luminance(token("--c-turn-body"));
      const boxRung = luminance(token("--c-bg-secondary"));

      // Three distinct rungs; two of them sharing a value is the collapse.
      expect(new Set([page, cardRung, boxRung]).size, `${theme}: three rungs`).toBe(3);

      // Away from the page: lighter in dark, darker in light. The "inverted, same
      // ladder" claim as an ordering, so a rung drifting past another fails here.
      const away = (x: number): number => Math.abs(x - page);
      expect(away(cardRung), `${theme}: the card is off the page`).toBeGreaterThan(0);
      expect(away(boxRung), `${theme}: a box is further out than the card`).toBeGreaterThan(
        away(cardRung),
      );
      const dir = theme === "dark" ? 1 : -1;
      expect(dir * (cardRung - page), `${theme}: the card steps the right way`).toBeGreaterThan(0);
      expect(dir * (boxRung - cardRung), `${theme}: the box steps the same way`).toBeGreaterThan(0);
    });

    it(`paints a tool group off the turn card's own fill in ${theme}`, () => {
      setTheme(theme);
      const turn = document.createElement("div");
      turn.className = "turn";
      host.append(turn);
      const shell = group();
      turn.append(shell);

      // The cascade rather than the tokens: real elements, not declarations.
      expect(bg(shell), `${theme}: the group does not share the card's fill`).not.toBe(bg(turn));
      expect(
        Math.abs(luminance(bg(shell)) - luminance(bg(turn))),
        `${theme}: the two fills are separated`,
      ).toBeGreaterThan(0.001);
    });
  }

  it("gives the two box headers in a group ONE hover fill to land on", () => {
    // One recipe resolves to one colour only if both headers sit on the same
    // parent fill. Their `:hover` values are `chat-depth.node.test.ts`'s; this
    // pins the surface underneath them.
    setTheme("dark");
    const shell = group();
    const header = shell.querySelector(".tool-group-header");
    const summary = shell.querySelector(".tool-summary");
    expect(header, "group header exists").not.toBeNull();
    expect(summary, "member summary exists").not.toBeNull();
    const under = (el: Element): string => {
      for (let p: Element | null = el; p !== null; p = p.parentElement) {
        const c = bg(p);
        if (c !== "rgba(0, 0, 0, 0)" && c !== "transparent") {
          return c;
        }
      }
      throw new Error("no painted ancestor");
    };
    expect(under(header as Element)).toBe(under(summary as Element));
  });
});
