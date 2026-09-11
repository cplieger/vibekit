// ---------------------------------------------------------------------------
// Tests for run-input.ts — the card a parked workflow step's question renders
// as in the interaction dock.
//
// Two answers, and the second is what separates this card from the agent's own
// question card: Send answer hands the step the reader's words, while Continue
// without answering re-drives the step with KAS's default continuation. The
// second exists for the post-restart case, where the ask registry is in memory so
// the question text is gone while the run is still parked.
//
// A CHAT-PARENTED ask offers a THIRD thing in the second button's place — Defer to
// parent agent, which answers nothing and asks the agent that launched the run
// instead. Presence of the callback is the whole discriminator here, so these cases
// pin which button each population gets and what the pressed one settles into.
//
// Supersede and settle-once are the QUEUE's properties, not this card's, so they
// live in decision-dock.test.ts.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

import { buildRunInputCard } from "./run-input.js";
import type { RunInputNeededPayload } from "./types.js";

function payload(over: Partial<RunInputNeededPayload> = {}): RunInputNeededPayload {
  return {
    workflow_id: "wf_1",
    ask_id: "a1",
    node_id: "review",
    step_session_id: "sess_step",
    agent_name: "reviewer",
    question: "Which branch should I target?",
    asked_at: "2026-09-03T10:00:00Z",
    ...over,
  };
}

function mount(
  p: RunInputNeededPayload,
  onSubmit: (text: string | null) => void = () => undefined,
  held = "",
  onDefer?: () => void | Promise<void>,
): HTMLElement {
  const card = buildRunInputCard(p, held, onSubmit, onDefer);
  document.body.replaceChildren(card);
  return card;
}

/** The answer row's buttons, in order. Scoped to `.run-input-actions` because the
 *  question's own clamp opener is a button too and belongs to the body. */
function actionLabels(card: HTMLElement): (string | null)[] {
  return [...card.querySelectorAll(".run-input-actions button")].map((b) => b.textContent);
}

function button(card: HTMLElement, label: string): HTMLButtonElement {
  const found = [...card.querySelectorAll("button")].find((b) => b.textContent === label);
  if (found === undefined) {
    throw new Error(`no button labelled ${label}`);
  }
  return found;
}

beforeEach(() => {
  document.body.replaceChildren();
});

