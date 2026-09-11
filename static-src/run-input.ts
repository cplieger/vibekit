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
import { attachClamp } from "./clamp-text.js";
import { RUN_INPUT_FALLBACK } from "./decision-dock.js";
import type { RunInputNeededPayload } from "./types.js";

/** `null` is "continue without answering"; a string is the answer. */
type SubmitFn = (text: string | null) => void;

/** Hand the question to the agent that launched this run. Rejects when the
 *  hand-off did not go out, which is what re-enables the button. */
type DeferFn = () => void | Promise<void>;

/** Lines the question shows before its opener. FOUR, the count `.steer-text`
 *  already uses one region down the same bar and for the same reason: the bar
 *  grows UPWARD into the transcript, so a question the agent wrote at length
 *  costs the reader the conversation it is about. The stylesheet clamps to this
 *  same count (`clamp-line-count.test.ts` holds the two together). */
const CLAMP_LINES = 4;

/** Build the dock card for one parked workflow step.
 *
 *  The reporter is threaded through rather than parked in module state, for
 *  `buildUserInputCard`'s reason and it is a correctness requirement rather than
 *  a style choice: the dock keeps an ANSWERED card on screen for the length of its
 *  advance animation, so two cards coexist, and a module-level reporter would be
 *  overwritten by the incoming one — after which the outgoing card's buttons would
 *  answer the INCOMING decision, which `settle`'s membership guard cannot catch
 *  because that decision is legitimately still queued.
 *
 *  `held` is the text a previous send is still holding for this ask, or "": the dock
 *  splices the card before the answer goes out, so a retryable refusal re-offers the
 *  question and this is what stops the box coming back empty. Seeded rather than
 *  restored, because the card is a fresh element each time.
 *
 *  `onDefer`'s PRESENCE is the chat-parented discriminator at this boundary: the
 *  card never learns a chat id, and a deferral never routes through `onSubmit`,
 *  because the two verbs disagree — skip lets the step proceed with no answer,
 *  while a deferral leaves the ask open and asks somebody else. */
export function buildRunInputCard(
  payload: RunInputNeededPayload,
  held: string,
  onSubmit: SubmitFn,
  onDefer?: DeferFn,
): HTMLElement {
  // An EMPTY question is the post-restart case rather than a malformed frame, so
  // it gets a sentence of its own instead of a blank heading. Shared with the
  // dock's own one-line label so the card and the run card's alert agree.
  const question = payload.question === "" ? RUN_INPUT_FALLBACK : payload.question;
  const text = el("strong", { className: "run-input-question" }, question);
  // A SIBLING of the clamped element, or the clamp would hide its own opener.
  // `attachClamp` hides it until measurement says the text overflows, and the
  // dock releases it when the card leaves (`releaseClampsIn` in `swap`).
  const more = el("button", {
    className: "run-input-more",
    type: "button",
  }) as HTMLButtonElement;
  const body = el("div", { className: "run-input-body" }, text, more);
  attachClamp(text, more, { lines: CLAMP_LINES });

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
  // A property write rather than an attribute, which is what a textarea's value is
  // after first paint; "" is the ordinary case and writes the same empty box.
  input.value = held;

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

  if (onDefer !== undefined) {
    actions.appendChild(deferButton(onDefer));
  } else if (payload.node_id !== "") {
    // WITHHELD on an ask with no node id, because the verb behind it cannot be
    // addressed: `set_step_status` takes a node and refuses 400 without one, so the
    // button could only ever produce an error toast — with the card already spliced
    // by the dock's settle, which leaves the reader worse off than not offering it.
    // Such an ask is still ANSWERABLE (the answer is addressed by session, not by
    // node), so Send stays and only the waive is gone.
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

/** Ask the launching agent instead, on a CHAT-PARENTED ask. The node-id gate
 *  above is Continue's alone — a deferral is addressed by CHAT, so an ask carrying
 *  no node id still gets one.
 *
 *  Hand-rolled rather than `withAsyncFeedback`, which restores the label after
 *  ~1200ms: this is a durable hand-off state that has to last the card's life,
 *  because the ask stays open and a reader who comes back needs to see that the
 *  agent was already asked. No CSS either — `css/40-a11y.css` floors `:disabled`,
 *  and a disabled button still receives hover, so the tooltip keeps working. */
function deferButton(onDefer: DeferFn): HTMLButtonElement {
  const b = el(
    "button",
    { type: "button", className: "btn-small" },
    "Defer to parent agent",
  ) as HTMLButtonElement;
  b.setAttribute(
    "data-tooltip",
    "The agent that launched this run is asked to answer instead. The question stays open, " +
      "so you can still answer it yourself.",
  );
  b.addEventListener("click", () => {
    // The re-entrancy guard, and it is FIRST: the label only changes once the
    // hand-off resolves, so without it a second click posts a second prompt.
    b.disabled = true;
    void Promise.resolve(onDefer()).then(
      () => {
        b.textContent = "Asked the agent";
        b.setAttribute(
          "data-tooltip",
          "The agent that launched this run has been asked to answer. The question is still " +
            "open, so you can still answer it yourself.",
        );
      },
      () => {
        b.disabled = false;
      },
    );
  });
  return b;
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
