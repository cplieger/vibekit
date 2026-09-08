// The WORKFLOW door into `exec-view/`, the shared subpage view: wiring only, folding
// KAS's `inspect` through `run-exec-source.ts` into that model, and owning the three
// things only a workflow knows — the status's verbs, what an empty step body means, and
// where a `run_step` frame renders. It is also the ONLY surface hosting a step
// transcript. Closing a run tab stops nothing (`owns: false` at every door, no
// `onClose`); stopping a run is the Cancel VERB.

import { el, effect, touch } from "@cplieger/reactive";
import { bindLoadingState } from "./actions/index.js";
import { hasTab, openRunTab, openTab, parentChatRef, tabIdFor } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks, runPendingAsks } from "./decision-dock.js";
import { cancelRun, pauseRun, resumeRun, retryRun } from "./actions/runs.js";
import { CONTROL_LABEL, offeredVerbs, refusalSentences, type RunVerb } from "./run-controls.js";
import { get, isThinking, messagesVersionOf } from "./store.js";
import { blockTextSigs, blockThinkingSigs } from "./store-signals.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight, neverRan, settled } from "./exec-view/status.js";
import { flatten, leaves, type ExecNode } from "./exec-view/model.js";
import { runToExec } from "./run-exec-source.js";
import type { RunStepStream } from "./run-step-blocks.js";
import type { RunChatStepStream, RunStepPaint } from "./run-chat-steps.js";
import { sliceRunSteps, type RunStepSlice } from "./run-step-slice.js";
import {
  clearStepTranscripts,
  requestStepTranscript,
  stepRead,
  stepSliceFor,
  stepTranscriptVersion,
} from "./run-step-transcript.js";
import {
  invalidateRun,
  invalidateRunControls,
  noteRunChat,
  runControls,
  runState,
  runChatID,
  runPlan,
  type RunState,
} from "./run-store.js";
import type { RunControlsResponse } from "./wire/types.gen.js";
import { refreshRunDots, trackRun } from "./run-dots.js";
import { buildPath } from "./router.js";
import { iconEl } from "./icon-el.js";
import { ICON_EXTERNAL } from "./icons.js";
import { parseStepSubtask } from "./step-subtask.js";
import type { RunStepPayload } from "./types.js";

/** Verb → its action. Separate from run-controls.ts on purpose: that module is
 *  the pure RULE and must stay importable without the actions framework; this is
 *  the wiring. */
const RUN_ACTION: Record<
  RunVerb,
  { readonly name: string; dispatch: (id: string) => Promise<unknown> }
> = {
  pause: pauseRun,
  resume: resumeRun,
  cancel: cancelRun,
  retry: retryRun,
};

/** The run this view is currently showing, so an SSE invalidation knows whether
 *  it is about the run on screen. Cleared when the view loads a different one;
 *  a closed tab simply stops matching, because the next open reassigns it. */
let shownRun = "";

/** Whether the shown run was launched by an AGENT, from the run's own state
 *  (`paint` sets it from `inspect`'s `parentSessionId`). It decides what the empty-step
 *  note says and whether the door beside it is offered, and nothing else: the control
 *  row is the SERVER's answer now, so no verb is gated on it here. */
let shownRunChatParented = false;

/** The launching chat of a run, or "" for a parentless one. TWO sources: `runChatID`
 *  (SSE-fed, so it answers for a live run), then the run tab's persisted
 *  `TabSubject.Parent`, the only answer left once a finished run's lease is released. A
 *  non-empty second answer is written back so every other reader converges on it. */
function launchingChatOf(workflowID: string): string {
  const known = runChatID(workflowID);
  if (known !== "") {
    return known;
  }
  const fromTab = parentChatRef(tabIdFor("run", workflowID));
  if (fromTab !== "") {
    noteRunChat(workflowID, fromTab);
  }
  return fromTab;
}

/** The pending bindings on the control row's buttons, dropped when the row that
 *  carries them goes — either replaced by a new one or discarded with the page.
 *
 *  `bindLoadingState` self-disposes only for an element that was ATTACHED the last
 *  time its effect ran, and this row is built before the caller appends it, so a
 *  button replaced before its first pending flip never reaches that path. Nothing
 *  else would drop it. */
let controlBindings: (() => void)[] = [];

/** The control row on screen and the affordance it was built from, so a render that
 *  did not move the answer hands the SAME row back.
 *
 *  The row is a function of that answer and the pending signals its buttons carry,
 *  and neither moves at the rate this is called: it is built inside the exec page's
 *  one render pass, which runs per `run_progress` frame. Rebuilding there threw away
 *  a live button several times a minute on a busy run. `""` is the no-answer
 *  signature; a real one always carries its separators. */
let controlRow: HTMLElement | null = null;
let controlSig = "";

