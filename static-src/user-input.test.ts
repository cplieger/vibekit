// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for user-input.ts — the agent's structured-question CARD (kiro-cli v3
// _kiro/userInput), as rendered in the interaction dock. Options render as
// choice cards (title + description + Recommended badge); a plain option
// answers with its title; an option with sub-options runs the pre-checked
// multi-select stage and answers "Title [Sub1, Sub2]" (the TUI's format); a
// free-form question (or the always-available typed field) answers with the
// typed text; Skip dismisses.
//
// No dialog mock and no focus-trap mock: the card is a plain subtree with no
// <dialog>, no showModal and no focus trap. Supersede and settle-once moved to
// decision-dock.ts (see decision-dock.test.ts) — they are properties of the
// queue, not of one card's DOM.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

import { buildUserInputCard, _resetForTest } from "./user-input.js";
import type { UserInputNeededPayload } from "./types.js";

function payload(over: Partial<UserInputNeededPayload> = {}): UserInputNeededPayload {
  return { request_id: 1, question: "Which approach?", ...over };
}

/** Mount a card so click handlers and queries run against a real tree. */
function mount(p: UserInputNeededPayload, onSubmit: (a: string, b?: string) => void): HTMLElement {
  const card = buildUserInputCard(p, onSubmit as never);
  document.body.replaceChildren(card);
  return card;
}

beforeEach(() => {
  document.body.replaceChildren();
  _resetForTest();
});

describe("buildUserInputCard", () => {
  it("renders the question, option cards with description + Recommended badge", () => {
    const card = mount(
      payload({
        options: [
          { title: "Fast", description: "Quick pass", recommended: true },
          { title: "Thorough" },
        ],
      }),
      () => undefined,
    );
    expect(card.querySelector(".user-input-body")?.textContent).toContain("Which approach?");
    const cards = card.querySelectorAll(".user-input-option");
    expect(cards.length).toBe(2);
    expect(cards[0]?.textContent).toContain("Quick pass");
    expect(cards[0]?.querySelector(".user-input-recommended")).not.toBeNull();
    expect(cards[1]?.querySelector(".user-input-recommended")).toBeNull();
  });

  it("a plain option answers with its title on click", () => {
    const onSubmit = vi.fn();
    const card = mount(payload({ options: [{ title: "Fast" }] }), onSubmit);
    (card.querySelector(".user-input-option") as HTMLButtonElement).click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "Fast");
  });

  it("sub-options run the pre-checked multi-select and fold into the answer", () => {
    const onSubmit = vi.fn();
    const card = mount(
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
    (card.querySelector(".user-input-option") as HTMLButtonElement).click();
    const boxes = card.querySelectorAll<HTMLInputElement>(".user-input-sub-option input");
    expect(boxes.length).toBe(3);
    expect([...boxes].every((b) => b.checked)).toBe(true); // TUI parity: pre-checked
    if (boxes[1] !== undefined) {
      boxes[1].checked = false; // drop "Docs"
    }
    const confirm = [
      ...card.querySelectorAll<HTMLButtonElement>(".user-input-actions button"),
    ].find((b) => b.textContent === "Confirm");
    confirm?.click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "Thorough [Tests, Bench]");
  });

  it("Back returns from the sub-select to the option cards", () => {
    const card = mount(
      payload({ options: [{ title: "Thorough", sub_options: [{ title: "Tests" }] }] }),
      () => undefined,
    );
    (card.querySelector(".user-input-option") as HTMLButtonElement).click();
    const back = [...card.querySelectorAll<HTMLButtonElement>(".user-input-actions button")].find(
      (b) => b.textContent === "Back",
    );
    back?.click();
    expect(card.querySelectorAll(".user-input-option").length).toBe(1);
  });

  it("a free-form question renders a textarea and answers with typed text", () => {
    const onSubmit = vi.fn();
    const card = mount(payload({ options: [] }), onSubmit);
    expect(card.querySelectorAll(".user-input-option").length).toBe(0);
    const input = card.querySelector(".user-input-text") as HTMLTextAreaElement;
    input.value = "  do both  ";
    const send = [...card.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Send",
    );
    send?.click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "do both");
  });

  it("a typed answer is available alongside options and empty text never submits", () => {
    const onSubmit = vi.fn();
    const card = mount(payload({ options: [{ title: "Fast" }] }), onSubmit);
    const input = card.querySelector(".user-input-text") as HTMLTextAreaElement;
    const send = [...card.querySelectorAll<HTMLButtonElement>(".user-input-freeform button")].find(
      (b) => b.textContent === "Send",
    );
    send?.click();
    expect(onSubmit).not.toHaveBeenCalled();
    input.value = "something custom";
    send?.click();
    expect(onSubmit).toHaveBeenCalledWith("answered", "something custom");
  });

  it("Skip dismisses", () => {
    const onSubmit = vi.fn();
    const card = mount(payload({ options: [{ title: "A" }] }), onSubmit);
    const skip = [...card.querySelectorAll<HTMLButtonElement>(".user-input-actions button")].find(
      (b) => b.textContent === "Skip",
    );
    skip?.click();
    expect(onSubmit).toHaveBeenCalledWith("dismissed", undefined);
  });

  it("is a plain subtree: no dialog, and nothing that would trap focus", () => {
    const card = mount(payload({ options: [{ title: "A" }] }), () => undefined);
    expect(card.tagName).toBe("DIV");
    expect(card.querySelector("dialog")).toBeNull();
    expect(card.classList.contains("dock-card")).toBe(true);
  });
});
