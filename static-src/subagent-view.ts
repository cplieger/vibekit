// ---------------------------------------------------------------------------
// One SUBAGENT execution, read on its own page (/chat/{id}/subagent/{taskId}).
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
// Here it is also unreachable — the page is a projection of blocks the chat store
// owns, so there is nothing a close could tear down.
//
// NOTHING IS FETCHED, and there is nothing to fetch. There is no
// `/api/subagents/{id}` and no subagent SSE event: a delegate's blocks live in the
// chat file, stamped with `agent_subtask_id` server-side so they survive replay.
// That is the one place this source is better off than the workflow's, whose step
// transcript is live-only — and it is why the empty note below has two cases where
// `run-view.ts`'s has three.
//
// TWO CONSEQUENCES of being a projection, both stated rather than papered over. The
// store's message list is a PAGINATED WINDOW, so a delegate whose turn has been paged
// out is not resident and the page says so instead of rendering blank. And a delegate
// in a chat this client has not opened is unknown until it is, so a deep link lands on
// a page that fills itself in once that chat's messages arrive.
// ---------------------------------------------------------------------------

import { el, effect, signal, touch } from "@cplieger/reactive";
import { hasTab, openSubagentTab } from "./tabs.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { inFlight } from "./exec-view/status.js";
import type { ExecNode } from "./exec-view/model.js";
import {
  buildDetachedBody,
  disposeDetachedBody,
  finalizeDetachedBody,
  updateDetachedBody,
} from "./messages-blocks.js";
import { get, isThinking, messagesVersionOf } from "./store.js";
import { blockTextSigs, blockThinkingSigs } from "./store-signals.js";
import { ICON_TAB_AGENT } from "./icons.js";
import { blockShape, shapeExtends, sliceSubagent, type SubagentSlice } from "./subagent-slice.js";
import { subagentPath, subagentToExec } from "./subagent-exec-source.js";
import { subagentRef } from "./tab-materialize.js";
import type { Message } from "./types.js";

/** The synthetic message id the delegate's detached render is keyed under.
 *
 *  One per (chat, subtask) rather than the real message's, for two reasons. A
 *  delegate's blocks can span two assistant messages — a mid-turn model switch splits
 *  a turn — and this page renders them as ONE transcript. And `messages-blocks.ts`
 *  holds a single render map keyed by message id, so reusing the real id would have
 *  the two surfaces clobber each other's render state and then dispose the wrong one. */
function renderID(chatID: string, subtaskID: string): string {
  return `sub:${chatID}:${subtaskID}`;
}

/** The delegate on screen, as a SIGNAL so the view effect re-runs on a tab
 *  switch itself rather than waiting for the next store bump. ONE
 *  `#subagent-view` element serves every subagent tab, exactly as `#run-view`
 *  serves every run tab. */
const shown = signal<{ chatID: string; subtaskID: string }>({ chatID: "", subtaskID: "" });

/** The page, built once and re-pointed, and the delegate its render belongs to.
 *  `exec-view/` knows nothing about subagents; the adapter is what makes this a
 *  second consumer of that page rather than a second page. */
let page: ExecPageView | undefined;
let pageKey = "";

/** The block shape already mounted into the detail host.
 *
 *  The dispatcher's incremental update appends past a watermark, so it is correct only
 *  while the prefix it mounted is unchanged. Growth at the tail keeps it; a rewind or
 *  a refetch does not. Comparing the prefix is what tells the two apart without
 *  discarding the reader's place on every streamed chunk. */
let renderedShape: readonly string[] = [];

/** Whether the settled body has been sealed. Guarded because every later repaint of a
 *  finished delegate lands here too. */
let sealed = false;

/** Point the shared subagent view at one delegate.
 *
 *  A subagent tab's `onShow`, named for the tab factory the way `showRun` is. Retarget
 *  rather than mount: the previous delegate's render is dropped here and the effect
 *  below re-points itself, so a tab switch costs one teardown and no new subscription. */
export function showSubagent(chatID: string, subtaskID: string): void {
  shown.value = { chatID, subtaskID };
  installViewEffect();
}

/** The view's single subscription to the store. Idempotent, for the reason
 *  `run-view.ts`'s is: it reads the shown delegate through module state, so a tab
 *  switch re-points it, while installing one per show would leak a subscription per
 *  tab opened. */
let viewEffectInstalled = false;
function installViewEffect(): void {
  if (viewEffectInstalled) {
    return;
  }
  viewEffectInstalled = true;
  effect(() => {
    const { chatID, subtaskID } = shown.value;
    // Structural growth — a new block, a new tool call, a loaded page of history —
    // bumps the OWNING chat's version. Tracking it per chat is what gives this
    // page live background updates: the transcript's global bump used to fire
    // only for the chat on screen. Read before the early return so the effect
    // stays subscribed on the passes that bail below.
    touch(messagesVersionOf(chatID));
    if (chatID === "" || subtaskID === "") {
      return;
    }
    const messages = get(chatID)?.messages ?? [];
    const slice = sliceSubagent(messages, subtaskID, isThinking(chatID));
    subscribeToDeltas(slice);
    paint(chatID, subtaskID, messages, slice);
  });
}

