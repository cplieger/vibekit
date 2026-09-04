// ---------------------------------------------------------------------------
// One workflow run: the WORKFLOW DOOR into the shared subpage view.
//
// This module is wiring and nothing else: it folds KAS's `inspect` reply through
// `run-exec-source.ts` into `exec-view/`'s model, hands that to `buildExecPage`, and
// owns the three things only a workflow knows — which verbs the status offers, what
// an empty step body means, and where a `run_step` frame renders. It draws no run UI
// of its own.
//
// Every door opens the same page: the Workflows tab's Run button, a History row, the
// transcript card's footer link, a `/run/{id}` deep link, and the automatic sub-tab a
// starting run offers its launching chat. There is no per-door flavour left — the
// verbs are gated on the RUN (parentless or agent-parented, below) rather than on
// which door was used, and no door's × stops anything.
//
// There is no composer, because there is nobody to type to.
//
// THE VIEW SERVES THREE SUBJECTS and the code below is only one of the three doors
// into it (user decision, 2026-08):
//
//   a PARENTLESS workflow    launched from the Workflows tab or by the scheduler.
//                            vibekit hosts its bridge, so the page carries the
//                            status's verbs and its steps stream in through
//                            `run_step`.
//   a CHAT-TRIGGERED workflow  launched by an agent calling `run_workflow`. The
//                            agent drives it on a bridge it holds, so the page
//                            carries NO verbs and its steps stream into that
//                            chat's transcript instead — which is what the detail
//                            pane's note says rather than sitting blank.
//   a SUBAGENT expansion     the same page over a delegate, reached when a reader
//                            wants one out of the transcript's keyhole. Its own
//                            adapter; nothing under `exec-view/` learns it exists.
//
// CLOSING NEVER STOPS THE WORK, for any of the three. `owns: false` at every door,
// no `onClose` in the factory. A × that means "close this" on one door and "destroy
// the work" on another is a gesture a reader cannot learn, and it was the only
// destructive control in the app with no confirmation, on the smallest target in the
// row. Stopping a run is the Cancel VERB. The launching chat's × still cancels its
// runs, and that is a different gesture: it destroys the conversation.
//
// WHAT IT RENDERS IS `exec-view/`, the shared subpage view, through an adapter. This
// page used to hand-roll its own node tree and output list, and that was a second,
// poorer rendering of one state: it put 7 of the ~30 fields `inspect` carries on
// screen, so a failed run showed a red dot and no reason while the card in a chat
// named the failing step and quoted it, a running step showed no elapsed time, and a
// captured report written in markdown showed its own asterisks in a `<pre>`.
// `run_http.go` refuses to keep a second representation of KAS's state on the server
// for exactly this reason; the client had one anyway.
//
// It does NOT render the transcript's run card. A card among a conversation's turns is
// a glance and a page of its own is a review, so the card's short-lived `full` detail
// mode is deleted: a flag that made one component mean two things was the wrong seam.
// What the two share is `exec-view/status.ts` — one status vocabulary — and nothing
// else.
//
// It also HOSTS THE LIVE STEP TRANSCRIPT, which is new. A parentless run's step
// frames used to be dropped on the run bridge, so this tab could show what a step
// produced and never how it got there; they now arrive as `run_step` events and
// render into the card's step rows (run-step-blocks.ts). That content is live-only
// — it belongs to a turn vibekit never prompted and so never finalizes — which is
// why an empty step body on a FINISHED run says so rather than sitting blank.
// ---------------------------------------------------------------------------

