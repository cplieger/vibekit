// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for user-input.ts — the agent's structured-question dialog
// (kiro-cli v3 _kiro/userInput). Options render as choice cards (title +
// description + Recommended badge); a plain option answers with its title;
// an option with sub-options runs the pre-checked multi-select stage and
// answers "Title [Sub1, Sub2]" (the TUI's format); a free-form question
// (or the always-available typed field) answers with the typed text;
// Escape / Skip dismisses. The ui-primitives dialog opener is mocked —
// happy-dom's <dialog> lacks showModal.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("@cplieger/ui-primitives/dialog", () => ({
  openDialog: (d: HTMLDialogElement) => {
    d.setAttribute("open", "");
  },
}));
vi.mock("./focus-trap.js", () => ({ trapFocus: () => () => undefined }));

import { showUserInputDialog, _resetForTest } from "./user-input.js";
import type { UserInputNeededPayload } from "./types.js";

function seedDom(): void {
  document.body.innerHTML = `
    <dialog id="user-input-dialog">
      <div class="user-input-body"></div>
      <div class="user-input-options"></div>
      <div class="user-input-freeform"></div>
      <div class="user-input-actions"></div>
    </dialog>`;
}

function payload(over: Partial<UserInputNeededPayload> = {}): UserInputNeededPayload {
  return { request_id: 1, question: "Which approach?", ...over };
}

const dlg = (): HTMLElement => document.getElementById("user-input-dialog") as HTMLElement;

beforeEach(() => {
  seedDom();
  _resetForTest();
});

describe("showUserInputDialog", () => {
  it("renders the question, option cards with description + Recommended badge", () => {
    showUserInputDialog(
      payload({
        options: [
          { title: "Fast", description: "Quick pass", recommended: true },
          { title: "Thorough" },
        ],
      }),
      () => undefined,
    );
    expect(dlg().querySelector(".user-input-body")?.textContent).toContain("Which approach?");
    const cards = dlg().querySelectorAll(".user-input-option");
    expect(cards.length).toBe(2);
    expect(cards[0]?.textContent).toContain("Quick pass");
    expect(cards[0]?.querySelector(".user-input-recommended")).not.toBeNull();
    expect(cards[1]?.querySelector(".user-input-recommended")).toBeNull();
  });

  it("a plain option answers with its title on click", () => {
    const onSubmit = vi.fn();
    showUserInputDialog(payload({ options: [{ title: "Fast" }] }), onSubmit);
    (dlg().querySelector(".user-input-option") as HTMLButtonElement).click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "Fast");
  });

  it("sub-options run the pre-checked multi-select and fold into the answer", () => {
    const onSubmit = vi.fn();
    showUserInputDialog(
      payload({
        options: [
          {
            title: "Thorough",
            sub_options_label: "Include:",
            sub_options: [{ title: "Tests" }, { title: "Docs" }, { title: "Bench" }],
          },
        ],
      }),
      onSubmit,
    );
    (dlg().querySelector(".user-input-option") as HTMLButtonElement).click();
    const boxes = dlg().querySelectorAll<HTMLInputElement>(".user-input-sub-option input");
    expect(boxes.length).toBe(3);
    expect([...boxes].every((b) => b.checked)).toBe(true); // TUI parity: pre-checked
    if (boxes[1] !== undefined) {
      boxes[1].checked = false; // drop "Docs"
    }
    const confirm = [
      ...dlg().querySelectorAll<HTMLButtonElement>(".user-input-actions button"),
    ].find((b) => b.textContent === "Confirm");
    confirm?.click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "Thorough [Tests, Bench]");
  });

  it("Back returns from the sub-select to the option cards", () => {
    showUserInputDialog(
      payload({ options: [{ title: "Thorough", sub_options: [{ title: "Tests" }] }] }),
      () => undefined,
    );
    (dlg().querySelector(".user-input-option") as HTMLButtonElement).click();
    const back = [...dlg().querySelectorAll<HTMLButtonElement>(".user-input-actions button")].find(
      (b) => b.textContent === "Back",
    );
    back?.click();
    expect(dlg().querySelectorAll(".user-input-option").length).toBe(1);
  });

  it("a free-form question renders a textarea and answers with typed text", () => {
    const onSubmit = vi.fn();
    showUserInputDialog(payload({ options: [] }), onSubmit);
    expect(dlg().querySelectorAll(".user-input-option").length).toBe(0);
    const input = dlg().querySelector(".user-input-text") as HTMLTextAreaElement;
    input.value = "  do both  ";
    const send = [...dlg().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Send",
    );
    send?.click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "do both");
  });

  it("a typed answer is available alongside options and empty text never submits", () => {
    const onSubmit = vi.fn();
    showUserInputDialog(payload({ options: [{ title: "Fast" }] }), onSubmit);
    const input = dlg().querySelector(".user-input-text") as HTMLTextAreaElement;
    const send = [...dlg().querySelectorAll<HTMLButtonElement>(".user-input-freeform button")].find(
      (b) => b.textContent === "Send",
    );
    send?.click();
    expect(onSubmit).not.toHaveBeenCalled();
    input.value = "something custom";
    send?.click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "something custom");
  });

  it("Skip dismisses; a superseding question dismisses the open one first", () => {
    const first = vi.fn();
    showUserInputDialog(payload({ request_id: 1, options: [{ title: "A" }] }), first);
    const second = vi.fn();
    showUserInputDialog(payload({ request_id: 2, options: [{ title: "B" }] }), second);
    expect(first).toHaveBeenCalledWith("dismissed", undefined);

    const skip = [...dlg().querySelectorAll<HTMLButtonElement>(".user-input-actions button")].find(
      (b) => b.textContent === "Skip",
    );
    skip?.click();
    expect(second).toHaveBeenCalledWith("dismissed", undefined);
    // Settled: a second submit path can't double-fire.
    expect(second).toHaveBeenCalledTimes(1);
  });
});
