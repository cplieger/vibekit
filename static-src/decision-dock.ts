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
import { info } from "./toast.js";
import type {
  PermissionNeededPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  SettledBy,
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

/** Which of the three motion phases a host is in. The attribute
 *  `data-dock-phase` carries the same word into `css/26-dock.css`, which owns
 *  every duration, easing and keyframe. */
type Phase = "entering" | "leaving" | "advancing";

/** The phase windows in milliseconds, the TWIN of the durations in
 *  `css/26-dock.css` (`--dur-standard`, `--dock-exit-dur`, `--dur-exit`).
 *
 *  There is deliberately no `transitionend` or `animationend` listener anywhere
 *  in this module: one timer per host is the sole cleanup authority, which
 *  removes a whole class of leak because there is no listener to orphan. The
 *  cost of that choice is this duplication, so `decision-dock.test.ts` reads the
 *  durations back out of the stylesheet and asserts the two agree — a retune of
 *  one side cannot silently desynchronise the cleanup from the animation.
 *
 *  Exported for that test only; nothing else reads it. */
export const DOCK_PHASE_MS: Readonly<Record<Phase, number>> = {
  entering: 200,
  leaving: 120,
  advancing: 150,
};

/** The two inline values a phase writes on the host box, as strings. `""` means
 *  "hand the property back to the stylesheet" — `height: auto` and
 *  `margin-block-end: var(--sp-2)`. */
interface BoxState {
  height: string;
  margin: string;
}

/** One mounted dock: its element, which decisions it shows, what it last
 *  rendered, and the live phase's three pieces of state. The composer's dock
 *  shows the ACTIVE CHAT's queue; a run tab's shows one RUN's decisions wherever
 *  they are keyed — an agent-launched run's ask lives under the launching chat's
 *  id, a manual one's under `run:<id>`, and the run tab must render both.
 *
 *  Each host owns its own phase, so the composer dock and a run dock rendering
 *  the same decision can be at different points without interfering. */
interface DockHost {
  el: HTMLElement;
  match: (d: Decision) => boolean;
  renderedKey: string;
  /** The answered card, kept on screen (neutralised) for the phase's duration.
   *  At most one per host, ever. */
  outgoing: HTMLElement | null;
  /** The cleanup timer. `clearTimeout` is the optimisation; `gen` is the
   *  guarantee. */
  timer: ReturnType<typeof setTimeout> | null;
  /** Bumped by `endPhase`. A timer callback whose generation has moved on
   *  returns without touching the DOM. */
  gen: number;
}

const hosts: DockHost[] = [];

/** Wire the composer's dock into the chat bottom bar. Idempotent per host. */
export function mountDecisionDock(hostEl: HTMLElement): void {
  addHost(hostEl, (d) => d.chatID === activeChatID());
}

/** Wire the run view's dock: it shows the CURRENT run's decisions regardless
 *  of which surface they are keyed to — an agent-launched run's ask is queued
 *  under the launching chat's id, a manual one's under `run:<id>`, and the run
 *  tab must render both. One shared view element serves every run tab, so the
 *  run is a GETTER and a tab switch re-keys the same host (the switch bumps the
 *  queue version via renderRunDock). Idempotent per host element. */
export function mountRunDecisionDock(hostEl: HTMLElement, runID: () => string): void {
  addHost(hostEl, (d) => {
    const id = runID();
    return id !== "" && (d.runID === id || d.chatID === `run:${id}`);
  });
}

/** Re-render every dock. The run view calls this when the run on screen
 *  changes: the host's match closure reads new state the reactive graph cannot
 *  see, so the repaint needs an explicit nudge. */
export function rerenderDocks(): void {
  bump();
}

function addHost(hostEl: HTMLElement, match: (d: Decision) => boolean): void {
  if (hosts.some((existing) => existing.el === hostEl)) {
    return;
  }
  const h: DockHost = {
    el: hostEl,
    match,
    renderedKey: "",
    outgoing: null,
    timer: null,
    gen: 0,
  };
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

/** Drop every decision keyed to this surface without answering: the chat went
 *  away (its tab closed, or another device deleted it) or the client can no
 *  longer support the claim that any of them is live (a transport gap). Does NOT
 *  call submit — nothing is owed to a request the agent has abandoned.
 *
 *  Unconditional, unlike `dropTurnDecisions`: when the chat itself is gone there
 *  is no surface left to answer on, and `close_chat` / `delete_chat` cancel the
 *  chat's runs server-side, so a run-scoped ask keyed here is dead too. */
export function dropDecisions(chatID: string): void {
  if (!queues.delete(chatID)) {
    return;
  }
  bump();
}

/** Drop the decisions a TURN owned, leaving a workflow run's alone.
 *
 *  A permission, an elicitation and a question all BLOCK the turn that raised
 *  them, so a turn that has ended cannot still be waiting on one: it was
 *  answered (already spliced), or it was abandoned when the turn was cancelled —
 *  and `cmdCancel` clears the server's own pending set, so the entry left here is
 *  a card for a request nothing will accept an answer for. Keeping it marked the
 *  chat as needing a decision indefinitely, since `tabStatusFor` puts `input`
 *  ahead of every other state.
 *
 *  A run-scoped ask is exempt and the exemption is load-bearing: an
 *  agent-launched run is parented on the calling chat's session but OUTLIVES the
 *  turn that launched it (a goal run ends its turn immediately and then runs),
 *  and its asks are queued under that chat's id. Dropping those would strand the
 *  run waiting for an answer no surface is offering any more, which is the exact
 *  failure the dock's queue was built to end. */
export function dropTurnDecisions(chatID: string): void {
  const q = queues.get(chatID);
  if (q === undefined) {
    return;
  }
  const rest = q.filter((d) => d.runID !== undefined && d.runID !== "");
  if (rest.length === q.length) {
    return;
  }
  if (rest.length === 0) {
    queues.delete(chatID);
  } else {
    queues.set(chatID, rest);
  }
  bump();
}

/** Does this chat hold an unanswered decision? The tab strip's activity dot asks,
 *  so a background chat blocked on a permission stops looking identical to one
 *  that is merely working.
 *
 *  Reads `queueVersion.value` (not `.peek()`) on purpose: the caller is a
 *  reactive effect, and this read is what subscribes it to the arrival and the
 *  answering of a decision. The queue is this module's state, so there is
 *  nothing in the chat store for that effect to have keyed on instead.
 *
 *  A `run:<id>` key is not a chat and has no tab, so it simply never matches. */
export function hasPendingDecision(chatID: string): boolean {
  void queueVersion.value;
  return (queues.get(chatID)?.length ?? 0) > 0;
}

/** Retire a decision ANOTHER surface answered (`decision_settled`), and say who
 *  answered it.
 *
 *  Every surface is offered the same ask — each tab, plus a run tab watching the
 *  same run — and only the first answer is accepted, so on every other surface
 *  the card outlives the question. It used to sit there looking live, and
 *  clicking it achieved nothing: the server took the answer, the agent ignored
 *  it for a request id it had already resolved, and nothing said so.
 *
 *  It does NOT call submit: the request is answered, and a second answer on one
 *  id is exactly what this exists to prevent.
 *
 *  Attribution is only surfaced when the card was ON SCREEN, which `renderedKey`
 *  is the record of. A card the reader never saw needs no explanation, and the
 *  surface that DID answer reaches this with nothing to remove — `settle` splices
 *  the entry before the answer goes out — so it never explains itself to the
 *  person who just clicked. */
export function collapseSettledDecision(
  chatID: string,
  kind: Decision["kind"],
  requestID: number,
  settledBy: SettledBy,
): void {
  const q = queues.get(chatID);
  const i = q?.findIndex((d) => d.kind === kind && d.requestID === requestID) ?? -1;
  if (q === undefined || i < 0) {
    return;
  }
  q.splice(i, 1);
  if (q.length === 0) {
    queues.delete(chatID);
  }
  const key = decisionKey(chatID, kind, requestID);
  if (hosts.some((h) => h.renderedKey === key)) {
    // The toast announces itself into the shared live region (politely), so a
    // second announce() call here would read the same sentence twice.
    info(settledMessage(kind, settledBy));
  }
  bump();
}

/** What the reader is told about a card that collapsed under them. The two
 *  causes call for different reactions, so they read differently: another window
 *  means a person decided, and the unattended floor means a deadline did. */
function settledMessage(kind: Decision["kind"], settledBy: SettledBy): string {
  const subject = settledSubject(kind);
  if (settledBy === "unattended") {
    return `${subject} was answered automatically because nobody was watching.`;
  }
  return `${subject} was answered in another window.`;
}

function settledSubject(kind: Decision["kind"]): string {
  switch (kind) {
    case "permission":
      return "The permission request";
    case "elicitation":
      return "The input request";
    case "user_input":
      return "The agent's question";
    default:
      kind satisfies never;
      return "The request";
  }
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

/** Identity of one decision as a string, so "what is rendered" can be compared
 *  against an event naming a decision this module may not be holding. */
function decisionKey(chatID: string, kind: Decision["kind"], requestID: number): string {
  return `${chatID}\u0000${kind}\u0000${String(requestID)}`;
}

function renderHost(h: DockHost): void {
  const mine = matching(h);
  const head = mine[0];

  if (head === undefined) {
    // `renderedKey` is cleared the moment a `leaving` phase starts, so a second
    // render with an empty queue does not restart the exit.
    if (h.renderedKey !== "") {
      swap(h, undefined, 0);
    }
    return;
  }

  // Rebuilding a card the user is filling in would discard their typing, so a
  // render for the same decision is a no-op. Only the depth line, which lives
  // outside the card, updates in place. This branch MUST NOT start a phase.
  const key = decisionKey(head.chatID, head.kind, head.requestID);
  const depth = mine.length;
  if (key === h.renderedKey) {
    updateDepth(h, depth);
    return;
  }

  swap(h, head, depth);
}

// ---------------------------------------------------------------------------
// The phase machine.
//
// `renderHost` is still the only funnel and it gained no lookahead: the phase
// falls out of two facts it already had. Whether another decision is queued
// behind the answered one is not DETECTED, it is OBSERVED — `settle` splices the
// answered entry and then calls `bump()`, so by the time the render effect runs,
// `matching(h)[0]` is already the NEXT decision, or undefined when the queue is
// empty. The advance is therefore the ordinary render path, keyed on nothing
// new: no coupling to click handlers, no timing dependency on an animation, no
// change to queue semantics. Every retirement path (`settle`,
// `collapseSettledDecision`, `dropDecisions`, `dropTurnDecisions`) animates
// identically for free, because they all splice then bump.
//
// The dispatch is NEVER gated on any of this. `settle` splices, calls `submit`
// and bumps synchronously inside the click handler, so the response is on the
// wire BEFORE the render effect that starts the animation runs.
// ---------------------------------------------------------------------------

/** Swap the host's content, animating the change unless motion is off.
 *
 *  | had content on screen | has an incoming head | phase     |
 *  |-----------------------|----------------------|-----------|
 *  | no                    | yes                  | entering  |
 *  | yes                   | yes                  | advancing |
 *  | yes                   | no                   | leaving   |
 *
 *  (No/no is unreachable: `renderHost` returns early.) */
function swap(h: DockHost, head: Decision | undefined, depth: number): void {
  // Measured FIRST, before endPhase and before any write, so it reads the LIVE
  // height. One code path then serves all three starting states: collapsed
  // (0), settled (the card's height), and mid-animation (wherever the box
  // currently is) — which is what makes rapid Allow-Allow-Allow morph from the
  // current geometry instead of snapping.
  const from = measure(h);
  const phase: Phase =
    head === undefined ? "leaving" : h.renderedKey === "" ? "entering" : "advancing";

  // Idempotent, and every phase starts with it: at most one timer and one
  // outgoing element per host, ever.
  endPhase(h);

  // Reduced motion and a background tab take NO phase at all: same final DOM,
  // same response behaviour, no outgoing element and no timer, so there is
  // nothing left to clean up. `document.hidden` is in the gate for a real
  // reason — animations do not advance in a background tab but `setTimeout`
  // does, so without it the user would return to a stale outgoing card sitting
  // at full opacity over the new one.
  if (motionOff()) {
    if (head === undefined) {
      h.el.replaceChildren();
      h.el.classList.add("hidden");
      h.renderedKey = "";
    } else {
      show(h, head, depth);
    }
    return;
  }

  // The answered card stays on screen for the phase. Nothing to take on an
  // enter: the host is either empty or mid-collapse with its content already
  // detached.
  const outgoing = phase === "entering" ? null : takeOutgoing(h);

  if (head === undefined) {
    // `.hidden` deliberately does NOT land here — it lands in `finishPhase`,
    // after the collapse. Adding it now is what made the old exit unanimatable.
    h.el.replaceChildren();
    h.renderedKey = "";
  } else {
    show(h, head, depth);
  }
  if (outgoing !== null) {
    h.el.prepend(outgoing);
    h.outgoing = outgoing;
  }

  h.el.dataset["dockPhase"] = phase;
  pinAndRelease(
    h,
    from,
    phase === "leaving" ? { height: "0px", margin: "0px" } : { height: "", margin: "" },
  );

  const gen = h.gen;
  h.timer = setTimeout(() => {
    finishPhase(h, gen, phase);
  }, DOCK_PHASE_MS[phase]);
}

/** Put a decision on screen. Identical on the animated and the instant path, so
 *  `announce()` keeps firing on exactly today's schedule — once per new head,
 *  with the card already in the DOM and never itself opacity-0. */
function show(h: DockHost, head: Decision, depth: number): void {
  h.el.replaceChildren(buildCard(head), depthRow(depth));
  h.el.classList.remove("hidden");
  h.renderedKey = decisionKey(head.chatID, head.kind, head.requestID);
  announce(announcementFor(head));
}

/** The box's current geometry. A collapsed host is `display: none`, which
 *  occupies nothing at all — margin included — so its margin start is 0 rather
 *  than the resting value `getComputedStyle` still reports for it. */
function measure(h: DockHost): BoxState {
  const height = `${String(h.el.getBoundingClientRect().height)}px`;
  if (h.el.classList.contains("hidden")) {
    return { height, margin: "0px" };
  }
  return { height, margin: getComputedStyle(h.el).marginBlockEnd };
}

/** Pin the box at a measured geometry with transitions SUPPRESSED, then release
 *  to the target.
 *
 *  The suppression is the single most load-bearing mechanical fact here.
 *  Measured in Chromium: writing the pin with transitions live starts a height
 *  transition for the PIN, and clearing the inline value in the same task then
 *  cancels it and starts no replacement — the box snaps. A forced reflow alone
 *  does not fix it and neither does waiting two animation frames. Suppressing
 *  for the pin's duration makes the release the only style change the transition
 *  machinery sees. */
function pinAndRelease(h: DockHost, from: BoxState, to: BoxState): void {
  const s = h.el.style;
  s.transition = "none";
  s.height = from.height;
  s.marginBlockEnd = from.margin;
  // Flush: the pin has to land in a COMPLETED style resolution.
  void getComputedStyle(h.el).height;
  s.transition = "";
  s.height = to.height;
  s.marginBlockEnd = to.margin;
}

/** Move the answered content into a neutralised wrapper that stays on screen for
 *  the phase. `aria-hidden` so it is not read a second time, `inert` so it can
 *  be neither tabbed into nor clicked (the retained-handle case is also caught
 *  by `settle`'s membership guard, which is the authoritative one). Moving a
 *  focused button into an inert subtree blurs it, which is exactly what the
 *  previous `replaceChildren()` already produced. */
function takeOutgoing(h: DockHost): HTMLElement | null {
  const kids = [...h.el.children];
  if (kids.length === 0) {
    return null;
  }
  const wrap = el("div", { className: "dock-outgoing", "aria-hidden": "true" });
  wrap.setAttribute("inert", "");
  wrap.append(...kids);
  return wrap;
}

/** Tear down whatever phase this host is in. Idempotent, and safe to call when
 *  there is no phase. */
function endPhase(h: DockHost): void {
  h.gen++;
  if (h.timer !== null) {
    clearTimeout(h.timer);
    h.timer = null;
  }
  h.outgoing?.remove();
  h.outgoing = null;
  delete h.el.dataset["dockPhase"];
  h.el.style.transition = "";
  h.el.style.height = "";
  h.el.style.marginBlockEnd = "";
}

/** The phase's window elapsed. The generation check is what makes a superseded
 *  timer harmless even if it somehow escaped `clearTimeout`. */
function finishPhase(h: DockHost, gen: number, phase: Phase): void {
  if (h.gen !== gen) {
    return;
  }
  endPhase(h);
  if (phase === "leaving") {
    h.el.classList.add("hidden");
  }
}

/** Animate nothing: the reader asked for no motion, or the tab is in the
 *  background where animations do not advance. Read live per transition rather
 *  than cached, so flipping the preference takes effect on the next swap. */
function motionOff(): boolean {
  if (document.hidden) {
    return true;
  }
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** Scoped to a DIRECT child: a stale depth row inside `.dock-outgoing` sits
 *  earlier in document order and would win an unscoped lookup. */
function updateDepth(h: DockHost, depth: number): void {
  const row = h.el.querySelector(":scope > .dock-depth");
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

/** Reset module state for test isolation. Production never calls this.
 *
 *  `endPhase` first, and not optionally: a pending cleanup timer from one test
 *  would otherwise fire into the next test's DOM. */
export function _resetForTest(): void {
  for (const h of hosts) {
    endPhase(h);
  }
  queues.clear();
  hosts.length = 0;
  queueVersion.value = 0;
}
