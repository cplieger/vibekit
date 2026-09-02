// ---------------------------------------------------------------------------
// Chat lifecycle: create, activate, delete, switch, send prompt. Also owns
// the chat-tab registration (openChatTab) and load-more pagination.
//
// The store is the single source of truth; this module just wires user
// actions to server commands and renders the active session.
// ---------------------------------------------------------------------------

import type { ResumableSessionRow } from "./types.js";
import {
  getActiveId,
  get,
  watchSession,
  setActive,
  setModel,
  upsertHeader,
  activeSession,
  removeChat,
  tabStatusFor,
  isEmptyChat,
  transcriptStale,
} from "./store.js";
import { loadList, loadMessages } from "./store-load.js";
import { effect, el } from "@cplieger/reactive";
import type { ChatHeader, Session } from "./types.js";
import { ensureBound } from "./banner-stack.js";
import {
  openTab,
  adoptSubject,
  activateTab,
  tabIdFor,
  getActiveTabId,
  renameTab,
  setTabStatus,
  setTabTooltip,
  openChatRefs,
  type OpenTabOutcome,
  type TabDotStatus,
} from "./tabs.js";
import { beginAdopt, adoptCommitted, opFailed } from "./tabs-sync.js";
import { hasPendingDecision, dropDecisions } from "./decision-dock.js";
import { submitPrompt } from "./submit.js";
import { chatSkeleton } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import {
  mountChatView,
  setLoadMore,
  loadTurnRail,
  pointTurnRail,
  fadeInTranscript,
  activeTranscriptView,
  transcriptViewFor,
  disposeChatView,
} from "./messages.js";
import { addAttachment } from "./attachments.js";
import {
  saveComposerState,
  restoreComposerState,
  retargetComposer,
  seedComposerState,
  flushComposerDraft,
  dropComposerState,
} from "./composer-state.js";
import { setCurrentModel, getLastModel } from "./session-context.js";
import { labelForMode } from "./roles.js";
import { refreshContextUI } from "./context-ui.js";
import { $ } from "./dom.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { info } from "./toast.js";
import { createChat, forkChat, setMode, type CreatedChat } from "./actions/chat.js";
import { newOpID } from "./transport.js";

// --- Bus: activate chat from other modules without importing chat.ts ---

onBus(BUS_ACTIVATE_CHAT, (p) => {
  activateChatView(p.chatID);
  if (p.then !== undefined) {
    p.then();
  }
});

// --- Chat tab registration ---

/** Open (or activate) a chat tab. Pass `{ activate: false }` for a bulk open:
 *  activation runs activateChatView (messages fetch + conflicts prefetch), so
 *  bulk-opening N chats active would fan out 2N requests.
 *
 *  A ROUND TRIP now, and it resolves once the tab is in the projection: the tab
 *  set is server-owned, so `open_tab` is what creates it and the `tabs_changed`
 *  frame is what paints it. The only local decision left here is the NAME, which
 *  the caller knows and a subject cannot carry.
 *
 *  Nothing else about the tab is decided here. The view, the route, the
 *  activation hook and the teardown all come from the one per-kind factory
 *  (tab-materialize.ts), which the composition root wires to this module's own
 *  `activateChatView` / `closeChatTab` / `chatTabDot` — so a chat tab opened from
 *  History, from a fork, from a boot list or from another device behaves
 *  identically. */
