// @vitest-environment happy-dom
// The chat-actions menu: four rows, each of which must do what it says.
//
// What is worth pinning here is the DISPATCH each row makes, because three of the
// four are the only door to a server verb and the fourth is a modal open. The
// card is built entirely in TS (index.html carries an empty one), so this file
// also stands in for a markup test of it.
//
// The goal row gets the most attention, because it is the one row whose output is
// consumed by a PARSER rather than by a handler of ours. So these tests run KAS's
// own parser (transcribed below) over the exact string the row sends, instead of
// restating that parser's conclusions in assertions of their own.
import { beforeEach, describe, expect, it, vi } from "vitest";

import { uploadLimitHint } from "./upload-policy.js";

const {
  openFilePicker,
  openTangentChat,
  openLiveRunView,
  setSupervisedDispatch,
  launchDispatch,
  recipesDispatch,
  submitPrompt,
  sendPromptTo,
  transportSend,
  toastError,
  collapseAll,
} = vi.hoisted(() => ({
  openFilePicker: vi.fn(),
  openTangentChat: vi.fn(),
  openLiveRunView: vi.fn(),
  setSupervisedDispatch: vi.fn(),
  launchDispatch: vi.fn(),
  recipesDispatch: vi.fn(),
  submitPrompt: vi.fn(),
  sendPromptTo: vi.fn(),
  transportSend: vi.fn(),
  toastError: vi.fn(),
  collapseAll: vi.fn(),
}));

let activeID = "";
let supervised: boolean | undefined;
let thinking = false;

vi.mock("./store.js", () => ({
  activeSession: {
    peek: () => (activeID === "" ? undefined : { id: activeID, supervised_mode: supervised }),
    get value() {
      return activeID === "" ? undefined : { id: activeID, supervised_mode: supervised };
    },
  },
  isThinking: (id: string) => id === activeID && thinking,
}));
vi.mock("./pill-expand.js", () => ({ makeExpandable: vi.fn(), collapseAll }));
vi.mock("./files-picker.js", () => ({ openFilePicker }));
vi.mock("./chat.js", () => ({ openTangentChat }));
vi.mock("./run-view.js", () => ({ openLiveRunView }));
vi.mock("./toast.js", () => ({ error: toastError, success: vi.fn(), info: vi.fn() }));
vi.mock("./actions/chat.js", () => ({ setSupervised: { dispatch: setSupervisedDispatch } }));
vi.mock("./actions/runs.js", () => ({
  launchRun: { dispatch: launchDispatch },
  loadRecipes: { dispatch: recipesDispatch },
}));
// The composer's send path. submit.ts is the ONE module allowed to decide
// prompt-versus-steer, so the goal row goes through it; the two lower-level
// senders stay mocked so a row that bypassed it is observable rather than a
// network error.
vi.mock("./submit.js", () => ({ submitPrompt }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo }));
vi.mock("./transport.js", () => ({ send: transportSend, newMessageID: () => "m-1" }));

import type * as ChatOptionsModule from "./chat-options.js";

/** KAS's `parseGoalCommand`, transcribed verbatim from the 2.18.1 bundle
 *  (`node_modules/@kiro/agent/dist/server/acp-server.js`, offset 19305949).
 *
 *  The row's entire contract is "this function accepts what we send, and reads
 *  back what the user typed", so the tests RUN it. Asserting the composed string
 *  against a hand-written expectation would only pin our own reading of the
 *  regex; the failure mode being guarded against is that reading being wrong.
 *
 *  On the prompt path a null return means the text falls through to the MODEL as
 *  prose (`session/prompt`, offset 21305522, calls this before invoking it), so
 *  null is never an acceptable answer for anything this row sends. */
function parseGoalCommand(userText: string): { description: string; maxIterations: number } | null {
  const trimmed = userText.trim();
  if (!trimmed.startsWith("/goal ") && trimmed !== "/goal") {
    return null;
  }
  const body = trimmed.slice(6).trim();
  if (body === "") {
    return null;
  }
  const maxMatch = /\s+--max\s+(\d+)$/.exec(body);
  let maxIterations = 5;
  let description = body;
  if (maxMatch?.[1] !== undefined) {
    maxIterations = Math.min(Math.max(parseInt(maxMatch[1], 10), 1), 200);
    description = body.slice(0, maxMatch.index).trim();
  }
  if (description === "") {
    return null;
  }
  return { description, maxIterations };
}

/** The minimum composer DOM initChatOptions touches, plus a fresh module state.
 *
 *  Returns the module INSTANCE it initialised, not the file's top-level import:
 *  the latch is module-level, so a fresh registry means a fresh latch, and
 *  asserting idempotence against a different instance would assert nothing. */
