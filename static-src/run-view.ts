// ---------------------------------------------------------------------------
// One workflow run: its node tree, its captured outputs, and its controls.
//
// Opened from the History list (a review) or from the Workflows tab (a live,
// launcher-owned tab whose close cancels the run).
//
// Two flavours, one difference that matters: an OWNED tab (the Workflows tab's
// Run button) carries controls and its close cancels the run; a REVIEW (a History
// row) carries neither. Pause and Resume are KAS's own verbs, Cancel is on both
// live statuses, and a finished run offers none in either flavour.
//
// The flavour is the opener's, not the status's, and that is the whole design.
// GET /api/sessions does not filter by status, so History genuinely lists running
// and paused runs — but History is where finished work is READ. A tab that can
// pause or cancel is a live surface, and the live surface is the Workflows tab.
// An earlier revision let History's tabs carry controls because a live run there
// was otherwise a dead end; the dead end is the honest answer, and the run is
// reachable through the door that launched it.
//
// So there is no composer, no input bar, and for a review no controls either,
// because there is nobody to type to and nothing here owns the work.
//
// The tree is rendered from KAS's own `state` shape, passed through verbatim by
// GET /api/runs/{id}. vibekit deliberately does not re-model it: the
// node plan is KAS's structure, and a second representation of it here would be
// one more thing to keep in sync for no gain.
// ---------------------------------------------------------------------------

import { el, effect } from "@cplieger/reactive";
import { closeTab, getActiveTabId, openRunTab, tabIdFor } from "./tabs.js";
import { mountRunDecisionDock, rerenderDocks } from "./decision-dock.js";
import { cancelRun, pauseRun, resumeRun, retryRun } from "./actions/runs.js";
import { RUN_CONTROLS, CONTROL_LABEL, type RunVerb } from "./run-controls.js";
import { invalidateRun, runState, runChatID, type RunNode, type RunState } from "./run-store.js";
import { refreshRunDots, trackRun } from "./run-dots.js";

/** Verb → its action. Separate from run-controls.ts's table on purpose: that
 *  module is the pure RULE and must stay importable without the actions
 *  framework; this is the wiring. */
const RUN_ACTION: Record<RunVerb, { dispatch: (id: string) => Promise<unknown> }> = {
  pause: pauseRun,
  resume: resumeRun,
  cancel: cancelRun,
  retry: retryRun,
};

/** The run this view is currently showing, so an SSE invalidation knows whether
 *  it is about the run on screen. Cleared when the view loads a different one;
 *  a closed tab simply stops matching, because the next open reassigns it. */
let shownRun = "";

/** Whether the run on screen was opened as a live, owned tab.
 *
 *  Controls render only for those. A run reached from History is a REVIEW even
 *  when the run is still moving: History is where finished work is read, and a
 *  tab you can act on belongs to the surface that launched the run. Set by the
 *  opener rather than derived from status, because the same `running` run is
 *  actionable through one door and a record through the other. */
let shownRunOwned = false;
/** Whether the shown run was launched manually (no parent chat). Only a
 *  parentless run offers Retry; a parented one is the agent's to recover. */
let shownRunParentless = false;

/** Point the shared run view (one DOM element serves every run tab) at a run,
 *  and give its dock somewhere to render. The dock host is mounted ONCE with a
 *  dynamic match — the run on screen — so tab switches re-key it without
 *  re-mounting. */
/** Point the run view at one run and mount its dock.
 *
 *  Exported for the tab factory (tab-materialize.ts): this is a run tab's
 *  `onShow`, and the factory has to name it without importing the three openers
 *  above, which build tabs. `parentless` is deliberately NOT derivable from a
 *  `TabSubject` — see this module's callers and the factory's header: it asks
 *  whether the RUN has a parent agent session, while a subject's `Parent` names
 *  the open tab this one nests under, and a chat-parented run reviewed while its
 *  chat's tab is closed has an empty Parent without being parentless. */
