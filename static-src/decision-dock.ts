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
  /** The workflow run this ask belongs to, when it is a step's ("" otherwise).
   *  What lets a run tab render an ask that is KEYED to the launching chat. */
  runID?: string;
  requestID: number;
  payload: PermissionNeededPayload;
  /** Answer. `fileDecisions` is sent only for a turn approval. */
  submit: (optionID: string, fileDecisions?: Record<string, boolean>) => void;
}

interface ElicitationDecision {
  kind: "elicitation";
  chatID: string;
  runID?: string;
  requestID: number;
  payload: ElicitationNeededPayload;
  submit: (action: "accept" | "decline" | "cancel", content?: Record<string, unknown>) => void;
}

interface UserInputDecision {
  kind: "user_input";
  chatID: string;
  runID?: string;
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

/** One mounted dock: its element, which decisions it shows, and what it last
 *  rendered. The composer's dock shows the ACTIVE CHAT's queue; a run tab's
 *  shows one RUN's decisions wherever they are keyed — an agent-launched run's
 *  ask lives under the launching chat's id, a manual one's under `run:<id>`,
 *  and the run tab must render both. */
interface DockHost {
  el: HTMLElement;
  match: (d: Decision) => boolean;
  renderedKey: string;
}

const hosts: DockHost[] = [];

/** Wire the composer's dock into the chat bottom bar. Idempotent per host. */
export function mountDecisionDock(hostEl: HTMLElement): void {
  addHost({
    el: hostEl,
    match: (d) => d.chatID === activeChatID(),
    renderedKey: "",
  });
}

/** Wire the run view's dock: it shows the CURRENT run's decisions regardless
 *  of which surface they are keyed to — an agent-launched run's ask is queued
 *  under the launching chat's id, a manual one's under `run:<id>`, and the run
 *  tab must render both. One shared view element serves every run tab, so the
 *  run is a GETTER and a tab switch re-keys the same host (the switch bumps the
 *  queue version via renderRunDock). Idempotent per host element. */
export function mountRunDecisionDock(hostEl: HTMLElement, runID: () => string): void {
  addHost({
    el: hostEl,
    match: (d) => {
      const id = runID();
      return id !== "" && (d.runID === id || d.chatID === `run:${id}`);
    },
    renderedKey: "",
  });
}

/** Re-render every dock. The run view calls this when the run on screen
 *  changes: the host's match closure reads new state the reactive graph cannot
 *  see, so the repaint needs an explicit nudge. */
export function rerenderDocks(): void {
  bump();
}

function addHost(h: DockHost): void {
  if (hosts.some((existing) => existing.el === h.el)) {
    return;
  }
  hosts.push(h);
  // Two triggers: the active chat changed (a different queue is on screen), or
  // this module's queue changed (a decision arrived or was answered).
  effect(() => {
    void activeSession.value?.id;
    void queueVersion.value;
    renderHost(h);
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

/** Answer a decision and retire it. The settle-once guard is here rather than
 *  in each card, because "answered already" is a property of the queue entry,
 *  not of the DOM that rendered it.
 *
 *  Membership, not head position, is the guard. With one host they were the
 *  same thing; with a run tab in play they are not — the tab can legitimately
 *  render a step's ask that sits BEHIND the chat's own ask in the launching
 *  chat's queue, and refusing that answer would leave a dead button. Each
 *  request id is its own JSON-RPC exchange, so answering out of queue order is
 *  protocol-correct; what must never happen is answering one twice, and
 *  splice-by-identity gives exactly that. */
function settle(d: Decision, run: () => void): void {
  const q = queues.get(d.chatID);
  const i = q?.indexOf(d) ?? -1;
  if (q === undefined || i < 0) {
    // Already answered or dropped: the reply would target a request the
    // server has forgotten, and a double answer on one request id is worse
    // than a dropped click.
    return;
  }
  q.splice(i, 1);
  if (q.length === 0) {
    queues.delete(d.chatID);
  }
  run();
  bump();
}

/** The decisions a host should show, across every queue, FIFO within each.
 *  Queue iteration order is insertion order, which is stable enough: a host
 *  either matches one chat's queue (the composer) or filters by run id, where
 *  cross-queue order barely arises (one run's asks come from one bridge). */
function matching(h: DockHost): Decision[] {
  const out: Decision[] = [];
  for (const q of queues.values()) {
    for (const d of q) {
      if (h.match(d)) {
        out.push(d);
      }
    }
  }
  return out;
}

function renderHost(h: DockHost): void {
  const mine = matching(h);
  const head = mine[0];

  if (head === undefined) {
    if (h.renderedKey !== "") {
      h.el.replaceChildren();
      h.el.classList.add("hidden");
      h.renderedKey = "";
    }
    return;
  }

  // Rebuilding a card the user is filling in would discard their typing, so a
  // render for the same decision is a no-op. Only the depth line, which lives
  // outside the card, updates in place.
  const key = `${head.chatID}\u0000${head.kind}\u0000${String(head.requestID)}`;
  const depth = mine.length;
  if (key === h.renderedKey) {
    updateDepth(h, depth);
    return;
  }

  h.el.replaceChildren(buildCard(head), depthRow(depth));
  h.el.classList.remove("hidden");
  h.renderedKey = key;
  announce(announcementFor(head));
}

function updateDepth(h: DockHost, depth: number): void {
  const row = h.el.querySelector(".dock-depth");
  if (row === null) {
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
  hosts.length = 0;
  queueVersion.value = 0;
}