async function mountMenu(): Promise<{ card: HTMLElement; mod: typeof ChatOptionsModule }> {
  document.body.innerHTML = `
    <span class="pill-slot">
      <button id="chat-options-btn" class="pill pill-expandable" type="button"></button>
      <span id="chat-options-card" class="pill-expand-content chat-options-card hidden"></span>
    </span>
  `;
  vi.resetModules();
  const mod = await import("./chat-options.js");
  mod.initChatOptions();
  return { card: document.getElementById("chat-options-card") as HTMLElement, mod };
}

/** Click the row whose visible name matches. */
function clickRow(card: HTMLElement, name: string): void {
  for (const btn of Array.from(card.querySelectorAll<HTMLButtonElement>(".chat-opt-btn"))) {
    if (btn.querySelector(".chat-opt-name")?.textContent === name) {
      btn.click();
      return;
    }
  }
  throw new Error(`no row named ${name}`);
}

beforeEach(() => {
  vi.clearAllMocks();
  activeID = "c-active";
  supervised = false;
  thinking = false;
  // Left armed on purpose. Nothing on the goal path should reach the recipe list
  // any more, and a resolved reply means a regression that did would proceed far
  // enough to be caught by the explicit negative rather than dying in an
  // unhandled rejection somewhere unrelated.
  recipesDispatch.mockResolvedValue({
    recipes: [
      { name: "publish", source: "bundled://publish" },
      { name: "goal", source: "bundled://goal", inputs: { goal: "string" } },
    ],
  });
});

describe("the chat-actions menu", () => {
  // Four residents, and the count is the assertion: a fifth added without a
  // decision, or one silently lost to a refactor, both show up here.
  it("holds exactly four entries", async () => {
    const { card } = await mountMenu();
    const names = Array.from(card.querySelectorAll(".chat-opt-name")).map((n) => n.textContent);
    expect(names).toEqual(["Attach a file", "Set a goal", "Start a tangent", "Supervised mode"]);
  });

  // The switch sorts last because it is the one resident that is a SWITCH rather
  // than an action, and it keeps the label+checkbox shape rather than the button
  // shape the other three share.
  it("renders the switch as a label with a checkbox and the rest as buttons", async () => {
    const { card } = await mountMenu();
    expect(card.querySelectorAll(".chat-opt-btn")).toHaveLength(3);
    const row = card.querySelector<HTMLLabelElement>("label.chat-opt-row");
    expect(row?.htmlFor).toBe("chat-opt-supervised");
    expect(row?.querySelector<HTMLInputElement>("input")?.type).toBe("checkbox");
  });

  // A card holding buttons must not sit inside its trigger button: invalid HTML,
  // and assistive tech flattens it. The trigger is a real <button>, so this is
  // the same guard pill-expand.test.ts applies to the markup.
  it("keeps every row out of the trigger button", async () => {
    const { card } = await mountMenu();
    for (const btn of Array.from(card.querySelectorAll(".chat-opt-btn"))) {
      expect(btn.closest("#chat-options-btn")).toBeNull();
    }
  });
});

describe("attach a file", () => {
  // The picker's own "Upload here" calls input.click() inside ITS handler, so the
  // dialog's gesture is two clicks deeper. What this row must not do is put an
  // await between the menu click and the picker open — the browser's
  // user-activation window is the one thing a file input cannot ask for later.
  //
  // Asserted SYNCHRONOUSLY (no await after the click) precisely because that is
  // the property: an `await` anywhere on the path would make this fail.
  it("opens the picker synchronously on the click", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Attach a file");
    expect(openFilePicker).toHaveBeenCalledTimes(1);
  });

  // The picker opens a modal over the composer; an expanded card left behind it
  // sits under the modal still carrying pointer-events.
  it("collapses the card before opening the modal", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Attach a file");
    expect(collapseAll).toHaveBeenCalledTimes(1);
    expect(collapseAll.mock.invocationCallOrder[0]).toBeLessThan(
      openFilePicker.mock.invocationCallOrder[0] as number,
    );
  });

  // The cap used to be discoverable only as a server 413. Asserted against
  // uploadLimitHint rather than a copied numeral: a literal here is a second
  // statement of the limit that can disagree with the one the pre-flight enforces.
  it("states the upload cap on the row", async () => {
    const { card } = await mountMenu();
    const hint = card.querySelector(".chat-opt-hint")?.textContent ?? "";
    expect(hint).toContain(uploadLimitHint().toLowerCase());
    expect(hint).not.toContain("\u2014");
  });
});