export function showRun(workflowID: string, owned: boolean, parentless: boolean): void {
  shownRun = workflowID;
  shownRunOwned = owned;
  shownRunParentless = parentless;
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

/** Open a run REVIEW: read-only, closing the tab closes nothing. The History
 *  page's row opens these.
 *
 *  Read-only even for a run that is still going. GET /api/sessions does not
 *  filter by status, so History does list running and paused runs, and an earlier
 *  revision let those carry controls on the reasoning that a live run in History
 *  was otherwise a dead end. That is the wrong trade: History is where finished
 *  work is read, and a tab that can pause or cancel is a live surface wearing a
 *  record's clothes. The live door is the Workflows tab.
 *
 *  PARENTLESSNESS is no longer an argument here. `showRun`'s third parameter asks
 *  whether the RUN has a parent agent session, which is the run's own fact, and the
 *  composition root answers it from the run store when it wires the factory's
 *  opener — so a review and a restored tab agree instead of each door deciding.
 *
 *  `parentChatID` nests it under the chat that launched it when that chat has a
 *  tab open, which is what makes a run reached from History land beside its own
 *  conversation instead of at the end of the strip. History already holds the fact
 *  (`run.parent_chat_id`); nothing new is fetched for it. When the caller does not
 *  know it — the run card's "Open run" link, a `/run/{id}` deep link — the store's
 *  own record of which chat launched the run answers instead.
 *
 *  NO offer guard here, unlike `openRunSubTab`. This is the RE-OPEN path: a reader
 *  who closed the automatic tab and then clicked the run in its transcript is
 *  asking for it back, and refusing them would be the opposite of respecting the
 *  close. A parentless run, or one whose chat is not open, stays top-level —
 *  `insertRow` would fall back to top-level for an absent parent anyway, and
 *  asking first keeps the intent explicit rather than incidental. */
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

/** Open a LAUNCHER-OWNED run tab: closing it CANCELS the run (user decision —
 *  the × means stop; a workflow that outlived its tab would spend credits and
 *  edit files with nothing on screen). The Workflows tab's Run button opens
 *  these. Cancel is fire-and-forget: the terminal run_complete tears the
 *  server-side bridge down, and the run_finished event settles every list. */
export function openLiveRunView(workflowID: string, name: string): void {
  // An owned tab is never auto-closed: its × cancels, so the auto-close must not
  // be able to reach it even if this run somehow carried an automatic tab first.
  autoOpened.delete(workflowID);
  // `owns: true` is the whole difference from the review above, and it is a
  // SUBJECT field now rather than a local spec choice — which is what lets the two
  // coexist for one `(kind, ref)` on the wire. The × cancels through the factory's
  // own onClose (tab-materialize.ts), so this door passes no callback: a launched
  // run opened on one device and closed on another means stop either way.
  void openRunTab(workflowID, name, { owns: true });
}

/** Paint the view from a store value. `undefined` means the first fetch has not
 *  resolved, which is the ONLY case that shows a loading row: a refetch driven by
 *  an invalidation must not blank a run the reader is looking at, several times a
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
  // History lands on a run already going, and a PAUSED run emits no frames at all
  // — so without this its dot would sit blank for as long as the pause lasts.
  // Unconditional now that every run's tab carries a dot, not just a parentless
  // one's. Both calls are needed: `trackRun` admits a run this client has seen no
  // event for (a tab restored on boot), and `refreshRunDots` repaints one it
  // already knows, where `trackRun` is a no-op.
  trackRun(workflowID);
  refreshRunDots();
  const parts: HTMLElement[] = [];

  parts.push(
    el(
      "div",
      { className: "run-summary" },
      el("span", { className: "run-status" }, state.status ?? "unknown"),
      el("span", { className: "run-id" }, state.workflowId),
      buildRunControls(workflowID, state.status ?? ""),
    ),
  );

  if (state.root !== undefined) {
    parts.push(el("h3", { className: "section-title" }, "Steps"));
    parts.push(el("div", { className: "run-tree" }, renderNode(state.root, 0)));
  }

  // Captured outputs are the point of reviewing a finished run: they are what
  // the steps produced. An EMPTY one is RENDERED, not dropped, and that is the
  // whole point of this block.
  //
  // A node gets a key only when it captured (KAS defaults captureOutput to true
  // and omits the key when a step sets it false), and the captured value is that
  // step's last assistant text. So an empty value is not "nothing to show": it
  // says the step ran and finished without saying anything, which is the most
  // diagnostic fact a finished run holds. Dropping it left the reader with no
  // row at all and no way to tell that step from one that never ran.
  const outputs = Object.entries(state.capturedOutputs ?? {});
  if (outputs.length > 0) {
    parts.push(el("h3", { className: "section-title" }, "Captured output"));
    for (const [node, value] of outputs) {
      parts.push(
        el(
          "div",
          { className: "run-output" },
          el("div", { className: "run-output-node" }, node),
          value.trim() === ""
            ? el(
                "div",
                { className: "run-output-empty" },
                "Empty: this step's last assistant message carried no text.",
              )
            : el("pre", { className: "run-output-body" }, value),
        ),
      );
    }
  }

  container.replaceChildren(...parts);
}

/** The run's control row. Empty for an unknown status, which renders nothing
 *  rather than an empty container. */
function buildRunControls(workflowID: string, status: string): HTMLElement | null {
  let verbs = RUN_CONTROLS[status];
  // Two different gates, because the two doors mean different things.
  //
  // An OWNED tab (the launcher's) carries the status's live verbs. A REVIEW
  // (opened from History) carries none of them — reaching a live run's controls
  // is the launching tab's job. But RETRY is not a live verb: it acts on a
  // finished run, and History is exactly where a failed one is found, so a
  // review offers retry and nothing else.
  //
  // Both are further gated on the run being PARENTLESS (user decision): an
  // agent-parented run's recovery is the agent's own, on a bridge it holds.
  if (verbs !== undefined && !shownRunOwned) {
    verbs = verbs.filter((v) => v === "retry");
  }
  if (verbs !== undefined && !shownRunParentless) {
    verbs = verbs.filter((v) => v !== "retry");
  }
  // Empty is as good as absent: a completed run offers nothing, and an empty
  // control row would be a visible container with no purpose.
  if (verbs === undefined || verbs.length === 0) {
    return null;
  }
  const row = el("div", { className: "run-controls" });
  for (const verb of verbs) {
    const dispatch = RUN_ACTION[verb];
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
          void dispatch.dispatch(workflowID);
        },
      },
      CONTROL_LABEL[verb],
    );
    row.appendChild(btn);
  }
  return row;
}

