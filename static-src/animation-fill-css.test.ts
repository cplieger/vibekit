// Does every animation drop off its element when it finishes, and does every
// one that must NOT stay put?
//
// An entry animation in this app declares only `from`, so its last keyframe IS
// the element's own value and a forwards fill changes nothing about how it looks
// while keeping the finished animation attached for the element's whole life.
// That is not only bookkeeping: Chromium will not treat a text run under an
// animated opacity as opaque, so it drops subpixel antialiasing for it. Measured
// as a live defect on the streaming chunk spans, where the reveal buffer emits
// one span per 3-10 characters, so a settled reply held hundreds of finished
// opacity animations and each chunk painted differently from the plain text
// around it. The reasoning and the full classification live at the keyframe
// library (css/03-base.css).
//
// An exit animation is the opposite case: its last keyframe is a state the
// element does not otherwise have, so dropping the fill snaps it back. Both
// directions are here because fixing one by sweeping `both` out of the
// stylesheets would silently break the other, and neither shows up in a
// screenshot.
//
// Every case mounts real markup under the real stylesheet and reads
// `getAnimations()`, so a selector that stops matching fails loudly instead of
// passing on an empty list.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

/** Build `spec` (a tag plus classes and attributes) and return it, optionally
 *  inside `parent`, which is built the same way. */
interface Fixture {
  /** What the stylesheet's selector needs to match, in words. */
  readonly what: string;
  /** `tag.class.class[attr]`, the element under test. */
  readonly el: string;
  /** Ancestor chain, outermost first, when the rule needs one. */
  readonly under?: readonly string[];
}

