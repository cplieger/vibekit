// ---------------------------------------------------------------------------
// One SUBAGENT execution, read on its own page (/chat/{id}/subagent/{taskId}) —
// with its whole PIPELINE beside it, and every stage of that pipeline readable in
// place.
//
// A delegate's output was only ever readable through a keyhole: its blocks render
// inside a card that is collapsed by default and, once opened, indented inside a
// turn inside a scrolling transcript. That is the right shape for glancing at ten
// delegates at once and the wrong one for reading the report a single delegate spent
// forty minutes writing. So the card stays exactly as it is and this is the second
// surface over the same blocks.
//
// IT IS THE SHARED EXEC VIEW, not a variant of the card. `exec-view/` is the one
// subpage view for delegated work and it serves three subjects: a parentless
// workflow run, a chat-triggered workflow run, and this. `subagent-exec-source.ts`
// folds a delegate into its model exactly as `run-exec-source.ts` folds KAS's
// `inspect`, so this file owns no layout, no status vocabulary and no tree. An
// earlier revision gave the transcript's delegate card a `full` mode and rendered
// that here; it is deleted, for the same reason the run card's identical flag was
// retired — one component meaning two things was the wrong seam.
//
// PURELY FOR VIEWING. No composer, because there is nobody to type to: a delegate
// takes its instructions from the agent that dispatched it. No decision dock either,
// and that is not an omission — a delegate's permission asks are queued under its
// LAUNCHING CHAT (`decision-dock.ts` keys them by chat id), so they are answered
// where the conversation is. No controls row: a delegate has no pause, resume or
// cancel verb of its own, so `buildExecPage` gets no `controls` and the row does not
// render.
//
// THE CLOSE STOPS NOTHING, which is now the rule for every subpage view rather than
// this kind's exception: `owns: false` at the opener and no `onClose` in the factory.
// It does RELEASE, though, and stopping and releasing are different verbs: the page
// holds one detached render per member the reader opened, and a render left registered
// keeps `messages-blocks.ts`'s repaint gate answering "still mounted" for DOM that is
// gone. So the DEMAND EFFECT below owns that release — it reads the open-tab set and
// drops the mounted page once no open subagent tab names a member of the group this
// page projects. It lives here rather than in the factory because the membership test
// is a lookup in the page's own projection, which is the thing only this module holds,
// and because two stage tabs of one pipeline share one page: a per-tab `onClose` would
// have to answer for the sibling, where the membership test answers for it structurally.
//
// NOTHING IS FETCHED, and there is nothing to fetch. There is no
// `/api/subagents/{id}` and no subagent SSE event: a delegate's blocks live in the
// chat file, stamped with `agent_subtask_id` server-side so they survive replay.
// That is the one place this source is better off than the workflow's, whose step
// transcript is live-only — and it is why the empty note below has two cases where
// `run-view.ts`'s has three.
//
// IT PROJECTS THE GROUP, NOT ONE MEMBER, and that is what makes the left-hand list
// navigable. The tab names one delegate, but `subagent-exec-source.ts` draws every
// stage of its pipeline as a selectable row, so a page that projected only the tab's
// own member had a row per sibling whose transcript host nothing ever wrote into —
// selecting one showed a note saying to open its own page. `sliceSubagentGroup` walks
// the conversation ONCE and buckets every member, `subscribeToDeltas` reads every
// member's streaming signals, and a body is mounted the first time its node is SHOWN
// (`onShowNode`) and kept afterwards. The page instance is keyed by the GROUP, so
// switching between two stage tabs of one pipeline keeps those bodies.
//
// TWO CONSEQUENCES of being a projection, both stated rather than papered over. The
// store's message list is a PAGINATED WINDOW, so a delegate whose turn has been paged
// out is not resident and the page says so instead of rendering blank. And a delegate
// in a chat this client has not opened is unknown until it is, so a deep link lands on
// a page that fills itself in once that chat's messages arrive.
// ---------------------------------------------------------------------------

