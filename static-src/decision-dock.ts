// ---------------------------------------------------------------------------
// The interaction dock: where the agent asks the user something.
//
// Three things need an answer mid-turn — a permission request, an MCP
// elicitation form, and a structured question — and all three used to be
// centered <dialog> modals with a backdrop. They are one region now, docked in
// the BOTTOM BAR: above the message box, inside the bar so it shares its
// background, and never inside the text input. The bar grows upward to expose
// it and shrinks back when it is answered.
//
// Why a dock and not a modal, in order of how much each cost:
//
//   - A modal blocks the transcript. The decision is ABOUT the transcript
//     ("may I run this", "which of these files do you want"), and the modal
//     covered the evidence needed to answer it. In the dock the user can
//     scroll, read the diff, open a file, and then answer.
//   - It is a bottom-bar region rather than a composer feature on purpose: the
//     workflow-run tab has no composer, so a dock owned by the prompt box
//     would leave a run with nowhere to ask. `mountDecisionDock(host)` takes
//     its host, and any bottom bar can be one.
//   - Focus is NOT trapped (it was, in two of the three). Trapping focus in a
//     non-modal region is a bug, not a feature: the user is meant to leave it
//     and come back. `focus-trap.ts` went with the modals.
//
// The queue is this module's, and it is per-chat. The old permission dialog had
// no queue at all: a second request re-rendered the same element and the first
// one's callback was dropped on the floor, leaving KAS waiting forever on an id
// nothing would ever answer. Decisions are keyed by chat so switching tabs and
// coming back still shows the ask — previously the SSE handler's active-chat
// gate dropped it and only a reconnect brought it back.
// ---------------------------------------------------------------------------

import { el, signal, effect } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { activeSession } from "./store.js";
import { buildPermissionCard } from "./permission.js";
import { buildElicitationCard } from "./elicitation.js";
import { buildUserInputCard } from "./user-input.js";
import type {
  PermissionNeededPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
} from "./types.js";

/** A permission ask, including the turn-approval variety (payload.files). */
interface PermissionDecision {
  kind: "permission";
  chatID: string;
  requestID: number;
  payload: PermissionNeededPayload;
  /** Answer. `fileDecisions` is sent only for a turn approval. */
  submit: (optionID: string, fileDecisions?: Record<string, boolean>) => void;
}

interface ElicitationDecision {
  kind: "elicitation";
  chatID: string;
  requestID: number;
  payload: ElicitationNeededPayload;
  submit: (action: "accept" | "decline" | "cancel", content?: Record<string, unknown>) => void;
}

interface UserInputDecision {
  kind: "user_input";
  chatID: string;
  requestID: number;
  payload: UserInputNeededPayload;
  submit: (action: "answered" | "dismissed", answer?: string) => void;
}

/** The dock's input. Only the union is exported — a caller enqueues a
 *  decision, and the per-kind shapes are this module's business. */
export type Decision = PermissionDecision | ElicitationDecision | UserInputDecision;

/** Per-chat FIFO of unanswered decisions. The head is the one on screen. */
const queues = new Map<string, Decision[]>();

/** Bumped on every queue mutation so the render effect re-runs. The active
 *  chat id alone is not enough of a trigger: a decision arriving for the chat
 *  already on screen changes nothing the store can see. */
const queueVersion = signal(0);
function bump(): void {
  queueVersion.value = queueVersion.peek() + 1;
}

let host: HTMLElement | null = null;
let renderedKey = "";

/** Wire the dock into a bottom bar. Idempotent per host. */
export function mountDecisionDock(hostEl: HTMLElement): void {
  if (host === hostEl) {
    return;
  }
  host = hostEl;
  // Two triggers: the active chat changed (a different queue is on screen), or
  // this module's queue changed (a decision arrived or was answered).
  effect(() => {
    void activeSession.value?.id;
    void queueVersion.value;
    renderActive();
  });
}

function activeChatID(): string {
  return activeSession.peek()?.id ?? "";
}