/** Render one node and its children. Depth drives indentation only. */
function renderNode(node: RunNode, depth: number): HTMLElement {
  const label =
    node.iteration === undefined ? node.nodeId : `${node.nodeId} #${String(node.iteration)}`;
  const row = el(
    "div",
    { className: `run-node run-node-${node.status}` },
    el("span", { className: "run-node-dot" }),
    el("span", { className: "run-node-id" }, label),
    el("span", { className: "run-node-type" }, node.type),
    node.agentName !== undefined && node.agentName !== ""
      ? el("span", { className: "run-node-agent" }, node.agentName)
      : null,
    el("span", { className: "run-node-dur" }, duration(node)),
  );
  row.style.paddingLeft = `calc(var(--sp-3) * ${String(depth)})`;

  const wrap = el("div", {}, row);
  for (const child of node.children ?? []) {
    wrap.appendChild(renderNode(child, depth + 1));
  }
  return wrap;
}

/** Elapsed time for a node, blank when it never finished. */
function duration(node: RunNode): string {
  if (node.startedAt === undefined || node.endedAt === undefined) {
    return "";
  }
  const ms = Date.parse(node.endedAt) - Date.parse(node.startedAt);
  if (!Number.isFinite(ms) || ms < 0) {
    return "";
  }
  return ms < 1000 ? `${String(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
}