import { el, effect } from "@cplieger/reactive";
import { bindLoadingState } from "./actions/index.js";
import { closeTab, getActiveTabId, openRunTab, tabIdFor } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks, runPendingAsks } from "./decision-dock.js";
import { cancelRun, pauseRun, resumeRun, retryRun } from "./actions/runs.js";
import { CONTROL_LABEL, offeredVerbs, refusalSentences, type RunVerb } from "./run-controls.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight } from "./exec-view/status.js";
import type { ExecNode } from "./exec-view/model.js";
import { runToExec } from "./run-exec-source.js";
import type { RunStepStream } from "./run-step-blocks.js";
import {
  invalidateRun,
  invalidateRunControls,
  runControls,
  runState,
  runChatID,
  runPlan,
  type RunState,
} from "./run-store.js";
import type { RunControlsResponse } from "./wire/types.gen.js";
import { refreshRunDots, trackRun } from "./run-dots.js";
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
 *  ONE argument now. It used to take `parentless`, and the composition root
 *  answered it from the run store's record of which chat launched the run — a map
 *  written only by SSE frames, so every client that had reloaded answered `true`
 *  for a chat-parented run and got the parentless control row. Parentage is a
 *  durable property of the run and the server resolves it from the chat store, so
 *  it arrives with the affordance instead of being guessed here. */
