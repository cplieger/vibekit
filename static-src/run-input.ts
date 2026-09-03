// ---------------------------------------------------------------------------
// Run-input card: a workflow STEP asked a question and its run is parked until
// somebody answers it. Rendered in the interaction dock, which owns the queue,
// the settle-once guard and the two hosts this card appears in.
//
// It renders in BOTH the launching chat's composer dock and the run tab's,
// because one Decision carrying `chatID` and `runID` matches both hosts' own
// matchers — which is the requirement: a run launched from a conversation asks
// its question in that conversation, and a reader who has the run's tab open
// answers it there.
//
// TWO answers, and the second is the reason this card is not `user-input.ts`
// with a different heading:
//
//   - SEND ANSWER hands the step the reader's words. It is a `session/prompt`
//     addressed to the paused step's own session, which KAS reroutes back into
//     the run.
//   - CONTINUE WITHOUT ANSWERING re-drives the step with KAS's DEFAULT
//     continuation instead. It exists for the post-restart case: the ask
//     registry is in memory, so a container restart leaves the run parked with
//     the question text gone, and a reader cannot answer what they cannot read.
//     Without it that run's only recourse would be cancelling work one sentence
//     from finishing.
//
// No focus trap, like every other dock card: the question is about work in the
// transcript, and the reader is meant to leave and come back.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { RUN_INPUT_FALLBACK } from "./decision-dock.js";
import type { RunInputNeededPayload } from "./types.js";

/** `null` is "continue without answering"; a string is the answer. */
type SubmitFn = (text: string | null) => void;

/** Build the dock card for one parked workflow step.
 *
 *  The reporter is threaded through rather than parked in module state, for
 *  `buildUserInputCard`'s reason and it is a correctness requirement rather than
 *  a style choice: the dock keeps an ANSWERED card on screen for the length of its
 *  advance animation, so two cards coexist, and a module-level reporter would be
 *  overwritten by the incoming one — after which the outgoing card's buttons would
 *  answer the INCOMING decision, which `settle`'s membership guard cannot catch
 *  because that decision is legitimately still queued. */
export function buildRunInputCard(payload: RunInputNeededPayload, onSubmit: SubmitFn): HTMLElement {
  // An EMPTY question is the post-restart case rather than a malformed frame, so
  // it gets a sentence of its own instead of a blank heading. Shared with the
  // dock's own one-line label so the card and the run card's alert agree.
  const question = payload.question === "" ? RUN_INPUT_FALLBACK : payload.question;
  const body = el("div", { className: "run-input-body" }, el("strong", null, question));

  const who = stepLabel(payload);
  if (who !== "") {
    body.appendChild(el("p", { className: "run-input-step" }, who));
  }
  if (payload.question === "") {
    // Says WHY there is nothing to read, so an empty card does not look broken.
    // The run is genuinely parked and genuinely answerable; only the text is gone.
    body.appendChild(
      el(
        "p",
        { className: "run-input-note" },
        "The question itself was lost when the server restarted. Answer if you know what it " +
          "asked, or let the step carry on without one.",
      ),
    );
  }

  const input = el("textarea", {
    className: "run-input-text",
    rows: "3",
    placeholder: "Type your answer\u2026",
    "aria-label": "Your answer to the workflow step",
  }) as HTMLTextAreaElement;

  const send = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Send answer",
  ) as HTMLButtonElement;
  send.addEventListener("click", () => {
    const text = input.value.trim();
    if (text === "") {
      // Focus rather than a refusal message: the box IS the instruction, and
      // "continue without answering" is a separate button rather than what an
      // empty send means.
      input.focus();
      return;
    }
    onSubmit(text);
  });

  // Cmd/Ctrl+Enter rather than bare Enter: an answer to a step is prose that may
  // want paragraphs, which is the same call the composer's textarea makes.
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send.click();
    }
  });

  const actions = el("div", { className: "run-input-actions" }, send);

  // WITHHELD on an ask with no node id, because the verb behind it cannot be
  // addressed: `set_step_status` takes a node and refuses 400 without one, so the
  // button could only ever produce an error toast — with the card already spliced
  // by the dock's settle, which leaves the reader worse off than not offering it.
  // Such an ask is still ANSWERABLE (the answer is addressed by session, not by
  // node), so Send stays and only the waive is gone.
  if (payload.node_id !== "") {
    const skip = el(
      "button",
      { type: "button", className: "btn-small" },
      "Continue without answering",
    );
    skip.setAttribute(
      "data-tooltip",
      "The step carries on with no answer from you, using whatever its own instructions say next.",
    );
    skip.addEventListener("click", () => {
      onSubmit(null);
    });
    actions.appendChild(skip);
  }

  return el(
    "div",
    { className: "dock-card dock-run-input" },
    body,
    el("div", { className: "run-input-editor" }, input),
    actions,
  );
}

/** Which step is asking, as one line, or "" when the frame could not name one.
 *
 *  Both fields are legitimately absent: KAS puts the node id on the notification
 *  only when the caller is a step, and the agent name only when the step declared
 *  one. A run blocked by an unnameable step is still blocked, so the row is
 *  omitted rather than filled with a placeholder. */
function stepLabel(p: RunInputNeededPayload): string {
  if (p.agent_name !== "" && p.node_id !== "") {
    return `${p.agent_name} \u00b7 step ${p.node_id}`;
  }
  if (p.agent_name !== "") {
    return p.agent_name;
  }
  if (p.node_id !== "") {
    return `step ${p.node_id}`;
  }
  return "";
}
