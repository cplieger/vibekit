//
// THE COMPOSER'S FOCUS TREATMENT, TESTED AS AN EVENT ORDER.
//
// The bug: `.prompt-box:focus-within` made the accent border a function of
// whether ANY descendant held focus, and a press on a descendant that cannot
// hold focus drops focus to <body> on the way down and restores it on the way
// up. The order is mousedown -> blur -> mouseup -> click, so the border went
// off-then-on-again inside one gesture — a flicker, not a state change.
//
// The reported instance was the "Supervised mode" row in the `+` menu, the one
// <label> in a card whose three siblings are <button>s. It was never one row's
// bug: `#context-card` is built entirely from spans, and `.prompt-pills` own
// padding is not focusable either. So a snapshot of one row proves nothing; what
// has to be proved is that the predicate the border keys on does not ROUND TRIP
// across the gesture, for a press on a non-focusable descendant.
//
// Two halves, and both are needed. The source half reads which predicate the
// stylesheet actually uses, because the test page loads no app stylesheet. The event
// half drives the real order against the real menu and evaluates that predicate
// with `matches()` at every step — the browser supports `:has(… :focus)`, so
// this is the shipped selector being asked the question, not a re-description of
// it. `:focus-within` is modelled by hand (the assertion is about the authored
// rule, not about the live selector) and
// labelled as such; it is here to show the round trip the fix removes.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";

const { setSupervisedDispatch, collapseAll } = vi.hoisted(() => ({
  setSupervisedDispatch: vi.fn(),
  collapseAll: vi.fn(),
}));

let activeID = "";
let supervised: boolean | undefined;