/** Drop the row on screen and the bindings its buttons hold.
 *
 *  Both callers own a moment the row stops being current: `buildRunControls` when the
 *  answer moved, `mountPage` when the whole page it lived in is disposed. Without the
 *  second, the last row's disposers were held until some later build — bounded to one
 *  row, and still a live effect following an action for a button nothing can see. */
function dropControlRow(): void {
  for (const dispose of controlBindings) {
    dispose();
  }
  controlBindings = [];
  controlRow = null;
  controlSig = "";
}

/** Point the run view at one run and mount its dock.
 *
 *  Exported for the tab factory (tab-materialize.ts): this is a run tab's `onShow`,
 *  and the factory has to name it without importing the openers above, which build
 *  tabs.
 *
 *  ONE argument now. It used to take `parentless`, and the composition root answered
 *  it from the run store's record of which chat launched the run — a map written only
 *  by SSE frames, so every client that had reloaded answered `true` for a
 *  chat-parented run. Parentage is a durable property of the RUN, so `paint` reads it
 *  off `inspect`'s own `parentSessionId` when the first reply lands and no door has to
 *  guess it. */
export function showRun(workflowID: string): void {
  shownRun = workflowID;
  // Until that reply lands the run is treated as PARENTLESS, which is the arm whose
  // sentences describe THIS tab and so cannot mislead about another one. Reset per
  // show, or the previous run's verdict would answer for this one.
  shownRunChatParented = false;
  const dock = document.getElementById("run-dock");
  if (dock !== null) {
    mountRunDecisionDock(dock, () => shownRun);
  }
  rerenderDocks();
  // ONE effect for the life of the module, installed on the first show. It reads
  // `shownRun` through the store, so a tab switch re-points it with no teardown:
  // the previous run's cell simply stops being read. Installing one per show would
  // leak a subscription per tab opened.
  installViewEffect();
  invalidateRun(workflowID);
  // The affordance, once per tab open. `invalidateRunControls` owns the trigger list.
  invalidateRunControls(workflowID);
}

/** The view's single subscription to the store. Idempotent. */
let viewEffectInstalled = false;
function installViewEffect(): void {
  if (viewEffectInstalled) {
    return;
  }
  viewEffectInstalled = true;
  effect(() => {
    const id = shownRun;
    // EVERY change to the launching chat's transcript bumps that chat's version, a
    // delta included (coalesced per microtask), which is how a chat-parented run's
    // projected steps arrive. Read BEFORE the early returns so the effect stays
    // subscribed on the passes that bail; `paint` returns early on an unresolved
    // fetch, so this cannot live there. An unknown chat has nothing to subscribe to,
    // and the run's own invalidation re-runs this effect when the pairing arrives.
    const chat = id === "" ? "" : launchingChatOf(id);
    if (chat !== "") {
      touch(messagesVersionOf(chat));
    }
    // A resolved on-demand read repaints the page. ONE signal for every step, which
    // is the inverse of the run store's per-run cells and right for the same reason
    // reversed: this page shows one node at a time, so a coarse bump costs one
    // repaint of the page the reader is looking at. Read BEFORE the early return,
    // with the others, so the effect stays subscribed on the passes that bail.
    touch(stepTranscriptVersion);
    if (id === "") {
      return;
    }
    paint(id, runState(id));
  });
}

/** Open a run's tab on request — every MANUAL door: the Workflows tab's Run button,
 *  a History row, the run card's footer link, a `/run/{id}` deep link.
 *
 *  NOT read-only, and that reversed with the close contract. History used to open a
 *  review that carried no verbs, on the reasoning that reaching a live run's controls
 *  was the launching tab's job and its × was the stop. With the × disarmed
 *  (tab-materialize.ts) that leaves a live run readable from History with no way to
 *  stop it, so `buildRunControls` gates on the RUN rather than on the door, and every
 *  door offers the same verbs for the same run. `GET /api/sessions` does not filter by
 *  status, so History genuinely does list running and paused runs.
 *
 *  PARENTLESSNESS is not an argument for the DOOR either, and it is now only one
 *  verb's argument at all: a chat-parented run offers pause, resume and cancel
 *  (`hostBridge` resolves the launching chat's bridge) and withholds only retry.
 *  `showRun`'s second parameter asks whether the RUN has a parent agent session,
 *  which is the run's own fact, and the composition root answers it from the run
 *  store when it wires the factory's opener — so a History row and a restored tab
 *  agree instead of each door deciding.
 *
 *  `parentChatID` is a HINT, not the authority: the coordinator fills a run tab's
 *  parent from the run's own lease, so a `/run/{id}` deep link on a browser holding
 *  no state still nests under its conversation. Passing it only saves that lookup,
 *  and a client-supplied parent WINS, which is what lets History name the parent of
 *  a FINISHED run whose lease has been released (`run.parent_chat_id`; nothing new
 *  is fetched for it). Every other door passes the chat it already holds, and
 *  `runChatID` covers the caller that genuinely has none.
 *
 *  It absorbed `openLiveRunView`, which differed only in `owns: true`. With the ×
 *  disarmed there was nothing left to distinguish, and a launcher-opened run is
 *  parentless so it lands top-level through this door too.
 *
 *  `focusNode` is the node the caller wants SELECTED on arrival — the transcript
 *  card's step row is the one door that names one, which is what makes that row a
 *  door rather than a disclosure. Recorded as a one-shot request rather than held,
 *  because a permanent pick would fight the reader the moment they clicked
 *  elsewhere in the tree. */