import { el, effect, signal, touch } from "@cplieger/reactive";
import { hasTab, openSubagentRefs, openSubagentTab } from "./tabs.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight } from "./exec-view/status.js";
import type { ExecNode } from "./exec-view/model.js";
import {
  buildDetachedBody,
  disposeDetachedBody,
  finalizeDetachedBody,
  updateDetachedBody,
} from "./messages-blocks.js";
import { refreshChatView } from "./chat.js";
import { get, isThinking, messagesVersionOf } from "./store.js";
import { blockTextSigs, blockThinkingSigs } from "./store-signals.js";
import { ICON_TAB_AGENT } from "./icons.js";
import {
  blockShape,
  shapeExtends,
  sliceSubagentGroup,
  type SubagentGroup,
  type SubagentProjection,
  type SubagentSlice,
} from "./subagent-slice.js";
import { subagentToExec } from "./subagent-exec-source.js";
import { parseSubagentRef, subagentRef } from "./tab-materialize.js";
import type { Message } from "./types.js";

/** The synthetic message id ONE MEMBER's detached render is keyed under.
 *
 *  One per (chat, subtask) rather than the real message's, for three reasons. A
 *  delegate's blocks can span two assistant messages — a mid-turn model switch splits
 *  a turn — and this page renders them as ONE transcript. `messages-blocks.ts` holds a
 *  single render map keyed by message id, so reusing the real id would have the two
 *  surfaces clobber each other's render state and then dispose the wrong one. And the
 *  page now mounts SEVERAL members at once, so the key has to name the member rather
 *  than the page. */
function renderID(chatID: string, subtaskID: string): string {
  return `sub:${chatID}:${subtaskID}`;
}

/** The identity of one PAGE INSTANCE: the GROUP, never the member the tab names.
 *
 *  Switching between two stage tabs of one pipeline then REUSES the page and keeps
 *  every body already mounted in it — the delegate re-points through `ExecRun.focus`,
 *  which `exec-view/page.ts` honours as a pick. Keyed by the member instead, every
 *  sibling-tab switch would drop the page and discard those bodies.
 *
 *  Prefixed, so it can never collide with a body's `renderID`: these are two
 *  namespaces and only the second is ever handed to `messages-blocks.ts`. */
function pageID(chatID: string, group: SubagentGroup, subtaskID: string): string {
  return group.pipeline === ""
    ? `page:sub:${chatID}:${subtaskID}`
    : `page:sub:${chatID}:pipeline:${group.pipeline}`;
}

/** The delegate on screen, as a SIGNAL so the view effect re-runs on a tab
 *  switch itself rather than waiting for the next store bump. ONE
 *  `#subagent-view` element serves every subagent tab, exactly as `#run-view`
 *  serves every run tab.
 *
 *  It is also the page's whole LIFETIME input: the demand effect empties it when
 *  no open tab wants the mounted page, and the paint effect's own empty-subject
 *  guard is what turns that into the drop. */
const shown = signal<{ chatID: string; subtaskID: string }>({ chatID: "", subtaskID: "" });

/** One mounted member's render lifecycle, keyed by subtask id.
 *
 *  Per MEMBER rather than per page, because the page hosts as many transcripts as the
 *  reader has opened and `messages-blocks.ts` answers its repaint gate as a UNION over
 *  every registered render — so a render left registered under a key nothing disposes
 *  keeps claiming "still mounted" for DOM that is gone. `run-chat-steps.ts`'s
 *  `StepRender` is the same record for the same reason. */
interface BodyRender {
  /** The host `exec-view/`'s detail pane hands out for this member's node. */
  host: HTMLElement;
  /** The block shape already mounted, for the update-versus-rebuild decision.
   *
   *  The dispatcher's incremental update appends past a watermark, so it is correct
   *  only while the prefix it mounted is unchanged. Growth at the tail keeps it; a
   *  rewind or a refetch does not. Comparing the prefix is what tells the two apart
   *  without discarding the reader's place on every streamed chunk. */
  shape: readonly string[];
  /** Whether the settled body has been sealed. Guarded because every later repaint of
   *  a finished delegate lands here too. */
  sealed: boolean;
}

/** ONE PAGE, ONE LIFETIME. Every field is a resource this page owns or an input it
 *  was last painted with, so dropping the record IS the disposal and a
 *  half-disposed page cannot be spelled. It replaced five independent module slots
 *  released by a hand-written sequence, one statement per slot, which is the shape
 *  that lets a release get part of the way — and a partial one is not benign here,
 *  because a body left registered keeps `messages-blocks.ts`'s repaint gate answering
 *  for DOM that is gone.
 *
 *  `key` is `pageID`'s answer — the GROUP, never the member the tab names — and is
 *  compared on every paint to decide keep-versus-remount. `chatID` is immutable per
 *  page because `pageID` embeds it, which is what lets the demand test below and
 *  `renderID` read it rather than re-deriving it. `projection` is refreshed by every
 *  paint and is both the latest paint's inputs (so a selection arriving from a CLICK
 *  can mount a body without waiting for the next store bump) and the group membership
 *  the demand test reads. `bodies` is one detached render per member the reader
 *  opened, all released together. `shownNodePath` is latched from this page's own
 *  `onShowNode`; a node path here IS a subtask id for a delegate (`subagentPath` is
 *  the identity), and is anything else — a pipeline root, a
 *  declared-but-undispatched stage — for a node the projection does not name, which
 *  is exactly the set that hosts no transcript. */
