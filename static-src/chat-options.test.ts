// @vitest-environment happy-dom
// The chat-actions menu: four rows, each of which must do what it says.
//
// What is worth pinning here is the DISPATCH each row makes, because three of the
// four are the only door to a server verb and the fourth is a modal open. The
// card is built entirely in TS (index.html carries an empty one), so this file
// also stands in for a markup test of it.
//
// The goal row gets the most attention, for one reason: its failure mode is
// silent. A row that sends `/goal` as prose reaches the model, which answers as
// though it ran — so the assertion that matters is a negative one (nothing was
// sent through the prompt path) alongside the positive launch.
import { beforeEach, describe, expect, it, vi } from "vitest";

import { uploadLimitHint } from "./upload-policy.js";

const {
  openFilePicker,
  openTangentChat,
  openLiveRunView,
  setSupervisedDispatch,
  launchDispatch,
  recipesDispatch,
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
  sendPromptTo: vi.fn(),
  transportSend: vi.fn(),
  toastError: vi.fn(),
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
// The two paths a "send some text" mistake would take. Mocked so that a row
// reaching either is observable rather than a network error.
vi.mock("./chat-commands.js", () => ({ sendPromptTo }));
vi.mock("./transport.js", () => ({ send: transportSend, newMessageID: () => "m-1" }));

import type * as ChatOptionsModule from "./chat-options.js";

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
  // The launch key is the RECIPE's own source, never a hand-built string:
  // POST /api/runs re-resolves the source against a fresh listRecipes and refuses
  // anything absent from it, so a fabricated `bundled://goal` would be a launch
  // that can only fail on a build whose recipe set does not include one.
  it("launches the goal recipe with its declared inputs", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    await vi.waitFor(() => {
      expect(card.querySelector(".chat-opt-form")).not.toBeNull();
    });
    const input = card.querySelector<HTMLInputElement>(".chat-opt-input");
    expect(input?.getAttribute("aria-label")).toBe("Goal input goal");
    input!.value = "get the suite green";
    card
      .querySelector<HTMLFormElement>(".chat-opt-form")
      ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    expect(launchDispatch).toHaveBeenCalledTimes(1);
    const [body] = launchDispatch.mock.calls[0] as [{ source: string; inputs: unknown }];
    expect(body.source).toBe("bundled://goal");
    expect(body.inputs).toEqual({ goal: "get the suite green" });
  });

  // THE assertion for this row. `/goal` has a real parser upstream, but the TUI
  // drives it as a structured command call — so text in the composer is prose to
  // the model, which answers as though it ran. That is the `/compact` failure, and
  // it is invisible without a negative assertion.
  //
  // Two halves. Nothing reaches either send path (a prompt or a transport
  // command), and the goal TEXT lands in the recipe's declared input rather than
  // being decorated into a slash command anywhere.
  it("sends no prompt and composes no slash command", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    await vi.waitFor(() => {
      expect(card.querySelector(".chat-opt-form")).not.toBeNull();
    });
    card.querySelector<HTMLInputElement>(".chat-opt-input")!.value = "ship it";
    card
      .querySelector<HTMLFormElement>(".chat-opt-form")
      ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    expect(sendPromptTo).not.toHaveBeenCalled();
    expect(transportSend).not.toHaveBeenCalled();
    const [body] = launchDispatch.mock.calls[0] as [
      { source: string; inputs: Record<string, string> },
    ];
    // The text is the input's VALUE, verbatim. `bundled://goal` legitimately
    // contains "/goal", so a substring scan of the whole body would be a check
    // that can only pass by accident; the values are where prose would hide.
    expect(Object.values(body.inputs)).toEqual(["ship it"]);
    for (const v of Object.values(body.inputs)) {
      expect(v.startsWith("/")).toBe(false);
    }
  });

  // Owned rather than a review, so the tab carries the run's own Pause / Resume /
  // Cancel: this is the surface that started the run, and a run reached from
  // History is deliberately read-only.
  it("opens the run as a launcher-owned tab", async () => {
    launchDispatch.mockImplementation(
      (_body: unknown, opts: { onSuccess?: (r: unknown) => void }) => {
        opts.onSuccess?.({ workflow_id: "wf_9", name: "goal" });
        return Promise.resolve(null);
      },
    );
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    await vi.waitFor(() => {
      expect(card.querySelector(".chat-opt-form")).not.toBeNull();
    });
    card
      .querySelector<HTMLFormElement>(".chat-opt-form")
      ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(openLiveRunView).toHaveBeenCalledWith("wf_9", "goal");
  });

  // Nothing is invented. A recipe that declares no inputs has nothing to collect,
  // so no field is fabricated for it and the launch goes straight out — which is
  // also what would happen if this build's goal recipe declared no iteration cap.
  it("launches immediately when the recipe declares no inputs", async () => {
    recipesDispatch.mockResolvedValue({
      recipes: [{ name: "goal", source: "bundled://goal" }],
    });
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    await vi.waitFor(() => {
      expect(launchDispatch).toHaveBeenCalledTimes(1);
    });
    expect(card.querySelector(".chat-opt-form")).toBeNull();
    const [body] = launchDispatch.mock.calls[0] as [{ inputs: unknown }];
    expect(body.inputs).toEqual({});
  });

  // The honest degradation. The goal recipe is RESOLVED from the live list rather
  // than assumed, so a build whose list has no goal in it says so instead of
  // posting a launch that can only be refused.
  it("says so when the build has no goal recipe", async () => {
    recipesDispatch.mockResolvedValue({
      recipes: [{ name: "publish", source: "bundled://publish" }],
    });
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    await vi.waitFor(() => {
      expect(toastError).toHaveBeenCalledTimes(1);
    });
    expect(launchDispatch).not.toHaveBeenCalled();
  });

  // Inline, not a modal — the Workflows tab's idiom. A second click closes the
  // form, so the row is a toggle rather than a form-stacker.
  it("toggles the inline form rather than stacking one per click", async () => {
    const { card } = await mountMenu();
    clickRow(card, "Set a goal");
    await vi.waitFor(() => {
      expect(card.querySelectorAll(".chat-opt-form")).toHaveLength(1);
    });
    clickRow(card, "Set a goal");
    expect(card.querySelectorAll(".chat-opt-form")).toHaveLength(0);
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