export function openRunView(
  workflowID: string,
  name: string,
  parentChatID = "",
  focusNode = "",
): void {
  // Replaces any earlier request outright: the last door clicked is the one the
  // reader is waiting on, and two pending picks for one run is a state nothing
  // could resolve honestly.
  focusRequest = focusNode === "" ? undefined : { workflowID, path: focusNode };
  const parentChat = parentChatID === "" ? runChatID(workflowID) : parentChatID;
  // Teach the store the pairing this door already knows. History carries
  // `run.parent_chat_id` for a FINISHED run whose lease is long gone, and the
  // transcript card carries the chat it lives in — so without this the run's own
  // page would have to re-derive the launching chat from a tab that may not be open
  // yet, and every other reader would keep answering "".
  noteRunChat(workflowID, parentChat);
  // The PARENT is a tab id, and a chat id is no longer one — so the nesting
  // question and the id it needs are the same lookup.
  const parentTab = parentChat === "" ? "" : tabIdFor("chat", parentChat);
  void openRunTab(
    workflowID,
    name,
    parentTab === "" ? { owns: false } : { parent: parentTab, owns: false },
  );
}

/** The node a door asked for, pending until a paint has honoured it.
 *
 *  ONE-SHOT, and both halves of that matter. It is CLEARED once a render has
 *  actually carried it, so the reader's own later clicks are never overruled; and
 *  it is left PENDING while the plan does not contain the path, which is the
 *  click-beats-fetch race — the row was clicked before `inspect` described the run,
 *  so the request waits for the reply rather than being lost to it. A path the plan
 *  never contains simply stays pending and the page auto-follows, which is the right
 *  answer for a row whose node the run does not have.
 *
 *  Keyed by run, so a request made for one run cannot be spent on another: the
 *  shared `#run-body` serves every run tab. */
let focusRequest: { workflowID: string; path: string } | undefined;

/** The page, built once and re-pointed. `exec-view/` knows nothing about
 *  workflows: `run-exec-source.ts` folds KAS's reply into its model, so a subagent
 *  tab is a second adapter into this same page rather than a second page. */
let page: ExecPageView | undefined;
let pageRun = "";

/** The live step transcript, LAZILY loaded.
 *
 *  `run-step-blocks.ts` reaches the real tool card, and that module's graph runs
 *  through the editor openers and the navigator into `chat.ts` — the whole
 *  transcript stack. A run tab that statically imported it would pull all of that to
 *  draw a tree, and it is only ever needed for a run being WATCHED: a review of a
 *  finished run has no live content at all, because a step's working output is
 *  streamed and never stored. The first `run_step` frame is the right moment to pay
 *  for it, and frames arriving during the load are queued rather than dropped so the
 *  beginning of a step's output is not the part that goes missing. */
let steps: RunStepStream | undefined;
let stepsLoading = false;
const pendingSteps: RunStepPayload[] = [];

/** The CHAT-route step transcript, lazily loaded for the same reason: it reaches
 *  `messages-blocks.ts`, and through it the whole transcript stack. */
let chatSteps: RunChatStepStream | undefined;
let chatStepsLoading = false;

/** The cached "Open the conversation" anchor, per (run, chat).
 *
 *  Cached because `detail.ts` guards its slot on element IDENTITY: `render` runs on
 *  every store invalidation, and re-seating a node BLURS it, so a fresh element per
 *  render would drop focus out of the link several times a minute on a live run. */
let emptyLink: { workflowID: string; chatID: string; el: HTMLElement } | undefined;

/** Build the page into `#run-body`, replacing whatever was there. */
function mountPage(container: HTMLElement, workflowID: string): ExecPageView {
  page?.dispose();
  // The row belonged to the page being replaced, and its host goes with it.
  dropControlRow();
  const built = buildExecPage({
    emptyNote: stepEmptyNote,
    emptyAction: stepEmptyAction,
    controls: (run) => buildRunControls(run.id),
    onShowNode: armStepRead,
  });
  page = built;
  pageRun = workflowID;
  // Retargeting drops the previous run's stream with the DOM it wrote into. Its
  // content is live-only either way, so there is nothing to carry over — and a
  // stream still holding the old page's hosts would append into detached elements.
  steps = undefined;
  stepsLoading = false;
  pendingSteps.length = 0;
  // Same reason, and one more: a detached render left registered in
  // `messages-blocks.ts`'s map would keep this run's disposers alive under the next
  // run's page.
  chatSteps?.dispose();
  chatSteps = undefined;
  chatStepsLoading = false;
  // The on-demand reads go with the page, which is this cache's whole bound: a run
  // tab retargeting or closing is the one moment a step's answer stops being wanted,
  // and there is no other moment it becomes wrong.
  clearStepTranscripts();
  emptyLink = undefined;
  // Beside the other per-run caches, and for their reason: a retarget must not carry
  // another run's pick into the page it is about to build. A request for the run
  // being retargeted TO survives because the guard only clears one naming ANOTHER
  // run: `openRunView` records it BEFORE it opens the tab, and that open is what
  // triggers this mount.
  if (focusRequest !== undefined && focusRequest.workflowID !== workflowID) {
    focusRequest = undefined;
  }
  container.replaceChildren(built.root);
  return built;
}