interface MountedPage {
  readonly key: string;
  readonly chatID: string;
  readonly view: ExecPageView;
  projection: SubagentProjection;
  readonly bodies: Map<string, BodyRender>;
  shownNodePath: string;
}
let mounted: MountedPage | undefined;

/** Whether a `view.render` is on the stack. NOT a field of the record: it is a
 *  re-entrancy flag scoped to ONE synchronous `view.render()` call rather than a
 *  resource, so a record field for it would have a lifetime of a single statement.
 *  `onShowNode` fires from inside a render AND from a click outside one; only the
 *  second has to sync the bodies itself, because `paint` syncs immediately after its
 *  own render. */
let inPaint = false;

/** Point the shared subagent view at one delegate.
 *
 *  A subagent tab's `onShow`, named for the tab factory the way `showRun` is. Retarget
 *  rather than mount: writing the subject is the whole call, and the paint effect below
 *  re-points itself off it — releasing the previous group's page when the new subject
 *  resolves to a different one — so a tab switch costs one teardown and no new
 *  subscription. */
export function showSubagent(chatID: string, subtaskID: string): void {
  shown.value = { chatID, subtaskID };
  installEffects();
}

/** A subagent tab's `refresh`. The page is a projection of the launching chat's
 *  blocks, so its window is the only thing that can make it current. */
export function refreshSubagent(chatID: string, _subtaskID: string): void {
  refreshChatView(chatID);
}

/** Whether an open subagent tab still names a member of `m`'s group.
 *
 *  A `Map.has` against the mounted page's OWN projection plus a chat compare, so it
 *  costs no group resolution per tab and no store read at all. The chat compare is
 *  load-bearing — two chats can hold the same subtask id, which
 *  `subagentTabProjectsChat`'s own tests pin — and it disposes of a malformed ref for
 *  free: `parseSubagentRef` answers two empty halves, and a mounted page's `chatID`
 *  is never empty because `paint` is unreachable with one. */
function demandHolds(m: MountedPage, refs: readonly string[]): boolean {
  for (const ref of refs) {
    const { chatID, subtaskID } = parseSubagentRef(ref);
    if (chatID === m.chatID && m.projection.slices.has(subtaskID)) {
      return true;
    }
  }
  return false;
}

/** The view's two subscriptions. Idempotent, for the reason `run-view.ts`'s is: they
 *  read the shown delegate through module state, so a tab switch re-points them, while
 *  installing one per show would leak a subscription per tab opened.
 *
 *  INSTALL ORDER IS LOAD-BEARING: `effect()` runs its body at install, and the paint
 *  effect's first run is what paints the page `showSubagent` was called for. Installing
 *  demand second runs its first pass against a page that is already mounted, so a
 *  harness whose `openSubagentRefs` answers `[]` would drop it on the spot. Demand
 *  first makes that first pass a no-op by construction. */