vi.mock("./store.js", () => ({
  activeSession: {
    peek: () => (activeID === "" ? undefined : { id: activeID, supervised_mode: supervised }),
    get value() {
      return activeID === "" ? undefined : { id: activeID, supervised_mode: supervised };
    },
  },
  isThinking: () => false,
  // The tangent row is disabled on an empty chat, and this file drives the menu
  // for a chat that has one, so every row it presses is a live one.
  isEmptyChat: () => false,
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./pill-expand.js", () => ({ makeExpandable: vi.fn(), collapseAll }));
vi.mock("./files-picker.js", () => ({ openFilePicker: vi.fn() }));
vi.mock("./chat.js", () => ({ openTangentChat: vi.fn() }));
vi.mock("./toast.js", () => ({ error: vi.fn(), success: vi.fn(), info: vi.fn() }));
vi.mock("./actions/chat.js", () => ({ setSupervised: { dispatch: setSupervisedDispatch } }));
vi.mock("./submit.js", () => ({ submitPrompt: vi.fn() }));

/** The selector the border keys on, read out of the shipped stylesheet. */
function focusSelector(): string {
  const body = ruleBody(loadCSS("15-input.css"), ".prompt-box");
  const m = /&(:[^\s{]+)\s*\{[^}]*border-color:\s*var\(--c-accent\)/.exec(body);
  expect(
    m,
    "expected .prompt-box to light its border from ONE nested state selector",
  ).not.toBeNull();
  return `.prompt-box${m![1]}`;
}

/** `:focus-within`, hand-modelled, so the assertion is about the authored rule
 *  rather than about a live selector match on a
 *  focused descendant, so the OLD behaviour has to be expressed rather than
 *  queried. This IS its definition — some descendant is the active element. */
function focusWithinModel(box: Element): boolean {
  const active = document.activeElement;
  return active !== null && active !== document.body && box.contains(active);
}

async function mount(): Promise<{
  box: HTMLElement;
  input: HTMLTextAreaElement;
  card: HTMLElement;
}> {
  document.body.innerHTML = `
    <div id="prompt-box" class="prompt-box">
      <textarea id="prompt-input"></textarea>
      <div class="prompt-pills">
        <span class="pill-slot">
          <button id="chat-options-btn" class="pill pill-expandable" type="button"></button>
          <span id="chat-options-card" class="pill-expand-content chat-options-card"></span>
        </span>
      </div>
    </div>
  `;
  vi.resetModules();
  const mod = await import("./chat-options.js");
  mod.initChatOptions();
  return {
    box: document.getElementById("prompt-box") as HTMLElement,
    input: document.getElementById("prompt-input") as HTMLTextAreaElement,
    card: document.getElementById("chat-options-card") as HTMLElement,
  };
}

/** The supervised row's <label>, found the way a user finds it. */
function supervisedLabel(card: HTMLElement): HTMLLabelElement {
  const label = [...card.querySelectorAll("label.chat-opt-row")].find((l) =>
    (l.textContent ?? "").includes("Supervised mode"),
  );
  if (label === undefined) {
    throw new Error("no Supervised mode row");
  }
  return label as HTMLLabelElement;
}

beforeEach(() => {
  vi.clearAllMocks();
  activeID = "c-active";
  supervised = false;
});

describe("the composer's focus treatment", () => {
  it("keys on the message box, not on any descendant", () => {
    const selector = focusSelector();
    expect(
      selector,
      "the border must key on the message box's own focus. `:focus-within` is what it was, and it is " +
        "true for every focusable descendant — so every press on a NON-focusable one flickers it, " +
        "which is one bug for the label row, the context card's spans and the pill row's padding alike.",
    ).toBe('.prompt-box:has([id="prompt-input"]:focus)');
    expect(
      loadCSS("15-input.css").replace(/\/\*[\s\S]*?\*\//g, " "),
      "no rule may reintroduce :focus-within on the prompt box",
    ).not.toMatch(/\.prompt-box[^{]*:focus-within|&:focus-within/);
  });

  it("does not round-trip across mousedown -> blur -> mouseup -> click on the label", async () => {
    const { box, input, card } = await mount();
    const selector = focusSelector();
    const label = supervisedLabel(card);
    const checkbox = card.querySelector<HTMLInputElement>("#chat-opt-supervised");
    expect(checkbox, "the supervised row must carry its checkbox").not.toBeNull();

    // The gesture starts with focus in the message box — the state the user was
    // in when they noticed the border going away.
    input.focus();
    const lit: boolean[] = [];
    const litOld: boolean[] = [];
    const sample = (): void => {
      lit.push(box.matches(selector));
      litOld.push(focusWithinModel(box));
    };
    sample();

    // mousedown. The browser walks up from the label for a mouse-focusable
    // ancestor, finds none, and clears focus to <body>. Modelled explicitly
    // because a synthetic pointer event moves no focus; this IS the step
    // the diagnosis names.
    label.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    input.blur();
    sample();

    // mouseup, then the click the label forwards to its checkbox, which takes
    // focus — the "until release" half of the report.
    label.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true }));
    checkbox!.checked = true;
    checkbox!.focus();
    checkbox!.dispatchEvent(new Event("change", { bubbles: true }));
    sample();

    // The interaction still works: the row toggled and persisted.
    expect(
      setSupervisedDispatch,
      "the click must still reach the supervised action",
    ).toHaveBeenCalledWith({
      chatID: "c-active",
      enabled: true,
    });

    // The border must be LIT at the start, or the rest of this proves nothing:
    // a selector that is false throughout cannot round-trip either, and reverting
    // to a live `:focus-within` match would make the
    // round-trip check below pass vacuously.
    expect(
      lit[0],
      "the border must be lit while the message box holds focus, or the round-trip check below is vacuous",
    ).toBe(true);

    // The old predicate round-trips — true, false, true — which is the flicker.
    expect(
      litOld,
      "the :focus-within model must reproduce the reported flicker, or this test is not exercising the bug",
    ).toEqual([true, false, true]);

    // The shipped selector does not. It goes false when the message box genuinely
    // loses focus and stays there; no value appears, disappears and reappears
    // inside one gesture.
    const roundTrips = lit.some(
      (v, i) => i > 0 && i < lit.length - 1 && !v && lit[i - 1] && lit[i + 1],
    );
    expect(
      roundTrips,
      `the border's state across the gesture was ${JSON.stringify(lit)}; a false between two trues is ` +
        `the flicker, whatever the endpoints are`,
    ).toBe(false);
  });

  it("does not round-trip on a press inside the context card either", async () => {
    // Same class, no <label> and no checkbox anywhere: #context-card is spans.
    // It has no press affordance inviting the gesture, which is the only reason
    // nobody reported it.
    const { box, input } = await mount();
    const selector = focusSelector();
    const row = document.createElement("span");
    row.className = "pill-ctx-label";
    row.textContent = "Tokens used";
    box.querySelector(".prompt-pills")!.appendChild(row);

    input.focus();
    expect(box.matches(selector)).toBe(true);
    row.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    input.blur();
    const during = box.matches(selector);
    row.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true }));
    row.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));

    // Nothing refocuses the box, so the state after release equals the state
    // during the press: one transition, not two.
    expect(
      box.matches(selector),
      "a press on a span must not restore the border it just took away",
    ).toBe(during);
  });
});