describe("buildRunInputCard", () => {
  it("renders the question and names the asking step", () => {
    const card = mount(payload());
    expect(card.querySelector("strong")?.textContent).toBe("Which branch should I target?");
    // Who is asking, because a run has many steps and answering the wrong one's
    // question is exactly the mistake this line prevents.
    expect(card.querySelector(".run-input-step")?.textContent).toBe("reviewer \u00b7 step review");
  });

  it("omits the step line when the frame could not name one", () => {
    // Both fields are legitimately absent: KAS puts the node id on the
    // notification only when the caller is a step. A run blocked by an unnameable
    // step is still blocked, so the row goes rather than showing a placeholder.
    const card = mount(payload({ agent_name: "", node_id: "" }));
    expect(card.querySelector(".run-input-step")).toBeNull();
  });

  it("falls back to a sentence and says why when the question is gone", () => {
    // The post-restart case: the registry is in memory, so the server reconstructs
    // an ask from the run's state with no text on it. A blank heading would read
    // as a broken card rather than as a recoverable state.
    const card = mount(payload({ question: "" }));
    expect(card.querySelector("strong")?.textContent).toBe("A step is waiting for your answer");
    expect(card.querySelector(".run-input-note")?.textContent).toContain("server restarted");
  });

  it("does not explain a missing question when there is one", () => {
    expect(mount(payload()).querySelector(".run-input-note")).toBeNull();
  });

  it("reports the typed answer on Send", () => {
    const onSubmit = vi.fn();
    const card = mount(payload(), onSubmit);
    const box = card.querySelector("textarea") as HTMLTextAreaElement;
    box.value = "  the release branch  ";
    button(card, "Send answer").click();
    // Trimmed: the server refuses whitespace, and sending it would spend the
    // reader's one claim on an answer the step cannot use.
    expect(onSubmit).toHaveBeenCalledWith("the release branch");
  });

  it("seeds the box with words a previous send is still holding", () => {
    // The dock splices the card BEFORE the answer goes out, so a retryable refusal
    // re-offers the question against a fresh element. Without the seed the reader
    // gets their question back with an empty box and retypes what they just wrote.
    const card = mount(payload(), () => undefined, "the release branch");
    expect((card.querySelector("textarea") as HTMLTextAreaElement).value).toBe(
      "the release branch",
    );
  });

  it("opens empty when nothing is held, which is the ordinary case", () => {
    expect((mount(payload()).querySelector("textarea") as HTMLTextAreaElement).value).toBe("");
  });

  it("refuses an empty Send rather than waiving the question", () => {
    const onSubmit = vi.fn();
    const card = mount(payload(), onSubmit);
    button(card, "Send answer").click();
    // Continuing with no answer is a DIFFERENT verb, because it drives the step
    // with KAS's own continuation instead of the reader's words. An empty box must
    // not reach it.
    expect(onSubmit).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(card.querySelector("textarea"));
  });

  it("reports null on Continue without answering", () => {
    const onSubmit = vi.fn();
    const card = mount(payload({ question: "" }), onSubmit);
    button(card, "Continue without answering").click();
    expect(onSubmit).toHaveBeenCalledWith(null);
  });

  it("says what Continue does, since the label cannot", () => {
    const card = mount(payload());
    expect(button(card, "Continue without answering").getAttribute("data-tooltip")).toContain(
      "carries on with no answer",
    );
  });

  it("withholds Continue when the ask names no node, whose verb needs one", () => {
    // `set_step_status` is addressed by node and refuses 400 without one, so the
    // button could only produce an error toast — with the card already spliced by the
    // dock's settle, which leaves the reader worse off than never being offered it.
    const card = mount(payload({ node_id: "" }));
    // Scoped to the ANSWER ROW, which is the subject: the question's own clamp
    // opener is a button too, and it belongs to the body rather than to the ways
    // this ask can be answered.
    expect(
      [...card.querySelectorAll(".run-input-actions button")].map((b) => b.textContent),
    ).toEqual(["Send answer"]);
  });

  it("still lets such an ask be ANSWERED, since the answer is addressed by session", () => {
    const onSubmit = vi.fn();
    const card = mount(payload({ node_id: "" }), onSubmit);
    const box = card.querySelector("textarea") as HTMLTextAreaElement;
    box.value = "the release branch";
    button(card, "Send answer").click();
    expect(onSubmit).toHaveBeenCalledWith("the release branch");
  });

  describe("a CHAT-PARENTED ask", () => {
    /** Resolves once the hand-off has settled: the click's own promise chain runs on
     *  microtasks, so nothing is observable in the same task. */
    const defer = (): Promise<void> => Promise.resolve();

    it("offers the launching agent in place of the waive", () => {
      const card = mount(payload(), () => undefined, "", defer);
      expect(actionLabels(card)).toEqual(["Send answer", "Defer to parent agent"]);
    });

    it("leaves a PARENTLESS ask exactly as it was", () => {
      // The population that has nobody to defer to: no callback, so the card is
      // today's card and the waive is still the second button.
      const card = mount(payload());
      expect(actionLabels(card)).toEqual(["Send answer", "Continue without answering"]);
    });

    it("still offers a deferral when the ask names no node", () => {
      // The node-id gate belongs to Continue alone, whose verb is addressed by node.
      // A deferral is addressed by CHAT, so it is available either way.
      const card = mount(payload({ node_id: "" }), () => undefined, "", defer);
      expect(actionLabels(card)).toEqual(["Send answer", "Defer to parent agent"]);
    });

    it("asks once and never routes the deferral through the answer verb", () => {
      const onSubmit = vi.fn();
      const onDefer = vi.fn(defer);
      const card = mount(payload(), onSubmit, "", onDefer);
      const b = button(card, "Defer to parent agent");
      b.click();
      // Twice, because the guard is the disable and it has to land on the FIRST
      // click: a second prompt into the launching chat is a second turn.
      b.click();
      expect(onDefer).toHaveBeenCalledTimes(1);
      // A deferral answers nothing, so it must not reach `onSubmit` in either of
      // that callback's meanings — the reader's words, or the waive.
      expect(onSubmit).not.toHaveBeenCalled();
    });

    it("settles into a persistent asked state once the hand-off lands", async () => {
      const card = mount(payload(), () => undefined, "", defer);
      button(card, "Defer to parent agent").click();
      await vi.waitFor(() => {
        const b = button(card, "Asked the agent");
        // Disabled for the card's life rather than for a moment: the agent has been
        // asked, and asking it again would spend another turn on the same question.
        expect(b.disabled).toBe(true);
        expect(b.getAttribute("data-tooltip")).toContain("has been asked to answer");
      });
    });

    it("keeps the question answerable after a deferral", async () => {
      // The ask is still OPEN — that is the whole difference from the waive — so the
      // textarea and Send stay live and the reader can still answer themselves.
      const onSubmit = vi.fn();
      const card = mount(payload(), onSubmit, "", defer);
      button(card, "Defer to parent agent").click();
      await vi.waitFor(() => {
        expect(button(card, "Asked the agent").disabled).toBe(true);
      });

      const box = card.querySelector("textarea") as HTMLTextAreaElement;
      box.value = "the release branch";
      button(card, "Send answer").click();
      expect(onSubmit).toHaveBeenCalledWith("the release branch");
    });

    it("hands the button back when the hand-off was refused", async () => {
      // `submit.ts` already reports the refusal through send-state, so the card owes
      // the reader nothing but a button they can press again — and the label must not
      // claim an agent was asked.
      const card = mount(
        payload(),
        () => undefined,
        "",
        () => Promise.reject(new Error("no")),
      );
      button(card, "Defer to parent agent").click();
      await vi.waitFor(() => {
        expect(button(card, "Defer to parent agent").disabled).toBe(false);
      });
    });

    it("says what the button does, since the label cannot", () => {
      const card = mount(payload(), () => undefined, "", defer);
      expect(button(card, "Defer to parent agent").getAttribute("data-tooltip")).toContain(
        "is asked to answer instead",
      );
    });
  });

  it("submits on Cmd/Ctrl+Enter and not on a bare Enter", () => {
    const onSubmit = vi.fn();
    const card = mount(payload(), onSubmit);
    const box = card.querySelector("textarea") as HTMLTextAreaElement;
    box.value = "the release branch";

    // A bare Enter inserts a newline: an answer to a step is prose that may want
    // paragraphs, which is the call the composer's own textarea makes.
    box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(onSubmit).not.toHaveBeenCalled();

    box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true }));
    expect(onSubmit).toHaveBeenCalledWith("the release branch");
  });
});