let effectsInstalled = false;
function installEffects(): void {
  if (effectsInstalled) {
    return;
  }
  effectsInstalled = true;

  // DEMAND IS AN INPUT, and it is a SECOND effect rather than a widening of the paint
  // effect below. `openSubagentRefs()` is its ONLY tracked read; adding it to the paint
  // effect would make every tab open, close, pin and reorder anywhere in the app
  // re-project this group and repaint the page. `run-dots.ts` carries the same shape
  // for the same reason — a seed effect beside a paint effect, because the two have
  // different inputs.
  effect(() => {
    const refs = openSubagentRefs();
    // UNTRACKED, because every transition that could produce "a page is mounted and
    // nothing wants it" is a tab-set change, which is this effect's dependency. A
    // mount can only follow an activation (`tab-materialize.ts`'s `reg.subagent.show`)
    // and an activation can only follow the row existing in the projection, so a fresh
    // mount is demanded by construction. A stale `projection` cannot answer wrong
    // either: to open a tab for a stage, that stage's invocation must be resident,
    // which means the launching chat's version bumped, which repainted this page.
    const m = mounted;
    if (m === undefined || demandHolds(m, refs)) {
      // Nothing mounted is the first pass and every pass after a drop. The one
      // residual it accepts: a page whose delegate was NOT resident left `shown` set
      // with nothing mounted, so a tab closed in that window is noticed only when the
      // page later mounts and the tab set next moves. Bounded and self-healing;
      // dropping the guard instead would empty `shown` at install and take the page
      // `showSubagent` just asked for with it.
      return;
    }
    // Write the INPUT, never `mounted`: the paint effect stays the single writer of
    // the mounted page, and its own empty-subject guard is the drop. Clearing `shown`
    // is also required rather than tidy — leaving it naming the closed delegate would
    // have the launching chat's next transcript delta re-mount the page for a tab that
    // no longer exists, which the demand effect could not notice because the tab set
    // did not move.
    shown.value = { chatID: "", subtaskID: "" };
  });

  effect(() => {
    const { chatID, subtaskID } = shown.value;
    if (chatID === "" || subtaskID === "") {
      // TOTAL over its input: an empty subject means no page. This branch is what
      // makes the demand effect's `shown` write the drop.
      unmount();
      return;
    }
    // Structural growth — a new block, a new tool call, a loaded page of history —
    // bumps the OWNING chat's version. Tracking it per chat is what gives this
    // page live background updates: the transcript's global bump used to fire
    // only for the chat on screen. BELOW the guard, because the only reachable empty
    // subject is now the demand drop — where there is no chat to stay subscribed to
    // and `shown` is already a dependency — and reading it above minted a
    // `messagesVersionSigs` entry under the key `""`.
    touch(messagesVersionOf(chatID));
    const messages = get(chatID)?.messages ?? [];
    const projection = sliceSubagentGroup(messages, subtaskID, isThinking(chatID));
    subscribeToDeltas(projection);
    paint(chatID, subtaskID, projection);
  });
}

/** Subscribe this effect to EVERY member's streaming blocks.
 *
 *  A text delta does NOT bump the chat's version: the transcript's fine-grained path
 *  writes a per-(message, block) signal instead, precisely so one chunk does not
 *  repaint a whole conversation. This page has to read the same signals or a
 *  delegate's prose would arrive in jumps, whenever some structural change happened to
 *  fire.
 *
 *  Every member rather than the tab's own, because the page mounts a sibling's
 *  transcript the moment the reader selects it, so a sibling's blocks are as much this
 *  page's as the tab's own. This is the FINE-GRAINED half of a channel that also bumps
 *  the chat's version, so what it buys is a delta painting in the tick it was written
 *  rather than on the following microtask — the coarse bump is what makes it arrive at
 *  all.
 *
 *  `get` rather than `ensure`, and that distinction is load-bearing. Minting a signal
 *  here would change the TRANSCRIPT's behaviour: `store.appendChunk` falls back to a
 *  full repaint only while no signal exists, so creating one for a block the
 *  transcript judged settled would silence that fallback and freeze the transcript's
 *  own bubble. Reading an absent signal is safe — the fallback fires, the chat's
 *  version bumps, and this effect re-runs anyway. */
function subscribeToDeltas(projection: SubagentProjection): void {
  for (const slice of projection.slices.values()) {
    for (const key of slice.sourceKeys) {
      touch(blockTextSigs.get(key), blockThinkingSigs.get(key));
    }
  }
}

function paint(chatID: string, subtaskID: string, projection: SubagentProjection): void {
  const host = document.getElementById("subagent-body");
  if (host === null) {
    return;
  }

  // Nothing resident for this delegate: no blocks AND no invocation. One honest
  // sentence per situation, because a blank page reads as a broken one in both.
  const own = projection.slices.get(subtaskID);
  if (own === undefined || (own.blocks.length === 0 && own.invocation === undefined)) {
    unmount();
    host.replaceChildren(el("div", { className: "list-empty" }, notResidentNote(chatID)));
    return;
  }

  const key = pageID(chatID, projection.group, subtaskID);
  // `parentElement` is checked for the reason `run-view.ts` checks it: `#subagent-body`
  // is one shared element, and a page cached against a detached container would send
  // every render into DOM nobody can see. The host is re-resolved per pass rather than
  // held on the record, or this check would compare a stale value against itself.
  const kept =
    mounted?.key === key && mounted.view.root.parentElement === host ? mounted : undefined;
  const m = kept ?? mountPage(host, key, chatID, projection);
  // AFTER `mountPage`, which drops the previous page and with it its own projection.
  m.projection = projection;
  inPaint = true;
  try {
    m.view.render(subagentToExec(subtaskID, projection));
  } finally {
    inPaint = false;
  }
  syncBodies(m);
}