describe("start a tangent", () => {
  it("opens a tangent off the active chat", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Start a tangent");
    expect(openTangentChat).toHaveBeenCalledWith("c-active");
    expect(collapseAll).toHaveBeenCalled();
  });

  // A tangent inherits a conversation, so there is no degraded version of one
  // taken off a chat that has none.
  it("refuses when no chat is active", async () => {
    activeID = "";
    const { card } = await mountMenu();
    clickRow(card, "Start a tangent");
    expect(openTangentChat).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledTimes(1);
  });
});

describe("set a goal", () => {
  /** Open the goal form on the row. */
  function openForm(card: HTMLElement): HTMLFormElement {
    clickRow(card, "Set a goal");
    const form = card.querySelector<HTMLFormElement>(".chat-opt-form");
    if (form === null) {
      throw new Error("the goal row opened no form");
    }
    return form;
  }

  function field(form: HTMLFormElement, label: string): HTMLInputElement {
    const input = form.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
    if (input === null) {
      throw new Error(`the goal form has no ${label} field`);
    }
    return input;
  }

  /** Fill the form and submit it. Returns the text the row sent, or undefined
   *  when it sent nothing at all. */
  function setGoal(card: HTMLElement, objective: string, cap = ""): string | undefined {
    const form = openForm(card);
    field(form, "Goal").value = objective;
    field(form, "Max iterations").value = cap;
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    const call = submitPrompt.mock.calls[0] as [string, string] | undefined;
    return call?.[1];
  }

  // The exact string, with the parse it has to survive. Both halves matter: a
  // composed command that KAS's parser returns null for reaches the model as
  // prose, and one it parses into the wrong objective silently sets a different
  // goal from the one that was typed.
  it("sends the objective alone when no cap is given", async () => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "make the test suite pass");
    expect(sent).toBe("/goal make the test suite pass");
    expect(submitPrompt).toHaveBeenCalledWith("c-active", "/goal make the test suite pass");
    // No suffix, so KAS's own default applies rather than a number vibekit
    // restates. 5 here is the parser's value, read back out of the parser.
    expect(parseGoalCommand(sent as string)).toEqual({
      description: "make the test suite pass",
      maxIterations: 5,
    });
  });

  it("appends the cap as the last thing in the command", async () => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "make the test suite pass", "5");
    expect(sent).toBe("/goal make the test suite pass --max 5");
    // The regex is anchored at the end (`/\s+--max\s+(\d+)$/`), so a suffix that
    // is not last is not a cap at all — it becomes part of the objective.
    expect(sent as string).toMatch(/ --max 5$/);
    expect(parseGoalCommand(sent as string)).toEqual({
      description: "make the test suite pass",
      maxIterations: 5,
    });
  });

  // Clamped by vibekit rather than left to KAS: the same arithmetic
  // (`Math.min(Math.max(n, 1), 200)`), applied before the string is built, so the
  // suffix is always a value the parser keeps. `-3` never reaches the regex as a
  // cap in any case — `\d+` cannot match a sign — so passing it through would
  // silently append it to the objective instead.
  it.each([
    ["0", 1],
    ["-3", 1],
    ["1", 1],
    ["7", 7],
    ["200", 200],
    ["201", 200],
    ["9000", 200],
  ])("clamps a cap of %s to %i", async (typed, want) => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "ship it", typed);
    expect(sent).toBe(`/goal ship it --max ${want}`);
    expect(parseGoalCommand(sent as string)?.maxIterations).toBe(want);
  });

  // A cap that is not a whole number is DROPPED, not forwarded. `--max soon`
  // fails the `\d+` match, so KAS would read "ship it --max soon" as the whole
  // objective — the goal statement silently gains two words of vibekit's UI.
  it.each([
    ["a word", "soon"],
    ["a fraction", "5.5"],
    ["whitespace", "   "],
    ["a numeral with units", "5 iterations"],
  ])("ignores %s as a cap", async (_desc, typed) => {
    const { card } = await mountMenu();
    const sent = setGoal(card, "ship it", typed);
    expect(sent).toBe("/goal ship it");
    expect(parseGoalCommand(sent as string)?.description).toBe("ship it");
  });

  // The recipe route is GONE, and this is what keeps it gone. It could not set the
  // iteration bound at all: the bundled recipe's repeat node is written
  // maxIterations 200 and launchGoal applies the user's number by mutating that
  // node on a clone, so a launch by source ran to 200 whatever was asked for.
  it("launches no run and fetches no recipe", async () => {
    const { card } = await mountMenu();
    setGoal(card, "ship it", "5");
    expect(recipesDispatch).not.toHaveBeenCalled();
    expect(launchDispatch).not.toHaveBeenCalled();
    expect(openLiveRunView).not.toHaveBeenCalled();
  });

  // The command goes through submit.ts, which is the one module allowed to decide
  // prompt-versus-steer. Reaching the lower-level senders directly would skip that
  // decision and the shared send lifecycle with it.
  it("sends through the composer's own send path", async () => {
    const { card } = await mountMenu();
    setGoal(card, "ship it");
    expect(submitPrompt).toHaveBeenCalledTimes(1);
    expect(sendPromptTo).not.toHaveBeenCalled();
    expect(transportSend).not.toHaveBeenCalled();
  });

  // A bare `/goal` is exactly the input parseGoalCommand returns null for, so it
  // would fall through to the model as prose. Refused here instead, where there is
  // something to say about it.
  it.each([
    ["empty", ""],
    ["whitespace only", "   "],
  ])("refuses a %s objective rather than sending an unparseable command", async (_d, objective) => {
    const { card } = await mountMenu();
    expect(setGoal(card, objective)).toBeUndefined();
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  // Mid-turn, Send means STEER (submit.ts), and `_session/steer` is not the prompt
  // path — parseGoalCommand has exactly one call site in the 2.18.1 bundle and it
  // is `session/prompt`. So a steered command is prose to the running turn, which
  // is the failure this row exists to avoid.
  it("refuses while a turn is running", async () => {
    thinking = true;
    const { card } = await mountMenu();
    expect(setGoal(card, "ship it")).toBeUndefined();
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  it("refuses when no chat is active", async () => {
    activeID = "";
    const { card } = await mountMenu();
    expect(setGoal(card, "ship it")).toBeUndefined();
    expect(submitPrompt).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  // There is no clear verb, upstream or here. parseGoalCommand takes the whole
  // body as the objective, so `/goal clear` launches a goal whose objective is the
  // word "clear" — a control offering it would misfire silently rather than do
  // nothing. Stopping a goal is cancelling its run.
  it("offers no clear verb and composes none", async () => {
    const { card } = await mountMenu();
    const form = openForm(card);
    expect(Array.from(form.querySelectorAll("button")).map((b) => b.textContent)).toEqual([
      "Set goal",
    ]);
    expect((card.textContent ?? "").toLowerCase()).not.toContain("clear");
    for (const btn of Array.from(card.querySelectorAll<HTMLButtonElement>(".chat-opt-btn"))) {
      btn.click();
    }
    for (const call of submitPrompt.mock.calls as [string, string][]) {
      expect(parseGoalCommand(call[1])?.description).not.toBe("clear");
    }
  });

  // Inline, not a modal — the Workflows tab's idiom. A second click closes the
  // form, so the row is a toggle rather than a form-stacker.
  it("toggles the inline form rather than stacking one per click", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    expect(card.querySelectorAll(".chat-opt-form")).toHaveLength(1);
    clickRow(card, "Set a goal");
    expect(card.querySelectorAll(".chat-opt-form")).toHaveLength(0);
  });

  // The card outlives every chat switch (it is built once at init), so the chat id
  // has to be read when the form is submitted rather than when it was built.
  it("sends to the chat that is active at submit time", async () => {
    const { card } = await mountMenu();
    const form = openForm(card);
    field(form, "Goal").value = "ship it";
    activeID = "c-other";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(submitPrompt).toHaveBeenCalledWith("c-other", "/goal ship it");
  });
});

describe("supervised mode", () => {
  it("dispatches the toggle for the active chat", async () => {
    const { card } = await mountMenu();
    const box = card.querySelector<HTMLInputElement>("#chat-opt-supervised");
    box!.checked = true;
    box!.dispatchEvent(new Event("change"));
    expect(setSupervisedDispatch).toHaveBeenCalledWith({ chatID: "c-active", enabled: true });
  });

  // No chat yet: nothing to persist against, and the visual resets rather than
  // claiming a setting that was never stored. The supervised DEFAULT for new chats
  // lives in Settings, which is where a before-the-first-prompt choice belongs.
  it("resets the visual and persists nothing with no active chat", async () => {
    activeID = "";
    const { card } = await mountMenu();
    const box = card.querySelector<HTMLInputElement>("#chat-opt-supervised");
    box!.checked = true;
    box!.dispatchEvent(new Event("change"));
    expect(setSupervisedDispatch).not.toHaveBeenCalled();
    expect(box!.checked).toBe(false);
  });
});

// initChatOptions is called once from app.ts, but the latch is what makes a
// second call safe — without it a re-init would append a second set of four rows.
describe("initChatOptions", () => {
  it("is idempotent", async () => {
    const { card, mod } = await mountMenu();
    mod.initChatOptions();
    expect(card.querySelectorAll(".chat-opt-name")).toHaveLength(4);
  });
});