/** Ask KAS for the shown step's transcript, when it is worth asking: the node must host
 *  one, it must be SETTLED (a live KAS session cannot be `session/load`ed), and the
 *  chat-route slice must be empty, or this fetches a second copy of rendered content.
 *
 *  `onShowNode`'s `(path, state)` guard is what makes the settled gate a DEFERRAL:
 *  `select()` pins the selection, so a step clicked while running is read once it
 *  settles, and the transient `unavailable` verdict is re-asked no more often. */
function armStepRead(node: ExecNode | undefined): void {
  if (node?.transcript !== true || shownRun === "") {
    return;
  }
  if (!settled(node.state)) {
    return;
  }
  if (chatSliceHasContent(shownRun, node.path)) {
    return;
  }
  requestStepTranscript(shownRun, node.path);
}

/** Whether the CHAT route already holds content for this step.
 *
 *  Recomputed rather than read off the last paint, because `armStepRead` fires on a
 *  selection change and the last paint may predate the chat's window arriving. */
function chatSliceHasContent(workflowID: string, nodePath: string): boolean {
  const chat = launchingChatOf(workflowID);
  if (chat === "") {
    return false;
  }
  const slice = sliceRunSteps(get(chat)?.messages ?? [], workflowID, isThinking(chat)).get(
    nodePath,
  );
  return slice !== undefined && slice.blocks.length > 0;
}

/** Whether this client holds the launching chat's message window at all.
 *
 *  `get` is the UNTRACKED reader (`watchSession` is the tracked one), which is what
 *  keeps this render-time call from adding a subscription to the view effect. */
function chatResident(workflowID: string): boolean {
  return get(launchingChatOf(workflowID)) !== undefined;
}

/** What a node with a transcript host but nothing in it should say. It is called on
 *  EVERY render of a hostable node, host or no host, so it must stay pure and cheap —
 *  what keeps its strings honest is that `detail.ts` HIDES the note element rather
 *  than withholding the call: `render` sets `empty.hidden = anyShown` (the shown
 *  node's host having children), and `bodyFor` hides the note and its action slot on
 *  the first frame. So no string here is ever on screen beside content it contradicts.
 *
 *  THREE questions, in this order, and only the last one is about the transcript:
 *
 *   - did the step RUN at all? `pending` and `skipped` have no session on either
 *     route, so no read can change the answer, and `run-exec-source.ts` marks every
 *     leaf hostable whatever its state — so both reach here as ordinary members of
 *     the tree.
 *   - is it still IN FLIGHT? A busy session cannot be `session/load`ed, so the read
 *     cannot serve a live step; the two answers differ on whether the OTHER route
 *     can, which is the one place the launch route is still visible in this note.
 *   - otherwise the step is SETTLED and every remaining answer is keyed on the READ's
 *     own verdict. That is the structural point of the on-demand read: after it the
 *     only route-dependent thing left on this pane is the DOOR (`stepEmptyAction`),
 *     which needs a launching chat to open.
 *
 *  Item 6's two route sentences are GONE rather than reworded, and both had become
 *  false. "A step's working output is streamed while it runs and is never stored"
 *  described the `run_step` channel, not the step's own KAS session, which the
 *  endpoint reads until the reaper takes it — `gone` is what says that honestly. And
 *  "its transcript is there. Nothing from it is loaded here" claimed a route this
 *  module now reads on demand. */