/** Subscribe this effect to the delegate's own streaming blocks.
 *
 *  A text delta does NOT bump the chat's version: the transcript's fine-grained path
 *  writes a per-(message, block) signal instead, precisely so one chunk does not
 *  repaint a whole conversation. This page has to read the same signals or a
 *  delegate's prose would arrive in jumps, whenever some structural change happened to
 *  fire.
 *
 *  `get` rather than `ensure`, and that distinction is load-bearing. Minting a signal
 *  here would change the TRANSCRIPT's behaviour: `store.appendChunk` falls back to a
 *  full repaint only while no signal exists, so creating one for a block the
 *  transcript judged settled would silence that fallback and freeze the transcript's
 *  own bubble. Reading an absent signal is safe — the fallback fires, the chat's
 *  version bumps, and this effect re-runs anyway. */
function subscribeToDeltas(slice: SubagentSlice): void {
  for (const key of slice.sourceKeys) {
    touch(blockTextSigs.get(key), blockThinkingSigs.get(key));
  }
}

function paint(
  chatID: string,
  subtaskID: string,
  messages: readonly Message[],
  slice: SubagentSlice,
): void {
  const host = document.getElementById("subagent-body");
  if (host === null) {
    return;
  }

  // Nothing resident for this delegate: no blocks AND no invocation. One honest
  // sentence per situation, because a blank page reads as a broken one in both.
  if (slice.blocks.length === 0 && slice.invocation === undefined) {
    dropPage();
    host.replaceChildren(el("div", { className: "list-empty" }, notResidentNote(chatID)));
    return;
  }

  const key = renderID(chatID, subtaskID);
  // `parentElement` is checked for the reason `run-view.ts` checks it: `#subagent-body`
  // is one shared element, and a page cached against a detached container would send
  // every render into DOM nobody can see.
  const kept = pageKey === key && page?.root.parentElement === host ? page : undefined;
  const view = kept ?? mountPage(host, key);
  view.render(subagentToExec(messages, subtaskID, slice));

  // The delegate's transcript, into the host the detail pane hands out for its node.
  // `run-step-blocks.ts` is deliberately NOT reused: that module applies
  // `RunStepPayload` frames, and these blocks are persisted, so they go through the
  // transcript's own dispatcher instead.
  const body = view.bodyFor(subagentPath(subtaskID));
  const shape = blockShape(slice.blocks);
  const message = syntheticMessage(key, slice);
  if (kept !== undefined && shapeExtends(renderedShape, shape) && renderedShape.length > 0) {
    updateDetachedBody(body, message, chatID, subtaskID, slice.live);
  } else {
    disposeDetachedBody(key, subtaskID);
    body.replaceChildren();
    sealed = false;
    buildDetachedBody(body, message, chatID, subtaskID, slice.live);
  }
  renderedShape = shape;

  // Settled: flush the markdown streams and collapse the reasoning traces, so a
  // finished delegate does not sit under a caret.
  if (!slice.live && !sealed) {
    sealed = true;
    finalizeDetachedBody(key, subtaskID);
  }
}

/** Build the page into `#subagent-body`, replacing whatever was there. */
function mountPage(host: HTMLElement, key: string): ExecPageView {
  dropPage();
  const built = buildExecPage({
    emptyNote: emptyNote,
    // The agent hexagon rather than the workflow glyph. No `controls`: a delegate has
    // no pause, resume or cancel verb, so the row does not render at all.
    icon: ICON_TAB_AGENT,
  });
  page = built;
  pageKey = key;
  renderedShape = [];
  sealed = false;
  host.replaceChildren(built.root);
  return built;
}

/** Release the page and the render it holds. Idempotent. */
function dropPage(): void {
  if (pageKey !== "") {
    disposeDetachedBody(pageKey, shown.peek().subtaskID);
  }
  page?.dispose();
  page = undefined;
  pageKey = "";
  renderedShape = [];
  sealed = false;
}

/** What a node with a transcript host but nothing in it should say.
 *
 *  TWO cases, where the workflow adapter's has three, and the difference is the whole
 *  reason this wording is injected: a delegate's blocks are persisted, so there is no
 *  such thing here as content that existed and is gone. A sibling stage the reader has
 *  selected has simply not been projected yet — `subagent-exec-source.ts` computes the
 *  slice only for the delegate the tab names, so selecting a stage in a pipeline is a
 *  request to open its own page. */
function emptyNote(node: ExecNode): string {
  if (node.path !== subagentPath(shown.peek().subtaskID)) {
    return "This stage has its own page. Open it from its card in the conversation to read its transcript here.";
  }
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
 *  NO GUARD, deliberately: this is `openRunView`'s half of the run tab's pair and not
 *  `openRunSubTab`'s. A subagent tab is never offered automatically, because neither
 *  reason the run's automatic offer exists transfers — a run outlives the turn that
 *  launched it and emits a progress frame per node event, and a delegate does neither.
 *  Every call here is a reader asking, from a card's footer link or a deep link, and it
 *  FOCUSES a tab that is already open, because one link means both "show me this" and
 *  "bring it back".
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