export async function openChatTab(
  id: string,
  name: string,
  opts?: { activate?: boolean; parentTabID?: string },
): Promise<OpenTabOutcome> {
  return openTab({
    kind: "chat",
    ref: id,
    name,
    // A side conversation hangs off the chat it came from. `owns` stays at its
    // default (true) deliberately: the side chat owns its own bridge, so closing
    // its tab must tear that bridge down the way any chat tab does. `owns: false`
    // is for a tab that only WATCHES work another chat owns.
    //
    // `parentTabID`, not a chat id: `TabSubject.Parent` names an open TAB, and a
    // chat id is no longer one. It was `parentId` fed with the parent CHAT's id,
    // which no tab has, so the server promoted every tangent to top level — the
    // silent degradation an unresolvable parent is designed to take, reached by a
    // caller passing the wrong id space rather than by a parent that closed.
    ...(opts?.parentTabID === undefined ? {} : { parent: opts.parentTabID }),
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

/** A chat tab's dot state right now: the chat's live state, with a pending ask
 *  outranking it.
 *
 *  Named rather than inlined because two producers need the same answer — this
 *  module's own opener and the tab factory (tab-materialize.ts), which cannot
 *  compute it itself: the pending-ask half reads the decision dock, so the factory
 *  takes it as an injected opener instead. Two copies of a precedence rule is two
 *  things that can disagree. */
export function chatTabDot(id: string): TabDotStatus | "" {
  return tabStatusFor(get(id), hasPendingDecision(id));
}

/** Everything closing a chat TAB does on this device — client-local cleanup
 *  only, identical whoever closed the tab.
 *
 *  Run DEFERRED for a close this device dispatched (the pending-op machine's
 *  confirmation, exactly once per closed tab) and immediately for a remote
 *  close's applied removal. Nothing here dispatches anything: the process
 *  teardown AND the retention-off record delete are both the server's
 *  `close_tab` operation now, so every branch that used to decide between
 *  keeping, dropping and deleting has converged on the one local cleanup.
 *  `delete_chat` survives as History's delete path and nothing else's. */
export function closeChatTab(id: string): void {
  // The draft belongs to the CHAT, not to the tab, so a pending save goes
  // out before the row drops: a close that keeps the record keeps the unsent
  // text with it, and a retention-off close's tombstone drops the late save
  // server-side rather than racing it.
  flushComposerDraft();
  // The chat's unanswered asks go with the tab. The close cancelled the turn
  // and the chat's runs server-side, so nothing here is still live — and the
  // dock queue is keyed by chat id, so a queue left behind was resurrected by
  // reopening the SAME id: the card came back and the tab dot said the chat
  // needed a decision that no longer existed.
  dropDecisions(id);
  // The chat's transcript view — active or parked — runs the real per-view
  // dispose. Before removeChat: the store reassigning the active chat repaints
  // synchronously, and a dead view still in the registry would be parked by
  // that paint only to be thrown away here.
  disposeChatView(id);
  // The store row, whatever retention says: a closed chat is a chat that
  // stopped being in a tab. Under retention the record survives server-side
  // and reopening session/loads it back; with retention off the server deleted
  // it inside the close.
  removeChat(id);
  // Local only: a close that kept the record kept its draft with it, and
  // reopening the chat seeds the text back from the server.
  dropComposerState(id);
  // `removeChat` reassigns the store's active chat to `remaining[0]` on its own,
  // with no activation behind it, so the composer would be left belonging to
  // nothing while the store already pointed somewhere. `dropComposerState` clears
  // the BOX for that case; this is the other half, the KEY — without it the next
  // keystroke lands in no chat at all and is parked nowhere. Harmless when the
  // strip activates a neighbour straight after, because a retarget is idempotent.
  retargetComposer(getActiveId());
}

// --- Activation generation ---

/** Bumped by every activateChatView call.
 *
 *  Two activations for one chat are ordinary: the History page opens the tab
 *  (openChatTab → onShow → activateChatView) and openPreviousSession activates it
 *  again, and a user can switch away and back while a fetch is in flight.
 *  store-load keys its abort controller by chat id, so the newer fetch ABORTS the
 *  older one, and loadMessages reports an abort the same way it reports a failure.
 *  Without this guard the superseded activation painted its retry box, so opening
 *  a previous chat showed "Failed to load messages." beside the transcript that
 *  had just loaded fine.
 *
 *  It completes the `getActiveId() !== id` check beside it rather than adding a
 *  second mechanism: that one catches a newer activation of a DIFFERENT chat, this
 *  one catches a newer activation of the SAME chat. */
let activationGen = 0;

/** Remove a previous activation's failure box from the chat's transcript view.
 *
 *  Every activation clears it rather than each success path remembering to: the
 *  box describes a load the activation now running has superseded, and the
 *  transcript repaint does not own it — it is appended furniture, which is why
 *  the Retry button used to remove it by hand. */
function clearChatLoadError(): void {
  for (const box of (activeTranscriptView() ?? $.messages).querySelectorAll(".load-error")) {
    box.remove();
  }
}

/** The transcript's failure affordance: what went wrong, and one button that
 *  tries again.
 *
 *  Shared by the two ways a chat can fail to open — its messages did not load,
 *  and its record is not in this device's store at all — because the reader's
 *  move is the same either way, and a blank pane is the one answer that is never
 *  acceptable. Retry re-activates, which re-runs whichever recovery the branch
 *  that painted the box owns. */
function paintChatLoadError(message: string, id: string): void {
  const box = el("div", { className: "load-error" }, el("span", {}, message));
  const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
  btn.addEventListener("click", () => {
    activateChatView(id);
  });
  box.appendChild(btn);
  (activeTranscriptView() ?? $.messages).appendChild(box);
}

/** Re-read the chat list for a tab whose chat this device's store does not hold,
 *  and activate it again if the read produces it.
 *
 *  One attempt per activation, and it cannot loop: a re-read that still does not
 *  produce the chat returns, and one that does re-enters activateChatView on the
 *  ordinary path. The generation check is what makes it safe to fire and forget —
 *  the reader may have switched tabs while the request was in flight. */
async function healMissingChat(id: string, gen: number): Promise<void> {
  if (!(await loadList())) {
    return;
  }
  if (getActiveId() !== id || activationGen !== gen || get(id) === undefined) {
    return;
  }
  activateChatView(id);
}

/** Point every per-chat view at `id` and load its transcript. This is a chat
 *  tab's `onShow`, exported so the tab factory can name it without this module
 *  having to hand the factory a spec. */
export function activateChatView(id: string): void {
  // Save-then-restore against the shared composer, the same pair activateFile
  // runs against the shared editor textarea. The save MUST precede setActive:
  // it reads the outgoing chat's id, which nothing can recover afterwards.
  saveComposerState();
  const gen = ++activationGen;
  // Re-key the rail on the switch itself: before the repaint below, and before
  // any branch or early return can skip it. It used to be re-keyed as a side
  // effect of a successful message load, so every path that never reaches that
  // callback inherited the previous chat's markers — a new chat is an empty
  // chat and loads nothing, a failed load returns early, a purged record
  // returns earlier still, and even a healthy switch left the rail describing
  // the old chat until its own fetch landed.
  //
  // Pointing the rail is the whole update here, because the rail spans the
  // SESSION: an empty chat has no turns to fetch, and a brand-new chat's id
  // exists nowhere but the tab that minted it, so asking for its turns is a
  // guaranteed 404. The loaded branch fetches for itself below.
  //
  // The SCROLLER is not reset here any more: the multiplexer owns per-view
  // scroll state — the park inside setActive's repaint saves the outgoing
  // view's, and the incoming view either restores its own (unpark) or starts
  // fresh (a new view resets). Landing at the bottom is the replay's own
  // behaviour: a cold view mounts its turns, and every user-triggered turn
  // pops to the live edge, exactly as a live arrival does.
  pointTurnRail(id);
  setActive(id);
  restoreComposerState(id);
  ensureBound();
  clearChatLoadError();
  const session = get(id);
  if (session === undefined) {
    // A tab naming a chat this device's store does not hold. It used to be a
    // dead end: the pane kept whatever the previous chat left in it, with no
    // error, no retry and no self-heal, so a reader saw a blank transcript until
    // they reloaded the whole page. Measured after a forced restart, where a
    // TRUNCATED /api/chats answer left several open tabs with no store row at
    // all (the server half of that is internal/chat's shared-scan ownership).
    //
    // The store is provably stale against a set the server minted, so the answer
    // is one re-read. The affordance is painted FIRST, so a re-read that does not
    // produce the chat leaves something to act on rather than a blank pane.
    paintChatLoadError("This conversation isn't loaded yet.", id);
    void healMissingChat(id, gen);
    return;
  }

  if (session.usage.context_size === 0 && session.model !== "") {
    // Store-safe refresh of the derived context size (setModel recomputes
    // usage.context_size); mutating `session.usage` directly writes to a
    // reference subscribers never see.
    setModel(id, session.model);
  }

  if (isEmptyChat(session)) {
    // The picker shows itself: its visibility is an effect over this same
    // predicate (picker.ts bindVisibility). Only the draft is this branch's —
    // and the record GET exists only to adopt the server-held draft, so it is
    // gated like the loaded branch: a window this device already fetched and no
    // gap has undermined yielded that draft the first time.
    if (transcriptStale(session)) {
      seedEmptyChatDraft(id);
    }
  } else if (!transcriptStale(session)) {
    // The window is the server's answer and no gap or eviction has undermined
    // it since it was fetched, so switching back costs ZERO fetches: the
    // repaint above already rendered it (or unparked the resident view whole).
    // Only the per-chat view furniture is re-wired, and the rail decides its
    // own fetch off its record — the message count moves under background SSE
    // ingest while the window (which tracks the tail live) stays trustworthy.
    setupLoadMore(id);
    void loadTurnRail(id);
  } else {
    // Hydrate messages from the server, then render.
    //
    // A SKELETON MAY ONLY PAINT OVER AN EMPTY TRANSCRIPT. It is a placeholder,
    // and this chat's messages are already on screen whenever the store holds
    // them: the repaint above runs synchronously off setActive, so a switch back
    // to a loaded chat renders the whole conversation and THEN stacked a
    // placeholder underneath it for the length of the refresh round trip. That
    // was every tab switch — turns above, shimmer below, shimmer gone.
    //
    // On a cold transcript the paint is still deferred by 150ms, so a fast
    // (cached) open never flashes it: skeletonTiming appends it only if the load
    // is still running at 150ms, and the cancel() call on completion clears the
    // pending timer and removes it if it was shown. (min-visible stays 0 — the
    // skeleton shares the messages container.)
    let skeletonPainted = false;
    const skeleton =
      session.messages.length > 0
        ? null
        : skeletonTiming(() => {
            skeletonPainted = true;
            const skel = chatSkeleton();
            // Into the ACTIVE VIEW: the placeholder stands in for this chat's
            // transcript, and the view's own column geometry (flex-end,
            // centred cap) is what positions it. setActive above painted, so
            // the view exists; the multiplexer fallback covers a fixture that
            // never mounted one.
            (activeTranscriptView() ?? $.messages).appendChild(skel);
            return () => {
              skel.remove();
            };
          });
    void loadMessages(id).then((ok) => {
      skeleton?.cancel();
      if (getActiveId() !== id || activationGen !== gen) {
        return;
      }
      if (!ok) {
        paintChatLoadError("Failed to load messages.", id);
        return;
      }
      // The turns that replace the placeholder fade in rather than cutting.
      // loadMessages' own bumpMessages paints synchronously, so they are already
      // in the DOM by the time this runs — and no frame has gone to the screen
      // between the two, which is why the swap is one transition rather than a
      // flash of both. Only when a skeleton was actually painted: a fast open has
      // nothing to transition from, and fading it would add a flicker to
      // something that is instant today.
      if (skeletonPainted) {
        fadeInTranscript();
      }
      // The chat's record is in now, so its stored draft can be adopted. Only
      // matters when this device holds none for the chat — after a reload — and
      // it deliberately loses to a draft the user has started typing since the
      // activation.
      seedComposerState(id);
      const fresh = get(id);
      if (fresh !== undefined) {
        setupLoadMore(id);
      }
      // The rail's index is session-wide and independent of the message window,
      // so it is its own fetch rather than something derived from what loaded.
      // FORCED: this activation found the transcript stale, and the rail is
      // implicated with it — but the load that just landed re-stamped the
      // session fresh, so the rail's gate can no longer see the verdict.
      void loadTurnRail(id, { force: true });
    });
  }
}

/** Fetch a message-less chat's record so its stored draft can be adopted.
 *
 *  The draft rides the single-chat GET (deliberately, so it travels on neither
 *  the list nor a chat_updated frame), and the branch above skips that GET for a
 *  chat with no messages — which is exactly the chat that can still hold one. A
 *  record exists from the moment the chat is created now, so the user can pick a
 *  mode, type half a message, reload, and come back to a persisted zero-message
 *  chat whose draft the server is holding. Without this the box came back empty,
 *  which defeats the reason the draft is server-side at all.
 *
 *  There is no unacknowledged-chat guard here any more, and its absence is the
 *  point: every chat is a server record before its tab opens, so the GET has
 *  something to answer. The re-check after the await stays — the user can switch
 *  chats while it is in flight, and the seed writes into the shared composer. */
function seedEmptyChatDraft(id: string): void {
  void loadMessages(id).then((ok) => {
    if (ok && getActiveId() === id) {
      seedComposerState(id);
    }
  });
}

// --- Pagination ---

function setupLoadMore(chatID: string): void {
  const session = get(chatID);
  if (session === undefined) {
    return;
  }
  setLoadMore(
    session.has_more
      ? (): void => {
          const oldest = session.messages[0];
          if (oldest === undefined) {
            return;
          }
          void loadMessages(chatID, oldest.id).then(() => {
            // Remove the load-more skeleton — scroll.ts watches for its
            // removal as the "load complete" signal. Scoped to this chat's
            // view: with parked views resident the id can exist more than
            // once, and this pass owns only its own chat's furniture.
            transcriptViewFor(chatID)?.querySelector(`[id="load-more-skeleton"]`)?.remove();
            if (getActiveId() !== chatID) {
              return;
            }
            const s = get(chatID);
            if (s === undefined) {
              return;
            }
            // Store mutations bump version; the chat-view effect re-renders
            // and reconcile prepends the older messages at the top with
            // identity preserved for already-mounted ones.
            setupLoadMore(chatID);
          });
        }
      : null,
    session.has_more,
  );
}

// --- Sending prompts ---

/** Send from the composer.
 *
 *  This used to hide the model picker first, which is why every OTHER sender
 *  (the goal row, the tangent row) left the overlay up: the hide lived on one
 *  path instead of on the state. `submitPrompt` sets `thinking` synchronously,
 *  and the picker's own effect keys on it, so the overlay closes for every
 *  sender now without this function knowing the picker exists. */
export function sendPrompt(text: string): void {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  void submitPrompt(chatID, text);
}

// --- Session lifecycle ---

const NEW_CHAT_NAME = "New conversation";

/** Seed the store row for a chat the SERVER has just created.
 *
 *  Nothing is minted here any more. The id, the name and the model all come off
 *  the header the create command returned, so the row is a projection of what the
 *  server persisted rather than a guess this tab will have to reconcile later —
 *  which is invariant 2 applied to chat creation, the same rule the message path
 *  has always followed.
 *
 *  `usage.context_size` is the one derived field: it is a function of the model
 *  and the client owns the model catalog, so the server's header cannot carry it. */
function seedChat(header: ChatHeader): void {
  upsertHeader(header);
  const model = header.model ?? "";
  if (model !== "") {
    setModel(header.id, model);
  }
}

/** Adopt the tab a creating command opened server-side: paint it from the
 *  response, hand the pending-op machine what it committed, and activate it.
 *
 *  This is what replaced the second POST. `create_chat` and `fork_chat` write
 *  the record and open the tab under one coordinator lock, so the reply already
 *  carries the committed subject — dispatching `open_tab` after it was a whole
 *  round trip to learn `created: false`. A reply with no subject (a server
 *  composition with no tab store) has nothing to adopt, so the pending op is
 *  retired and whatever frame may come paints the ordinary remote way. */
function adoptCreatedTab(opID: string, created: CreatedChat): void {
  const subject = created.subject;
  if (subject === undefined) {
    opFailed(opID);
    return;
  }
  adoptSubject(subject, created.chat.name === "" ? NEW_CHAT_NAME : created.chat.name);
  adoptCommitted(opID, subject, created.version, true);
  activateTab(subject.id);
}

/** Create a new chat and open its tab. Resolves to the new chat's id, or "" when
 *  the server refused. If initialPrompt is non-empty, send it immediately.
 *
 *  ASYNC because the id is the SERVER's. It was minted here
 *  (`c-${Date.now()}-${Math.random()}`), which meant that between clicking New
 *  chat and the first prompt the chat existed only in this browser's memory: no
 *  other device could resolve the id, the shared arrangement had to exclude it,
 *  and four exemptions existed to keep the rest of the app from acting on its
 *  absence. Awaiting a round trip is the cost of not having that window.
 *
 *  Every caller must await or explicitly detach — `no-floating-promises` is on, so
 *  the compiler names the sites, but a bare `void` at one that reads `getActiveId()`
 *  on the next line would silently read the PREVIOUS chat. See the callers.
 *
 *  Nothing shows the picker here: `setActive` on a chat with no messages is
 *  already the condition its effect watches, and sending a prompt sets
 *  `thinking`, so both arms of this branch are covered by the same derivation. */
export async function createSession(initialPrompt?: string): Promise<string> {
  const model = getLastModel();
  // Minted HERE and passed as an argument, never inside the action's run(): the
  // framework re-runs run() per retry attempt, so an op id minted in there would
  // be fresh on every attempt and the server would mint a second chat for one
  // gesture. See transport.newOpID. Registered with the pending-op machine
  // BEFORE the dispatch, so the tabs frame the create commits correlates as this
  // device's own however early it lands.
  const opID = newOpID();
  beginAdopt(opID);
  const created = await createChat.dispatch({ opID, model });
  if (created === null) {
    // The action framework has already raised its toast. Nothing is seeded and no
    // tab opens, which is the honest surface: there is no chat.
    opFailed(opID);
    return "";
  }
  seedChat(created.chat);
  const id = created.chat.id;
  setActive(id);
  // The composer belongs to THIS chat from here, not from the activation below.
  // The activation that retargets it is the adopted tab's `onShow`, so without
  // this the box would belong to the chat the user just left for a beat — and
  // anything typed into it would be filed under that chat and flushed to the
  // server as its draft.
  retargetComposer(id);
  // The reply carries the tab the create opened server-side, so the row is
  // painted and ACTIVATED here, from the response — which is the whole point of
  // pressing New chat, with no second round trip and no frame wait in the way.
  adoptCreatedTab(opID, created);

  if (initialPrompt !== undefined && initialPrompt !== "") {
    setCurrentModel(model);
    sendPrompt(initialPrompt);
  }
  return id;
}

/** Open a TANGENT off `parentChatID`: a real chat that starts with the parent's
 *  whole conversation behind it, opened as a SUB-TAB under the one it came from.
 *
 *  The context is the parent's REAL context, not a copy of it. `chat.fork` calls
 *  KAS's own `session/fork`, and the new chat is created already bound to the
 *  session it returns, so the transcript arrives from the session/load replay
 *  and nothing is copied here. Nothing syncs the two afterwards either: one JSON
 *  file per chat, one SSE topic per chat, and no cross-chat write path.
 *
 *  Five things it is, each load-bearing:
 *
 *    - A real PERSISTED chat, created by the fork command rather than by a first
 *      prompt. So closing it leaves it in History like any other chat, and
 *      invariant 3 holds — the bridge it gets has a chat file behind it.
 *    - It OWNS its bridge: the tab keeps the default `owns`, so its × tears the
 *      tangent down rather than orphaning a process. `owns: false` would be
 *      wrong — a forked session is genuinely this tab's own work, not a view
 *      over someone else's.
 *    - A SUB-TAB of its parent, permanently: `TabSubject.Parent` is set at open
 *      and never reassigned, which is what makes a parent cycle unrepresentable
 *      and why there is no reparent command and no "Promote to its own tab" any
 *      more. The parent's close cascade takes it along.
 *    - It inherits model, mode and effort from the parent — read SERVER-side off
 *      the parent's record, because the record is the truth about all three and a
 *      tab's projection can be stale. Nothing is dispatched from here for them.
 *    - Nothing seeds a first prompt. There is no selection to seed one from: with
 *      the whole conversation inherited, a selected phrase chooses nothing.
 *
 *  The name is left to the ordinary precedence (the agent's focus title, else the
 *  first prompt's truncation), which is why nothing here names it. */
export async function openTangentChat(parentChatID: string): Promise<void> {
  if (parentChatID === "" || get(parentChatID) === undefined) {
    return;
  }
  const model = get(parentChatID)?.model ?? getLastModel();
  // The fork creates the chat and MINTS its id, so the sub-tab can only open once
  // the reply lands. Before server minting the tab opened first and the dispatch
  // followed; now there is no id to open a tab for until the server answers.
  const opID = newOpID();
  beginAdopt(opID);
  const created = await forkChat.dispatch({ opID, parentChatID });
  if (created === null) {
    opFailed(opID);
    return;
  }
  seedChat(created.chat);
  const id = created.chat.id;
  setActive(id);
  // Same round-trip window as createSession's: the tangent is the active chat
  // now, so the composer has to be its own before anything else runs.
  retargetComposer(id);
  // The reply's subject already carries the PARENT the coordinator nested the
  // tangent under — the server resolved the parent's tab, so nothing here has
  // to. Adopting it paints the sub-tab and activates it, with no second POST.
  adoptCreatedTab(opID, created);
  setCurrentModel(model);
}

export function switchSession(id: string): void {
  // The chat id and its TAB id are different values now — the tab's is opaque and
  // server-minted — so reaching a chat's tab goes through the one lookup rather
  // than through the assumption that the two are the same string.
  const tabID = tabIdFor("chat", id);
  if (tabID === "" || (id === getActiveId() && getActiveTabId() === tabID)) {
    return;
  }
  activateTab(tabID);
}

/** Attach workspace files to the active chat's next prompt. Each shows as a
 *  removable pill below the textarea. The user still types their message; the
 *  attachments are sent alongside on submit. Switches to the chat tab if needed so
 *  the user sees where they went.
 *
 *  PLURAL, and that is not a convenience. Every caller was already a loop over a
 *  batch — an upload's landed paths, the picker's selection — and each iteration
 *  called the singular form, which was fine while the chat id was minted locally:
 *  the first call set the active chat synchronously and the rest saw it. With the
 *  id coming from the server, N iterations would each find no active chat and each
 *  ask for one, so a three-file drop onto an empty workspace would create three
 *  chats and attach one file to each. Taking the batch makes the ensure-a-chat step
 *  happen once by construction, rather than guarding a race with a flag.
 *
 *  AWAITS its own create for the same reason the create exists: the lines after it
 *  address the chat it produced. */
export async function attachPathsToActiveChat(paths: readonly string[]): Promise<void> {
  if (paths.length === 0) {
    return;
  }
  let id = getActiveId();
  if (id === "") {
    id = await createSession();
    if (id === "") {
      return;
    }
  }
  const tabID = tabIdFor("chat", id);
  if (tabID !== "" && getActiveTabId() !== tabID) {
    activateTab(tabID);
  }
  for (const p of paths) {
    addAttachment(p);
  }
  $.promptInput.focus();
}

/** Open a session the previous-session picker listed.
 *
 *  Every row the picker offers is a TAB CONVERSATION: the server lists a session
 *  only when a vibekit chat claims it (`toResumable`), so `chat_id` is always
 *  present and opening a row is just opening that chat.
 *
 *  The ADOPTION path is gone with the unclaimed rows that reached it. It ran
 *  `resume_session` on a session vibekit did not own, and every instance a user
 *  actually met was vibekit's own utility-bridge session: the adopted chat had no
 *  user turn to replay, so the page rendered blank and an empty chat was left
 *  behind. `resumeSession` itself stays — it is the command a future explicit
 *  "adopt a session" affordance would use, and it is still the only way a session
 *  vibekit does not own can become a chat. */
export async function openPreviousSession(
  row: ResumableSessionRow,
): Promise<"opened" | "gone" | "failed"> {
  const chatID = row.chat_id ?? "";
  if (chatID === "") {
    return "failed";
  }
  const existing = get(chatID);
  if (existing === undefined) {
    // The store holds no row for this chat, which is the ORDINARY case for the
    // thing this page exists to do: closing a tab calls removeChat, so a chat
    // closed in this browser session is gone from the store while its file
    // survives on the server. `loadList` puts it back before anything reads it.
    //
    // Required, not defensive. activateChatView returns early on a missing row
    // and loadMessages refuses to write into one, so activating first renders an
    // empty chat view and stops. Only a reconnect's own loadList hid it.
    await loadList();
  }
  // AWAITED: opening the tab is a round trip, and activateChatView is what
  // loads the transcript into the view the tab reveals. `openChatTab` already
  // activates, so the explicit call is the belt for a tab that was open and
  // active already — where the activation is a no-op and this is what refetches.
  const outcome = await openChatTab(chatID, get(chatID)?.name ?? row.title);
  if (outcome === "not-found") {
    // Retention is off and a close DELETED this conversation; the row the
    // History page listed describes a session whose chat is gone. Said here —
    // with the activation SKIPPED, or the reader lands on an empty transcript
    // over a dead active pointer — and answered as "gone" so the caller drops
    // its row. Distinct from a network failure by openTab's outcome: a failed
    // fetch must never read as "ephemeral and deleted".
    info("That conversation was ephemeral (retention is off) and is gone.");
    return "gone";
  }
  if (outcome !== "opened") {
    // The framework's error surface has already spoken; the row stays.
    return "failed";
  }
  activateChatView(chatID);
  return "opened";
}

/** Create a new chat pre-set to the "plan" workflow mode — the share-target
 *  `?agent=planner` shortcut. On v3 (KAS) "planner" is the bundled Plan mode,
 *  not a v2 agent: the mode is persisted on the empty chat and applied at
 *  session/new (StartOpts.Mode). Mirrors role-picker's selectMode.
 *
 *  AWAITS the create: `set_mode` is addressed to the chat the create returned, so
 *  detaching here would send it to whatever chat happened to be active. */
export async function createPlannerSession(): Promise<void> {
  const id = await createSession();
  if (id === "") {
    return;
  }
  void setMode.dispatch({ chatID: id, modeID: "plan" });
}

/** A chat tab's hover tooltip: its mode, then what the agent says it is doing.
 *
 *  Either half can be missing — a chat with no session yet has no mode, and the
 *  agent only declares a description while it is working — so the separator is
 *  emitted only when both are present, rather than leaving a dangling one. */
function tabTooltipFor(s: Session): string {
  const mode = s.current_mode_id === "" ? "" : labelForMode(s.current_mode_id, s.available_modes);
  const doing = s.agent_status_text ?? "";
  if (mode === "" || doing === "") {
    return mode === "" ? doing : mode;
  }
  return `${mode} · ${doing}`;
}

/** One open chat tab's row effect. Tracks THIS chat's per-entity signal (plus
 *  the session set's structure, so a row landing after its tab still paints)
 *  and the decision dock, and writes only its own row.
 *
 *  The dock read doubles as the subscription: a decision arriving or being
 *  answered on a BACKGROUND chat re-runs this row — the case the dot's "input"
 *  state exists for. */
function chatRowEffect(chatID: string): () => void {
  return effect(() => {
    const s = watchSession(chatID);
    const pendingAsk = hasPendingDecision(chatID);
    if (s === undefined) {
      return;
    }
    // ONE lookup, reused by all three writers below. They are id-keyed because
    // the DOM row is, and a chat id is no longer that id.
    const tabID = tabIdFor("chat", chatID);
    if (tabID === "") {
      return;
    }
    // Reconcile tab name with server auto-rename / agent focus title.
    renameTab(tabID, s.name);
    // Per-tab activity dot. tabStatusFor owns the precedence; the pending-ask
    // half comes from the dock's own queue rather than the store.
    setTabStatus(tabID, tabStatusFor(s, pendingAsk));
    // The tooltip carries the chat's MODE and the agent's "what I'm working
    // on", in that order. The mode half is here because the dot took the slot
    // the per-mode role glyph used to hold, and for a BACKGROUND chat that was
    // the only place a role read out at all — the mode pill and its picker are
    // active-chat only. It restores the information with no element, no width
    // and no second visual vocabulary in the 9px column. Pointer-only, so it is
    // a convenience rather than a full replacement; a second glyph on the row
    // was the alternative and it re-spends the width the dot just claimed,
    // worst on mobile where the strip is a drawer.
    setTabTooltip(tabID, tabTooltipFor(s));
  });
}

/** The previous install's teardown. installStoreSubscribers runs once at boot;
 *  a re-install (tests) tears the old wiring down first so row effects cannot
 *  stack and double-write. */
let disposeInstall: (() => void) | undefined;

/** Wire the tab strip's per-open-row effects and the context bar's
 *  active-session effect. Also mounts the chat view (idempotent).
 *  The message list is rendered via mountChatView's own effect. */
export function installStoreSubscribers(): void {
  mountChatView();
  disposeInstall?.();

  // The context bar tracks the ACTIVE session only: it re-renders on the active
  // chat's own field changes and on switches, and a background session's churn
  // never reaches it. Covers activation (setActive re-derives activeSession)
  // and every store write to the active chat — model switches included.
  const disposeContext = effect(() => {
    const active = activeSession.value;
    if (active !== undefined) {
      refreshContextUI(active);
    }
  });

  // The per-open-row effect registry, synced on the tab projection's emit:
  // row effects appear and disappear with open tabs, so a session with no open
  // tab (closed history) is subscribed to by nothing and triggers nothing.
  const rowEffects = new Map<string, () => void>();
  const disposeSync = effect(() => {
    const open = openChatRefs();
    const openSet = new Set(open);
    for (const [chatID, dispose] of rowEffects) {
      if (!openSet.has(chatID)) {
        dispose();
        rowEffects.delete(chatID);
      }
    }
    for (const chatID of open) {
      if (!rowEffects.has(chatID)) {
        rowEffects.set(chatID, chatRowEffect(chatID));
      }
    }
  });

  disposeInstall = () => {
    disposeSync();
    for (const dispose of rowEffects.values()) {
      dispose();
    }
    rowEffects.clear();
    disposeContext();
  };
}