function stepEmptyNote(node: ExecNode): string {
  // Ahead of everything else, because a step with no execution behind it is the one
  // case here that no read can change. `.ev-d-state` two rows above already reads
  // "not started" or "skipped"; these say what that means for the blank region.
  if (neverRan(node.state)) {
    return node.state === "skipped"
      ? "This step was skipped, so it produced no output."
      : "This step has not started, so there is nothing to show yet.";
  }
  // IN FLIGHT is answered here IN FULL, so the verdict arms below are reached only
  // for a settled step. Two answers, split on whether this client can watch the step
  // at all: a PARENTLESS run's frames arrive live (`run_step`), and a CHAT-PARENTED
  // run's do while the launching chat's window is resident — a live step's blocks sit
  // at that chat's TAIL, the page a window always holds.
  //
  // With neither, nothing is arriving and nothing is being fetched either, because
  // `armStepRead`'s third gate refuses a live step. So that answer states the bound
  // rather than promising output: the read is armed by the repaint that shows the step
  // settled (`onShowNode` fires on a state change), and the DOOR beside this note
  // offers the conversation meanwhile.
  if (inFlight(node.state)) {
    return !shownRunChatParented || chatResident(shownRun)
      ? "Waiting for this step to produce output\u2026"
      : "This step's transcript cannot be read here until it finishes.";
  }
  switch (stepRead(shownRun, node.path)?.state) {
    case "loading":
      return "Loading this step's transcript\u2026";
    case "ready":
      // Reached only with the slice empty AND the read holding no blocks, which is
      // its own fact: the step ran and wrote nothing. Deliberately not "captured
      // nothing" — `capturedOutput` is a different thing this pane already names two
      // regions above, and `skipped` already owns "produced no output".
      return "This step ran without producing a transcript.";
    case "gone":
      return "This step's transcript is no longer stored. What the step CAPTURED is above, when it declared captureOutput.";
    case "unavailable":
      return "This step's transcript could not be read just now.";
    case "unaddressable":
      // The one arm that is not the endpoint's own verdict: the server refused the
      // ADDRESS with a 4xx, so unlike the line above it this must not read as
      // transient — `settled()` never re-asks, and offering a retry that fails
      // identically is the affordance this state exists to remove.
      return "This step's transcript cannot be read: the run's plan does not name it.";
    default:
      // A SETTLED step with no read recorded, which is the instant before its request
      // exists rather than a state the pane sits in: `repaint` fires `onShowNode` —
      // and so `armStepRead` — AFTER `detail.render` has built this note, and again
      // whenever the shown node's state moves, so a read is in flight or one
      // instruction away. The chat route reaches the same instant from the other
      // side, while a step's blocks are inside the lazy `run-chat-steps.js` import,
      // and `bodyFor` retires the note when they land.
      return "Loading this step's transcript\u2026";
  }
}

/** The affordance beside that note: a door into the launching conversation.
 *
 *  Rendered ONLY for a chat-parented run whose launching chat is known. It is still
 *  honest beside the `gone` and `unavailable` notes — the chat may hold those blocks
 *  on another device or after a load — and beside the in-flight one it is the remedy
 *  by a route worth stating, since the transcript DRAWS no step content: opening the
 *  chat loads its message window, and the slice this page reads is over that window.
 *  Its own reveal target is `revealRunCard`, whose step rows lead straight back here.
 *  `detail.ts` hides it whenever content is on screen.
 *
 *  Withheld for a step that NEVER RAN, on the note's own premise: this link's subject
 *  is a transcript sitting in the launching chat, and a step with no execution behind
 *  it has none there to reach — so the pane would say the step produced nothing
 *  beside a door to its output.
 *
 *  Built the way `fundamentals/run-card.ts` builds `.run-open`: a real anchor, so
 *  middle-click and copy-link work, with a click handler that lets the app's own
 *  routing own a plain click and steps aside for a modified one.
 *
 *  NO `#turn-{n}` permalink, deliberately: `Turn.n` is an ordinal within the
 *  paginated window rather than within the session, so a computed anchor names the
 *  wrong turn on any chat long enough to page. */