/** Enqueue a decision and show it when its chat is the one on screen. */
export function pushDecision(d: Decision): void {
  const q = queues.get(d.chatID) ?? [];
  // A re-delivered request (SSE reconnect replays every unanswered permission)
  // must not stack a second copy of itself.
  if (q.some((existing) => existing.kind === d.kind && existing.requestID === d.requestID)) {
    return;
  }
  q.push(d);
  queues.set(d.chatID, q);
  bump();
}

/** Drop a decision without answering it: the ask is no longer live (the turn
 *  was cancelled, or the chat went away). Does NOT call submit — nothing is
 *  owed to a request the agent has abandoned. */
export function dropDecisions(chatID: string): void {
  if (!queues.delete(chatID)) {
    return;
  }
  bump();
}

/** Answer the head decision and advance the queue. The settle-once guard is
 *  here rather than in each card, because "answered already" is a property of
 *  the queue entry, not of the DOM that rendered it. */
function settle(d: Decision, run: () => void): void {
  const q = queues.get(d.chatID);
  if (q?.[0] !== d) {
    // Superseded or already answered: the reply would target a request the
    // server has forgotten, and a double answer on one request id is worse
    // than a dropped click.
    return;
  }
  q.shift();
  if (q.length === 0) {
    queues.delete(d.chatID);
  }
  run();
  bump();
}

function renderActive(): void {
  if (host === null) {
    return;
  }
  const chatID = activeChatID();
  const q = queues.get(chatID);
  const head = q?.[0];

  if (head === undefined) {
    if (renderedKey !== "") {
      host.replaceChildren();
      host.classList.add("hidden");
      renderedKey = "";
    }
    return;
  }

  // Rebuilding a card the user is filling in would discard their typing, so a
  // render for the same decision is a no-op. Only the depth line, which lives
  // outside the card, updates in place.
  const key = `${chatID}\u0000${head.kind}\u0000${String(head.requestID)}`;
  const depth = q?.length ?? 1;
  if (key === renderedKey) {
    updateDepth(depth);
    return;
  }

  host.replaceChildren(buildCard(head), depthRow(depth));
  host.classList.remove("hidden");
  renderedKey = key;
  announce(announcementFor(head));
}

function updateDepth(depth: number): void {
  const row = host?.querySelector(".dock-depth");
  if (row === null || row === undefined) {
    return;
  }
  row.textContent = depthText(depth);
  row.classList.toggle("hidden", depth < 2);
}

function depthRow(depth: number): HTMLElement {
  const row = el("p", { className: "dock-depth" }, depthText(depth));
  row.classList.toggle("hidden", depth < 2);
  return row;
}

function depthText(depth: number): string {
  return depth < 2 ? "" : `${String(depth - 1)} more waiting`;
}

function announcementFor(d: Decision): string {
  switch (d.kind) {
    case "permission":
      return d.payload.files !== undefined && d.payload.files.length > 0
        ? "Review this turn's changes"
        : "The agent needs permission";
    case "elicitation":
      return "A tool is requesting input";
    case "user_input":
      return "The agent has a question";
    default:
      d satisfies never;
      return "";
  }
}

function buildCard(d: Decision): HTMLElement {
  switch (d.kind) {
    case "permission":
      return buildPermissionCard(d.chatID, d.payload, (optionID, fileDecisions) => {
        settle(d, () => {
          d.submit(optionID, fileDecisions);
        });
      });
    case "elicitation":
      return buildElicitationCard(d.payload, (action, content) => {
        settle(d, () => {
          d.submit(action, content);
        });
      });
    case "user_input":
      return buildUserInputCard(d.payload, (action, answer) => {
        settle(d, () => {
          d.submit(action, answer);
        });
      });
    default:
      d satisfies never;
      return el("div");
  }
}

/** Reset module state for test isolation. Production never calls this. */
export function _resetForTest(): void {
  queues.clear();
  host = null;
  renderedKey = "";
  queueVersion.value = 0;
}