/** Bring every mounted transcript up to the latest projection, mounting the SHOWN
 *  node's first if it is not resident yet.
 *
 *  Lazy on selection rather than eager for the whole group, because a hidden body still
 *  costs CONSTRUCTION — `exec-view/detail.ts` keeps each host for the pane's life and
 *  hides it, so mounting a pipeline's unopened stages would build every one of their
 *  tool cards for nobody. Once mounted a body STAYS mounted, so returning to a stage
 *  costs no rebuild. NOT its scroll position: `.ev-pane` declares no overflow, so the
 *  page is the scrollport and one offset is shared across every host. */
function syncBodies(m: MountedPage): void {
  const wanted = m.shownNodePath;
  const slice = wanted === "" ? undefined : m.projection.slices.get(wanted);
  // An EMPTY slice is left unmounted deliberately: `exec-view/detail.ts` shows the
  // empty note only while its host has no children, and that note is the honest answer
  // for a delegate that has produced nothing yet.
  if (slice !== undefined && slice.blocks.length > 0 && !m.bodies.has(wanted)) {
    m.bodies.set(wanted, {
      // The host the detail pane hands out for that node. `run-step-blocks.ts` is
      // deliberately NOT reused for what goes in it: that module applies
      // `RunStepPayload` frames, and these blocks are persisted, so they go through
      // the transcript's own dispatcher instead.
      host: m.view.bodyFor(wanted),
      shape: [],
      sealed: false,
    });
  }
  for (const [id, rec] of m.bodies) {
    const s = m.projection.slices.get(id);
    if (s !== undefined) {
      renderBody(rec, m.chatID, id, s);
    }
  }
}

/** Paint one member's transcript into its own host, deciding per record whether the
 *  mounted prefix can be appended to or has to be rebuilt. */
function renderBody(
  rec: BodyRender,
  chatID: string,
  subtaskID: string,
  slice: SubagentSlice,
): void {
  // Derived rather than stored on the record: both halves are in hand and stable
  // (`mounted.chatID` is readonly, `subtaskID` is the map key), so one function is
  // what stops the build, the update, the seal and the dispose disagreeing about it.
  const key = renderID(chatID, subtaskID);
  const shape = blockShape(slice.blocks);
  const message = syntheticMessage(key, slice);
  if (shapeExtends(rec.shape, shape) && rec.shape.length > 0) {
    updateDetachedBody(rec.host, message, chatID, subtaskID, slice.live, slice.sourceKeys);
  } else {
    disposeDetachedBody(key, subtaskID);
    rec.host.replaceChildren();
    rec.sealed = false;
    buildDetachedBody(rec.host, message, chatID, subtaskID, slice.live, slice.sourceKeys);
  }
  rec.shape = shape;

  // Settled: flush the markdown streams and collapse the reasoning traces, so a
  // finished delegate does not sit under a caret. Read off the SLICE, so liveness is
  // per member: a stage can finish while its siblings carry on.
  if (!slice.live && !rec.sealed) {
    rec.sealed = true;
    finalizeDetachedBody(key, subtaskID);
  }
}

/** Build the page into `#subagent-body`, replacing whatever was there. */
function mountPage(
  host: HTMLElement,
  key: string,
  chatID: string,
  projection: SubagentProjection,
): MountedPage {
  unmount();
  const built = buildExecPage({
    emptyNote: emptyNote,
    // The agent hexagon rather than the workflow glyph. No `controls`: a delegate has
    // no pause, resume or cancel verb, so the row does not render at all.
    icon: ICON_TAB_AGENT,
    // The seam a selection arrives on, from BOTH doors: a repaint's own re-derived
    // selection and a reader's click in the tree or the timeline. `paint` syncs
    // straight after its own render, so only the click has to sync here — and the
    // click is the door nothing else would tell this module about.
    //
    // The record is resolved through the module slot rather than captured, so a
    // selection can never reach a page that has been dropped — and it is safe for the
    // record to be assigned AFTER this call, because `exec-view/page.ts` fires this
    // only from `repaint()`, which runs from `render()` and from a row click, never
    // from the `buildExecPage` call these options are being passed to.
    onShowNode: (node) => {
      const m = mounted;
      if (m === undefined) {
        return;
      }
      m.shownNodePath = node?.path ?? "";
      if (!inPaint) {
        syncBodies(m);
      }
    },
  });
  mounted = {
    key,
    chatID,
    view: built,
    projection,
    bodies: new Map(),
    shownNodePath: "",
  };
  host.replaceChildren(built.root);
  return mounted;
}