export function showRun(workflowID: string): void {
  shownRun = workflowID;
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
 *  stop it, so `buildRunControls` gates on the RUN — parentless or not — and a
 *  parentless run offers its status's verbs through every door. `GET /api/sessions`
 *  does not filter by status, so History genuinely does list running and paused runs.
 *
 *  PARENTLESSNESS is not an argument here either. `showRun`'s second parameter asks
 *  whether the RUN has a parent agent session, which is the run's own fact, and the
 *  composition root answers it from the run store when it wires the factory's
 *  opener — so a History row and a restored tab agree instead of each door deciding.
 *
 *  `parentChatID` nests it under the chat that launched it when that chat has a
 *  tab open, which is what makes a run reached from History land beside its own
 *  conversation instead of at the end of the strip. History already holds the fact
 *  (`run.parent_chat_id`); nothing new is fetched for it. When the caller does not
 *  know it — the run card's "Open run" link, a `/run/{id}` deep link — the store's
 *  own record of which chat launched the run answers instead.
 *
 *  NO offer guard here, unlike `openRunSubTab`. This is the MANUAL path — the
 *  Workflows tab's Run button, the run card's footer link, a `/run/{id}` deep link —
 *  and a reader who closed the automatic tab and then asked for the run is asking for
 *  it back; refusing them would be the opposite of respecting the close. A parentless
 *  run, or one whose chat is not open, stays top-level: `insertRow` would fall back to
 *  top-level for an absent parent anyway, and asking first keeps the intent explicit.
 *
 *  It absorbed `openLiveRunView`, which differed only in `owns: true`. With the ×
 *  disarmed there was nothing left to distinguish, and a launcher-opened run is
 *  parentless so it lands top-level through this door too. */
export function openRunView(workflowID: string, name: string, parentChatID = ""): void {
  // From here on this tab is the READER's, so the completion auto-close leaves it
  // alone. Same reasoning as the offer guard, pointing the other way: a tab
  // someone asked for must not be taken away on a schedule of the app's choosing.
  autoOpened.delete(workflowID);
  const parentChat = parentChatID === "" ? runChatID(workflowID) : parentChatID;
  // The PARENT is a tab id, and a chat id is no longer one — so the nesting
  // question and the id it needs are the same lookup.
  const parentTab = parentChat === "" ? "" : tabIdFor("chat", parentChat);
  void openRunTab(
    workflowID,
    name,
    parentTab === "" ? { owns: false } : { parent: parentTab, owns: false },
  );
}

/** Open a run's tab PROACTIVELY, as a sub-tab of the chat whose agent launched it,
 *  without stealing focus.
 *
 *  This is the surface a run needs and did not have. A run is initiated in a turn
 *  and then outlives it: `run_workflow` returns as soon as the run is created, so
 *  the launching turn ends and its card scrolls away while the run carries on for
 *  minutes. The transcript keeps the RECORD, the tab carries the DETAIL, and the
 *  strip carries the LIVENESS through this tab's dot — which is what shows a run
 *  still executing while its chat's own dot has already gone green.
 *
 *  Four properties, each deliberate:
 *
 *   - NOT ACTIVATED. The strip is the reader's; a run the agent started on its own
 *     may appear there but must not take the screen. The same `{ activate: false }`
 *     the boot loop uses.
 *   - `owns: false`. Closing it REMOVES A VIEW and stops nothing: not the run, and
 *     not this client's observation of it, because the run's card in the launching
 *     chat's transcript is a second live view of the same store cell. The chat's own
 *     × is what cancels, via `close_chat` server-side (user decision).
 *   - OFFERED ONCE PER CLIENT, and this is the load-bearing one. `openTab` dedupes
 *     by id, which is not the same thing: a run emits a `run_progress` per node
 *     event, so without this guard a reader who closed the tab would watch it
 *     reappear within seconds, forever, and the app would be arguing with them. The
 *     guard is what lets a progress frame still open a tab for a client that joined
 *     MID-RUN and never saw `run_started` — first offer opens, a close is final.
 *   - RE-OPENABLE on request. A close is final only for the automatic offer; the
 *     card's "Open run" link and a `/run/{id}` deep link both go through
 *     `openRunView`, which has no guard and nests under the same parent. That path
 *     also FOCUSES a tab that is already open, because `openTab` activates an
 *     existing id unless told not to — so one link serves both "show me this" and
 *     "bring it back".
 */
const offered = new Set<string>();

/** The runs whose sub-tab this client opened BY ITSELF, and therefore the only
 *  tabs it may close by itself.
 *
 *  Not the same set as `offered`, though they start identical, and the difference
 *  is the whole reason there are two. `offered` records that the automatic path
 *  has spent its one offer, and it must never be un-recorded or the tab would
 *  reappear on the next progress frame. Membership HERE says the tab on screen is
 *  the app's own doing rather than something the reader asked for — so a reader
 *  who re-opens the run through `openRunView` takes it out, and from then on the
 *  tab is theirs to close. Closing a tab a reader deliberately opened would be
 *  the same argument-with-the-reader the offer guard exists to avoid, in the
 *  other direction. */
const autoOpened = new Set<string>();

/** The endings an automatic sub-tab closes itself on.
 *
 *  Not every terminal status, and the exclusions come from this app's own rule
 *  for automatic hiding rather than from taste: `tool-group.ts` never auto-
 *  collapses a group holding a failure, and re-opens one that fails while already
 *  collapsed, because a failure is not noise. A failed or aborted run is the one
 *  a reader wants the detail of, so its tab stays. `paused` is not an ending at
 *  all — KAS reports an onMaxIterations policy stop through the same frame, and
 *  the run is still this process's to resume. An unrecognised status stays too:
 *  a tab is cheap and a closed one the reader wanted is not.
 *
 *  `cancelled` is in, because the reader asked for the stop and a run they
 *  cancelled has nothing they were waiting to read. */
const CLEAN_ENDINGS: ReadonlySet<string> = new Set(["completed", "cancelled"]);

export function openRunSubTab(workflowID: string, name: string, parentChatID: string): void {
  if (workflowID === "" || parentChatID === "" || offered.has(workflowID)) {
    return;
  }
  const parentTab = tabIdFor("chat", parentChatID);
  if (parentTab === "") {
    // The launching chat has no tab here (a background chat on another device's
    // arrangement). Not marked offered: if that chat opens later, the run still
    // deserves its sub-tab.
    return;
  }
  offered.add(workflowID);
  autoOpened.add(workflowID);
  void openRunTab(workflowID, name, { parent: parentTab, owns: false, activate: false });
}

/** Close a run's automatic sub-tab now that the run has ended cleanly.
 *
 *  The counterpart of the offer above, and it is deliberately narrower than "close
 *  the tab of a finished run". Three gates, each closing a way this could take a
 *  tab someone still wanted:
 *
 *   - AUTOMATIC ONLY. Only a tab this client opened by itself is closed. A run
 *     opened from History or from its card's "Open run" link was asked for; a
 *     launcher-owned tab is worse still, since its × means cancel. A TANGENT is
 *     out by construction rather than by a filter: nothing but the run door above
 *     ever puts an id in this set, so a forked chat's sub-tab is unreachable from
 *     here.
 *   - CLEAN ENDINGS ONLY. See `CLEAN_ENDINGS`.
 *   - NEVER THE TAB ON SCREEN. Closing the active tab would take the view out
 *     from under a reader who is watching the run finish, which is the moment its
 *     output becomes worth reading. They can close it themselves; the point of
 *     this is the tabs nobody is looking at.
 *
 *  Every connected client runs this independently and `closeTab` tolerates an id
 *  that is already gone, so the races between them are not a case to handle. */
export function autoCloseRunSubTab(workflowID: string, status: string): void {
  if (!autoOpened.has(workflowID) || !CLEAN_ENDINGS.has(status)) {
    return;
  }
  const tabID = tabIdFor("run", workflowID);
  if (tabID === "") {
    // The reader already closed it. Drop the claim so nothing here holds a run
    // whose tab is gone.
    autoOpened.delete(workflowID);
    return;
  }
  if (tabID === getActiveTabId()) {
    return;
  }
  autoOpened.delete(workflowID);
  void closeTab(tabID);
}

/* `openLiveRunView` is DELETED, and it left no behaviour behind. Its whole
   difference from `openRunView` was `owns: true` — the × cancels — and with a run tab
   always a VIEW there is nothing left to distinguish: it nested nowhere the other
   does not (a launcher-opened run is parentless, so `runChatID` answers "" and
   `openRunView` goes top-level anyway). Two doors that behave identically are two
   things to keep in step, so the Workflows tab's Run button opens `openRunView` like
   every other door. */

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

/** Build the page into `#run-body`, replacing whatever was there. */
function mountPage(container: HTMLElement, workflowID: string): ExecPageView {
  page?.dispose();
  // The row belonged to the page being replaced, and its host goes with it.
  dropControlRow();
  const built = buildExecPage({
    emptyNote: stepEmptyNote,
    controls: (run) => buildRunControls(run.id),
  });
  page = built;
  pageRun = workflowID;
  // Retargeting drops the previous run's stream with the DOM it wrote into. Its
  // content is live-only either way, so there is nothing to carry over — and a
  // stream still holding the old page's hosts would append into detached elements.
  steps = undefined;
  stepsLoading = false;
  pendingSteps.length = 0;
  container.replaceChildren(built.root);
  return built;
}

/** What a node with a transcript host but nothing in it should say, and there are
 *  three answers because they are three different facts a blank region states none
 *  of:
 *
 *   - a CHAT-PARENTED run's steps never stream here at all. Their frames arrive on
 *     the launching chat's connection and render in that chat's own run card, so
 *     this tab holds the plan, the timings and the results while the working
 *     transcript is one tab away. "Waiting" would be a lie that never resolves.
 *   - a FINISHED parentless run has no transcript to show anyone: the content was
 *     live-only, so opening the run afterwards has none of it.
 *   - a LIVE parentless step simply has not spoken yet.
 *
 *  Parentage comes from the SERVER's affordance answer, the same read the control
 *  row uses. It was a local flag fed by an event cache that is empty after a
 *  reload, which sent every reloaded reader of a chat-parented run down the
 *  parentless arm — so the note promised a transcript that could never arrive
 *  here. Before the answer lands the run is treated as parentless, which is the
 *  arm whose sentences describe THIS tab and so cannot mislead about another one. */
function stepEmptyNote(node: ExecNode): string {
  if ((runControls(shownRun)?.parent_chat_id ?? "") !== "") {
    return "Launched by an agent, so this step's working output streams into that chat's transcript rather than here. This tab holds the plan, the timings and the captured output.";
  }
  if (inFlight(node.state)) {
    return "Waiting for this step to produce output\u2026";
  }
  return "No live transcript. A step's working output is streamed while it runs and is never stored, so it is unavailable once the step has finished; anything it captured is above.";
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
  view.render(runToExec(workflowID, state, runPlan(workflowID), runPendingAsks(workflowID)));
}

/** The run's control row, rendered from the SERVER's answer and rebuilt only when
 *  that answer moved (`controlRow` owns why).
 *
 *  Nothing is decided here. The exec view's own state word is not even consulted:
 *  the affordance is computed against a status the server read one round trip ago,
 *  and re-deriving it from the state this page happens to hold would put the
 *  drifting copy back.
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