function build(spec: string): HTMLElement {
  const [head = "div", ...rest] = spec.split(/(?=[.[])/u);
  const node = document.createElement(head === "" ? "div" : head);
  for (const part of rest) {
    if (part.startsWith(".")) {
      node.classList.add(part.slice(1));
      continue;
    }
    const attr = part.slice(1, -1);
    const [name = "", value = ""] = attr.split("=");
    node.setAttribute(name, value.replace(/^"|"$/gu, ""));
  }
  return node;
}

function mount(f: Fixture): HTMLElement {
  const node = build(f.el);
  let host: HTMLElement = stage;
  for (const step of f.under ?? []) {
    const wrap = build(step);
    host.appendChild(wrap);
    host = wrap;
  }
  host.appendChild(node);
  return node;
}

/** Entry animations: the last keyframe is the element's own value, so nothing
 *  may still be attached once the animation has run. */
const ENTRY: readonly Fixture[] = [
  { what: "an appended chat element", el: "div[data-chat-entry]" },
  { what: "a turn card", el: "div.turn[data-chat-entry]" },
  {
    what: "a streamed text chunk",
    el: "span[data-vk-chunk-enter]",
    under: ["div.message.assistant"],
  },
  {
    what: "a completed code block",
    el: "pre[data-vk-block-enter]",
    under: ["div.message.assistant"],
  },
  {
    what: "a completed blockquote",
    el: "blockquote[data-vk-block-enter]",
    under: ["div.message.assistant"],
  },
  { what: "a completed table", el: "table[data-vk-block-enter]", under: ["div.message.assistant"] },
  { what: "a tooltip", el: "div.uip-tooltip" },
  { what: "a model picker card", el: "button.picker-btn" },
  { what: "a transcript boundary", el: "div.boundary" },
  { what: "a tool card", el: "div.tool-call" },
  { what: "a tool group", el: "div.tool-group" },
  { what: "a delegated-work card", el: "div.subagent-block" },
  { what: "a todo checklist", el: "div.todo-list" },
  { what: "a plan card", el: "div.plan-message" },
  { what: "a file browser row", el: "div.fb-row" },
  { what: "a run card", el: "div.run-card" },
  { what: "the shell entering fullscreen", el: "div.shell-panel.shell-fullscreen" },
  // A no-fill entry, as the control that says these assertions are not vacuous:
  // it detaches for a reason that has nothing to do with the sweep.
  { what: "a tab entering the strip", el: "div.tab.entering" },
];

/** Exit animations: the last keyframe is a state the element does not otherwise
 *  have, so the animation MUST still be applied when it ends. */
const EXIT: readonly Fixture[] = [
  { what: "a tab leaving the strip", el: "div.tab.exiting" },
  { what: "a sub-tab merging into its parent", el: "div.tab.exiting.exiting-merge" },
  { what: "the tab drag indicator opening its gap", el: "div.tab-drag-indicator" },
  {
    what: "a dismissed dock card",
    el: "div.dock-outgoing",
    under: ['div.decision-dock[data-dock-phase="leaving"]'],
  },
  {
    what: "a dock card advancing out",
    el: "div.dock-outgoing",
    under: ['div.decision-dock[data-dock-phase="advancing"]'],
  },
  { what: "the shell leaving fullscreen", el: "div.shell-panel.shell-fullscreen-leaving" },
];

describe("an entry animation detaches when it finishes", () => {
  for (const f of ENTRY) {
    it(`drops off ${f.what}`, async () => {
      const node = mount(f);
      const running = node.getAnimations();
      // Guard the assertion below against a selector that no longer matches:
      // with no animation to start with, "none is left" is trivially true.
      expect(running, `the rule reaches ${f.what}`).toHaveLength(1);
      await Promise.all(running.map((a) => a.finished));
      expect(node.getAnimations(), `nothing left attached to ${f.what}`).toEqual([]);
    });
  }
});

describe("an exit animation holds its end state", () => {
  for (const f of EXIT) {
    it(`stays applied to ${f.what}`, async () => {
      const node = mount(f);
      const running = node.getAnimations();
      expect(running, `the rule reaches ${f.what}`).toHaveLength(1);
      await Promise.all(running.map((a) => a.finished));
      expect(node.getAnimations(), `still applied to ${f.what}`).toHaveLength(1);
    });
  }
});

describe("the fade a streamed chunk actually shows", () => {
  it("starts the span invisible, so text does not pop in at full opacity", () => {
    const node = mount({
      what: "a streamed text chunk",
      el: "span[data-vk-chunk-enter]",
      under: ["div.message.assistant"],
    });
    expect(Number(getComputedStyle(node).opacity)).toBeLessThan(1);
  });
});

/** Keyframe names whose only stop is the start, so the animation ends at
 *  whatever the element itself declares. Read off the assembled bundle. */
function fromOnlyKeyframes(css: string): Set<string> {
  const out = new Set<string>();
  for (const m of css.matchAll(/@keyframes\s+([\w-]+)\s*\{/gu)) {
    let depth = 0;
    let body = "";
    for (let i = m.index + m[0].length - 1; i < css.length; i++) {
      if (css[i] === "{") {
        depth++;
      } else if (css[i] === "}") {
        depth--;
        if (depth === 0) {
          body = css.slice(m.index + m[0].length, i);
          break;
        }
      }
    }
    const stops = [...body.matchAll(/(?:^|[{}\s,])(from|to|\d+%)\s*(?=[,{])/gu)].map((s) => s[1]);
    if (stops.length > 0 && stops.every((s) => s === "from" || s === "0%")) {
      out.add(m[1] ?? "");
    }
  }
  return out;
}

/** Every finite `animation` shorthand in the bundle, whitespace-normalized so a
 *  wrapped declaration cannot hide its fill keyword — which is exactly how the
 *  vendored toast progress bar's `both` escaped a line-oriented grep. */
function finiteAnimations(css: string): string[] {
  const out: string[] = [];
  for (const m of css.matchAll(/(?:^|[{;\s])animation\s*:([^;}]*)/gu)) {
    const decl = (m[1] ?? "").split(/\s+/u).filter(Boolean).join(" ");
    if (decl !== "" && decl !== "none" && !decl.includes("infinite")) {
      out.push(decl);
    }
  }
  return out;
}

// The sweep, and the reason it exists beside the per-selector cases above: those
// pin the rules that leaked, and a rule added next month is not among them. This
// asks the question of the WHOLE corpus, so a new entry animation cannot arrive
// carrying a forwards fill.
describe("the whole stylesheet", () => {
  it("never pairs a from-only keyframe with a forwards fill", () => {
    const css = sheet.textContent ?? "";
    const fromOnly = fromOnlyKeyframes(css);
    expect(fromOnly.size, "the keyframe library parsed").toBeGreaterThan(5);

    const offenders = finiteAnimations(css).filter(
      (decl) =>
        /\b(?:both|forwards)\b/u.test(decl) &&
        [...fromOnly].some((name) => new RegExp(`\\b${name}\\b`, "u").test(decl)),
    );
    expect(offenders, "every from-only animation takes `backwards` or no fill").toEqual([]);
  });
});