/** Release the page and every render it holds. Idempotent.
 *
 *  `mounted = undefined` comes FIRST, so nothing can observe a half-disposed record
 *  and there is no ordered sequence of resets left to get part-right. The node is
 *  removed as well as disposed, so "no page mounted" and "the host holds no page"
 *  agree — a disposed subtree left in `#subagent-body` was harmless only because the
 *  view is hidden whenever no subagent tab is active. */
function unmount(): void {
  const m = mounted;
  if (m === undefined) {
    return;
  }
  mounted = undefined;
  for (const subtaskID of m.bodies.keys()) {
    disposeDetachedBody(renderID(m.chatID, subtaskID), subtaskID);
  }
  m.view.dispose();
  m.view.root.remove();
}

/** What a node with a transcript host but nothing in it should say.
 *
 *  TWO cases, where the workflow adapter's has three, and the difference is the whole
 *  reason this wording is injected: a delegate's blocks are persisted in the chat file,
 *  so there is no such thing here as content that existed and is gone, and no route
 *  this page could fetch instead.
 *
 *  It is called for a REAL delegate only: `exec-view/detail.ts` calls it when
 *  `node.transcript === true`, and only `toLeaf` sets that flag, so the pipeline root
 *  and a declared-but-undispatched stage never reach it and get no note at all. There
 *  is no "this stage has its own page" case any more — the page projects the whole
 *  group, so a selected sibling IS mounted, in place. */
function emptyNote(node: ExecNode): string {
  return inFlight(node.state)
    ? "Waiting for this delegate to produce output\u2026"
    : "This delegate finished without producing any transcript.";
}

/** Why this page has nothing to show, in the reader's terms. */
function notResidentNote(chatID: string): string {
  return get(chatID) === undefined
    ? "This conversation is not open here yet. Open it and this delegate's output appears."
    : "This delegate's turn is not in the loaded history. Scroll up in the conversation to load it.";
}

/** The synthetic message the dispatcher renders. Its id is the render key, so the
 *  build, the update, the seal and the dispose cannot disagree about it. */
function syntheticMessage(id: string, slice: SubagentSlice): Message {
  return {
    id,
    role: "assistant",
    ts: 0,
    content: "",
    blocks: slice.blocks,
    tool_calls: slice.toolCalls,
  };
}

/** Open (or focus) a delegate's page.
 *
 *  Every call here is a reader asking, from a card's footer link or a deep link, and
 *  it FOCUSES a tab that is already open, because one link means both "show me this"
 *  and "bring it back".
 *
 *  It takes no name: the tab factory derives the label from the chat store, so a tab
 *  restored on boot and a tab opened from a card read the same. */
export function openSubagentView(chatID: string, subtaskID: string): void {
  void openSubagentTab(chatID, subtaskID);
}

/** Whether an open subagent tab projects `chatID`'s transcript — the eviction
 *  sweep's exemption, registered by app.ts through `registerEvictionExemption`
 *  (store.ts must not import tabs.ts, so the composition root wires it).
 *
 *  Answered from the RESIDENT blocks: a subagent tab's ref is
 *  `chatID/subtaskID`, and the subtask ids reachable from this chat are the
 *  ones on its blocks. A tab for a delegate whose turn is NOT resident does not
 *  hold the window — its page already renders the not-resident notice, so
 *  eviction changes nothing it was showing. */
export function subagentTabProjectsChat(chatID: string): boolean {
  const msgs = get(chatID)?.messages ?? [];
  for (const m of msgs) {
    for (const b of m.blocks ?? []) {
      const st = b.agent_subtask_id;
      if (st !== undefined && st !== "" && hasTab("subagent", subagentRef(chatID, st))) {
        return true;
      }
    }
  }
  return false;
}