function stepEmptyAction(node: ExecNode): HTMLElement | null {
  if (node.transcript !== true || !shownRunChatParented || neverRan(node.state)) {
    return null;
  }
  const workflowID = shownRun;
  const chatID = launchingChatOf(workflowID);
  if (chatID === "") {
    return null;
  }
  if (emptyLink?.workflowID === workflowID && emptyLink.chatID === chatID) {
    return emptyLink.el;
  }
  const link = el(
    "a",
    { className: "ev-d-link", href: buildPath({ kind: "chat", id: chatID }) },
    "Open the conversation",
    el("span", { className: "ev-d-link-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  );
  link.addEventListener("click", (e) => {
    // A modified click is a deliberate escape from the app's own routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
      return;
    }
    e.preventDefault();
    void openTab({
      kind: "chat",
      ref: chatID,
      name: get(chatID)?.name ?? "Chat",
    }).then(() => {
      // The reveal is the REFINEMENT and the open is the affordance, which is why it
      // is best-effort and lazily imported: `messages.ts` is the transcript stack,
      // and a run tab must not pull it in to draw a link.
      void import("./messages.js")
        .then((mm) => mm.revealRunCard(chatID, workflowID))
        .catch(() => {
          /* the tab is open, which is what the link promised */
        });
    });
  });
  emptyLink = { workflowID, chatID, el: link };
  return link;
}

/** Whether an open run tab projects `chatID`'s transcript — the eviction sweep's
 *  third exemption, registered by app.ts through `registerEvictionExemption`
 *  (store.ts is a leaf and must not import tabs.ts or run-store.ts, so the
 *  composition root wires it).
 *
 *  It exists because `hasExecutingRunForChat` exempts a chat with a run that is
 *  still EXECUTING only, so opening the sub-tab of a FINISHED — or merely PARKED —
 *  chat-parented run and then working elsewhere would let the sweep take the chat's
 *  messages out from under the slice — state 1 would silently degrade to state 3
 *  while the reader watched.
 *
 *  Answered from the RESIDENT blocks, which is `subagentTabProjectsChat`'s shape and
 *  its reasoning: the run ids reachable from this chat are the ones stamped on its
 *  blocks, and a run tab whose steps are NOT resident does not hold the window — its
 *  pane already renders the nothing-is-loaded note, so eviction changes nothing it
 *  was showing. */
export function runTabProjectsChat(chatID: string): boolean {
  const msgs = get(chatID)?.messages ?? [];
  for (const m of msgs) {
    for (const b of m.blocks ?? []) {
      const step = parseStepSubtask(b.agent_subtask_id ?? "");
      if (step !== null && hasTab("run", step.workflowID)) {
        return true;
      }
    }
  }
  return false;
}

/** Apply one `run_step` frame, if it is about the run on screen.
 *
 *  A frame for another run is DROPPED rather than buffered. The event is
 *  workspace-global (a parentless run has no chat to address), so every client sees
 *  every run's steps, and holding the ones for runs this tab is not showing would be
 *  an unbounded buffer for content that is discarded on reload anyway. The cost is
 *  that switching to a run mid-flight starts its transcript from that moment, which
 *  is the same thing that happens when the tab is opened late. */
export function applyRunStep(payload: RunStepPayload): void {
  if (payload.workflow_id !== pageRun || page === undefined) {
    return;
  }
  if (steps !== undefined) {
    steps.apply(payload);
    return;
  }
  pendingSteps.push(payload);
  if (stepsLoading) {
    return;
  }
  stepsLoading = true;
  const forRun = pageRun;
  void import("./run-step-blocks.js")
    .then(({ createRunStepStream }) => {
      // The tab may have retargeted during the load. Anything queued belonged to the
      // run that is gone, so it is discarded with it rather than replayed into
      // another run's rows.
      if (pageRun !== forRun || page === undefined) {
        pendingSteps.length = 0;
        return;
      }
      const view = page;
      const stream = createRunStepStream((nodePath) => view.bodyFor(nodePath));
      steps = stream;
      for (const queued of pendingSteps) {
        stream.apply(queued);
      }
      pendingSteps.length = 0;
    })
    .catch(() => {
      // The step transcript is an enhancement over a page that is already rendering
      // the plan, the timings and the outputs, so a failed chunk load leaves a
      // usable tab. Cleared so the next frame retries.
      pendingSteps.length = 0;
    })
    .finally(() => {
      stepsLoading = false;
    });
}

/** Paint the view from a store value. `undefined` means the first fetch has not
 *  resolved, which is the ONLY case that shows a loading row: a refetch driven by an
 *  invalidation must not blank a run the reader is looking at, several times a
 *  minute on a busy one. */
function paint(workflowID: string, state: RunState | undefined): void {
  const container = document.getElementById("run-body");
  if (container === null) {
    return;
  }
  if (state === undefined) {
    if (container.childElementCount === 0) {
      container.replaceChildren(el("div", { className: "list-empty" }, "Loading run\u2026"));
    }
    return;
  }
  // Nudge the tab dot. A run launched from the Workflows tab is painted by the
  // `run_started` frame that follows, but a tab restored on boot or opened from
  // History lands on a run already going, and a PAUSED run emits no frames at all —
  // so without this its dot would sit blank for as long as the pause lasts. Both
  // calls are needed: `trackRun` admits a run this client has seen no event for, and
  // `refreshRunDots` repaints one it already knows.
  trackRun(workflowID);
  refreshRunDots();

  // Chat-parentedness comes from the RUN, not from client memory or from the door.
  // `parentSessionId` is `inspect`'s own answer and is empty for a manual and a
  // scheduled launch alike; the second term can only ADD chat-parented verdicts, and
  // every one it adds offers a DOOR into the conversation rather than withholding
  // anything, which is the safe direction. No verb hangs off it: the control row is
  // the server's answer.
  const launchingChat = launchingChatOf(workflowID);
  shownRunChatParented = (state.parentSessionId ?? "") !== "" || launchingChat !== "";

  // Reused only while the page this module built is STILL MOUNTED in this container.
  // The run id alone is not enough: `#run-body` is one shared element whose children
  // any caller may have replaced, and a page cached against a detached container
  // would leave the live one blank while every render went to DOM nobody can see.
  const mounted = pageRun === workflowID && page?.root.parentElement === container;
  const view = mounted && page !== undefined ? page : mountPage(container, workflowID);
  // Two inputs on different clocks, the same pair the transcript's card takes:
  // `inspect` says what the nodes are doing, and the dock says which of them is
  // blocked on a person. The run's own status cannot carry the second — KAS blocks
  // the asking step's turn and leaves the run `running` — and both reads are
  // signal-backed, so the one effect this module installs repaints on either.
  const focus = focusRequest?.workflowID === workflowID ? focusRequest.path : "";
  const run = runToExec(workflowID, state, runPlan(workflowID), runPendingAsks(workflowID), focus);
  view.render(run);
  // Spent only once the page could actually honour it — the plan has to CONTAIN the
  // path, since `page.ts` ignores a focus naming an absent node. Clearing on the
  // render alone would drop a pick made before `inspect` arrived; leaving it forever
  // would re-assert it on every invalidation and fight the reader's next click.
  if (focus !== "" && flatten(run.nodes).some((n) => n.path === focus)) {
    focusRequest = undefined;
  }
  projectStepTranscripts(workflowID, launchingChat, run.nodes);
}

/** Project this run's step transcripts into the detail pane, from BOTH routes.
 *
 *  Per path, the chat slice wins when it has content and the on-demand read fills in
 *  otherwise. That order is the whole preference rule: the chat's blocks are already
 *  in this client's store and already stream, while the read is a settled answer, so
 *  preferring the read would replace live content with a snapshot.
 *
 *  It still performs NO chat fetch. `store-load.ts` `loadMessages` would make the
 *  chat route reachable more often and is declined for its own reason — a run tab
 *  fetching a chat's window makes a SECOND owner of that window (paging,
 *  `liveTurnMessage` bookkeeping, the eviction sweep) — and it never closed the
 *  paged-out case anyway. The on-demand read is what closes it, and it addresses the
 *  STEP rather than the conversation. */
function projectStepTranscripts(
  workflowID: string,
  launchingChat: string,
  nodes: readonly ExecNode[],
): void {
  const slices =
    shownRunChatParented && launchingChat !== ""
      ? sliceRunSteps(get(launchingChat)?.messages ?? [], workflowID, isThinking(launchingChat))
      : new Map<string, RunStepSlice>();
  subscribeToDeltas(slices);
  const paint = leafPaints(workflowID, launchingChat, nodes, slices);
  if (paint.size === 0) {
    return;
  }
  if (chatSteps !== undefined) {
    chatSteps.apply(paint);
    return;
  }
  if (chatStepsLoading) {
    return;
  }
  chatStepsLoading = true;
  const forRun = pageRun;
  void import("./run-chat-steps.js")
    .then(({ createRunChatStepStream }) => {
      // The tab may have retargeted during the load, in which case this stream would
      // write into the previous page's detached hosts. `page` rather than the
      // captured view for the same reason: the current page is the mounted one.
      if (pageRun !== forRun || page === undefined) {
        return;
      }
      const host = page;
      const stream = createRunChatStepStream((nodePath) => host.bodyFor(nodePath), workflowID);
      chatSteps = stream;
      // RE-PROJECTED rather than replayed: the chunk load is async, so what either
      // route holds now may have grown past what was captured before it, and applying
      // the stale map would leave the tail to the next repaint.
      const fresh =
        shownRunChatParented && launchingChat !== ""
          ? sliceRunSteps(get(launchingChat)?.messages ?? [], workflowID, isThinking(launchingChat))
          : new Map<string, RunStepSlice>();
      stream.apply(leafPaints(workflowID, launchingChat, nodes, fresh));
    })
    .catch(() => {
      // The step transcript is an enhancement over a page already rendering the plan,
      // the timings and the outputs, so a failed chunk load leaves a usable tab.
    })
    .finally(() => {
      chatStepsLoading = false;
    });
}

/** What to paint for every LEAF of this plan that has content on either route.
 *
 *  A path naming no node is dropped so no orphan host is minted, and a CONTAINER is
 *  out by construction (it carries `transcript !== true` and hosts nothing). Every
 *  leaf WITH content is painted rather than only the selected one, because the hosts
 *  persist for the pane's life and a chat-route transcript cannot be replayed — the
 *  same reason `detail.ts` keeps them.
 *
 *  The CHAT slice wins whenever it has blocks: it is live and already in the store,
 *  where the on-demand read is a settled snapshot. A chat slice that is present but
 *  EMPTY loses, which is the case the read exists for — a window that has paged the
 *  run's turn out projects an entry with nothing in it. */
function leafPaints(
  workflowID: string,
  launchingChat: string,
  nodes: readonly ExecNode[],
  slices: ReadonlyMap<string, RunStepSlice>,
): Map<string, RunStepPaint> {
  const out = new Map<string, RunStepPaint>();
  for (const node of leaves(nodes)) {
    const chat = slices.get(node.path);
    if (chat !== undefined && chat.blocks.length > 0) {
      out.set(node.path, { slice: chat, source: { kind: "chat", chatID: launchingChat } });
      continue;
    }
    const read = stepSliceFor(workflowID, node.path);
    if (read !== undefined) {
      out.set(node.path, { slice: read, source: { kind: "kas" } });
    }
  }
  return out;
}

/** Subscribe the view effect to the steps' own streaming blocks.
 *
 *  A SAME-TICK trigger, not the only one: `appendChunk` writes the block signal
 *  (effects flush synchronously) and schedules a version bump for the same delta,
 *  which `installViewEffect` reads a microtask later.
 *
 *  `get` rather than `ensure`: `appendChunk`'s full-repaint fallback fires only
 *  while no signal exists, so minting one freezes the TRANSCRIPT's own bubble. */
function subscribeToDeltas(slices: ReadonlyMap<string, RunStepSlice>): void {
  for (const slice of slices.values()) {
    for (const key of slice.sourceKeys) {
      touch(blockTextSigs.get(key), blockThinkingSigs.get(key));
    }
  }
}

/** The run's control row, rendered from the SERVER's answer and rebuilt only when
 *  that answer moved (`controlRow` owns why).
 *
 *  Nothing is decided here. The exec view's own state word is not even consulted:
 *  the affordance is computed against a status the server read one round trip ago,
 *  and re-deriving it from the state this page happens to hold would put the
 *  drifting copy back — which is what the status-keyed table plus a parentlessness
 *  gate used to be. Parentlessness gates no verb here either: `hostBridge`
 *  (run_host.go) resolves an agent-parented run's carrier from the launching chat's
 *  live bridge, so which verbs that run offers is the server's answer too.
 *
 *  Three outcomes. Verbs render as buttons. No verbs but a REFUSAL renders the
 *  server's sentences where the buttons would have been — before this the row
 *  returned null and a reader was never told why a run offered nothing. Neither
 *  renders nothing, the honest answer for a completed run (its state word says it)
 *  and for the moment before the first fetch resolves. */
function buildRunControls(workflowID: string): HTMLElement | null {
  const answer = runControls(workflowID);
  const sig = answer === undefined ? "" : controlSignature(workflowID, answer);
  if (sig === controlSig) {
    return controlRow;
  }
  // The answer moved, so the row on screen is on its way out and its bindings go
  // with it — the caller replaces the host's children with whatever is returned here.
  dropControlRow();
  controlSig = sig;
  controlRow = answer === undefined ? null : renderControls(workflowID, answer);
  return controlRow;
}

/** What the row is a function of, and nothing else: the run it acts on, the verbs it
 *  draws and the sentences it draws instead. The parent chat travels on the same
 *  answer and changes nothing here, so it is left out rather than churning the row. */
function controlSignature(workflowID: string, answer: RunControlsResponse): string {
  // Keyed, not positional: `refused` is a map on the wire, so two answers that differ
  // only in the order the server happened to serialize them are the same row.
  const refused = Object.entries(answer.refused ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([verb, text]) => `${verb}\u0001${text}`)
    .join("\u0002");
  return `${workflowID}\u0000${answer.verbs.join("\u0001")}\u0000${refused}`;
}

/** The row itself. Split from the decision above so the guard reads as one thing. */
function renderControls(workflowID: string, answer: RunControlsResponse): HTMLElement | null {
  const verbs = offeredVerbs(answer.verbs);
  if (verbs.length === 0) {
    return refusalRow(refusalSentences(answer.refused));
  }
  const row = el("div", { className: "run-controls" });
  for (const verb of verbs) {
    const action = RUN_ACTION[verb];
    const btn = el(
      "button",
      {
        type: "button",
        className: verb === "cancel" ? "btn btn-sm btn-danger" : "btn btn-sm",
        onclick: () => {
          // No optimistic state flip. Every one of these verbs settles at a NODE
          // boundary, so the run is still `running` when the reply arrives and a
          // flipped label would be a lie for as long as the node takes. The
          // run_progress invalidation is what repaints this row.
          void action.dispatch(workflowID);
        },
      },
      CONTROL_LABEL[verb],
      // `el` answers HTMLElement; the pending binding needs the `disabled`
      // property, which only the concrete button type declares.
    ) as HTMLButtonElement;
    // A retry starts a process and can legitimately take tens of seconds, so an
    // unbound button looks dead for the whole handshake and can be clicked again
    // meanwhile. Its disposer is held rather than left to the binding's own
    // detach detection, which cannot arm on a button bound before it is attached.
    controlBindings.push(bindLoadingState(action.name, btn));
    row.appendChild(btn);
  }
  return row;
}

/** The row a run with no verbs gets: the server's own sentences, in the place a
 *  reader is already looking for the control. Null when there is nothing to say,
 *  so a completed run keeps its clean header. */
function refusalRow(sentences: readonly string[]): HTMLElement | null {
  if (sentences.length === 0) {
    return null;
  }
  return el(
    "div",
    { className: "run-controls run-controls-refused", role: "note" },
    ...sentences.map((text) => el("p", { className: "run-control-refusal" }, text)),
  );
}
