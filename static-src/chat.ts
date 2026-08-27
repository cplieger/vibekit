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
  getActive,
  get,
  getSessions,
  setActive,
  setModel,
  upsertHeader,
  activeSession,
  removeChat,
  tabStatusFor,
  isEmptyChat,
} from "./store.js";
import { loadList, loadMessages } from "./store-load.js";
import { effect, el } from "@cplieger/reactive";
import type { ChatHeader, Session } from "./types.js";
import { ensureBound } from "./banner-stack.js";
import {
  openTab,
  activateTab,
  tabIdFor,
  getActiveTabId,
  renameTab,
  setTabStatus,
  setTabTooltip,
  type TabDotStatus,
} from "./tabs.js";
import { hasPendingDecision, dropDecisions } from "./decision-dock.js";
import { submitPrompt } from "./submit.js";
import { chatSkeleton } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import {
  mountChatView,
  setLoadMore,
  scrollToBottom,
  resetScrollState,
  loadTurnRail,
  pointTurnRail,
  fadeInTranscript,
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
import { isRetentionEnabled } from "./retention.js";
import { createChat, deleteChat as deleteChatAction, forkChat, setMode } from "./actions/chat.js";
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
): Promise<void> {
  await openTab({
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

/** Everything closing a chat TAB does on this device, plus the one dispatch it
 *  makes when the close originated here.
 *
 *  Extracted from `openChatTab`'s own onClose, unchanged, so the tab factory can
 *  name this behaviour without importing the tab store's spec shape. `remote` is
 *  required here rather than defaulted: the default belongs at the store boundary,
 *  where a caller can legitimately omit the flag, and defaulting it twice would be
 *  two places to get the safe reading wrong. */
export function closeChatTab(id: string, { remote }: { remote: boolean }): void {
  // The draft belongs to the CHAT, not to the tab, so a pending save goes
  // out before the teardown dispatch: a close that keeps the record keeps
  // the unsent text with it, and a delete's tombstone then drops the save
  // rather than racing it.
  flushComposerDraft();
  // The chat's unanswered asks go with the tab. Closing cancels the turn and
  // the chat's runs server-side, so nothing here is still live — and the dock
  // queue is keyed by chat id, so a queue left behind was resurrected by
  // reopening the SAME id: the card came back and the tab dot said the chat
  // needed a decision that no longer existed.
  dropDecisions(id);
  // There is no archive step any more: a closed chat is just a chat that
  // stopped being in a tab, and "archived" is computed from its age
  // against the retention window. So closing a tab persists nothing.
  //
  // Retention = 0 is the one case that still acts: it means NO retention
  // (ephemeral chats, lost on close), which is the least-retention end of
  // the scale — not "keep forever". Higher N = more retention.
  // Zero-message chats were never persisted server-side, so they are
  // dropped locally either way.
  const s = get(id);
  if (isRetentionEnabled()) {
    // Closing kills the work (user decision): the turn, the chat's runs, the
    // process. That teardown is the SERVER's now — `close_tab` runs it for every
    // chat tab it closes, which is what `close_chat` became when the command was
    // retired — so nothing is dispatched from here in either direction. Two
    // commands meaning one gesture would be two things to keep in step: a client
    // sending only `close_chat` tore the bridge down and left the tab, one sending
    // only `close_tab` left the process.
    //
    // What is left is the LOCAL cleanup, and it runs whoever closed the tab: the
    // store row, the dock queue, the composer state. The record survives;
    // reopening session/loads it back.
    removeChat(id);
  } else if (s?.message_count === 0) {
    removeChat(id);
  } else if (remote) {
    // Retention off means a close DELETES, and the other device already did. Drop
    // the local row without a second delete_chat.
    removeChat(id);
  } else {
    // delete_chat is a genuinely different command from close_tab — it removes the
    // record — so this one still dispatches, and `remote` is what stops the second
    // device sending it again.
    deleteChat(id);
  }
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

/** Permanently delete a chat (retention = 0, non-empty) on tab close — the
 *  "no retention" / ephemeral mode. Optimistic via the action; the SSE
 *  chat_deleted echo is a no-op once the store row is already gone. */
function deleteChat(id: string): void {
  void deleteChatAction.dispatch(id);
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

/** Point every per-chat view at `id` and load its transcript. This is a chat
 *  tab's `onShow`, exported so the tab factory can name it without this module
 *  having to hand the factory a spec. */
export function activateChatView(id: string): void {
  // Save-then-restore against the shared composer, the same pair activateFile
  // runs against the shared editor textarea. The save MUST precede setActive:
  // it reads the outgoing chat's id, which nothing can recover afterwards.
  saveComposerState();
  const gen = ++activationGen;
  // Re-key the two per-chat view SINGLETONS on the switch itself: before the
  // repaint below, and before any branch or early return can skip it. Both used
  // to be re-keyed as a side effect of a successful message load, so every path
  // that never reaches that callback inherited the previous chat's furniture —
  // a new chat is an empty chat and loads nothing, a failed load returns early,
  // a purged record returns earlier still, and even a healthy switch left the
  // rail describing the old chat until its own fetch landed.
  //
  // Pointing the rail is the whole update here, because the rail spans the
  // SESSION: an empty chat has no turns to fetch, and a brand-new chat's id
  // exists nowhere but the tab that minted it, so asking for its turns is a
  // guaranteed 404. The loaded branch fetches for itself below.
  pointTurnRail(id);
  resetScrollState();
  setActive(id);
  restoreComposerState(id);
  ensureBound();
  const session = get(id);
  if (session === undefined) {
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
    // predicate (picker.ts bindVisibility). Only the draft is this branch's.
    seedEmptyChatDraft(id);
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
            $.messages.appendChild(skel);
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
        // Show retry button on failed load.
        const retry = el(
          "div",
          { className: "load-error" },
          el("span", {}, "Failed to load messages."),
        );
        const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
        btn.addEventListener("click", () => {
          retry.remove();
          activateChatView(id);
        });
        retry.appendChild(btn);
        $.messages.appendChild(retry);
        return;
      }
      // The turns that replace the placeholder fade in rather than cutting.
      // loadMessages' own emitMessages paints synchronously, so they are already
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
        scrollToBottom();
      }
      // The rail's index is session-wide and independent of the message window,
      // so it is its own fetch rather than something derived from what loaded.
      void loadTurnRail(id);
    });
  }
  refreshContextUI(session);
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
            // removal as the "load complete" signal (was previously done
            // inside the imperative prependMessages helper).
            document.getElementById("load-more-skeleton")?.remove();
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
  // gesture. See transport.newOpID.
  const header = await createChat.dispatch({ opID: newOpID(), model });
  if (header === null) {
    // The action framework has already raised its toast. Nothing is seeded and no
    // tab opens, which is the honest surface: there is no chat.
    return "";
  }
  seedChat(header);
  const id = header.id;
  setActive(id);
  // The composer belongs to THIS chat from here, not from the activation below.
  // `openChatTab` is a server round trip and the activation that retargets the
  // composer is its `onShow`, so without this the box still belonged to the chat
  // the user just left for the length of that trip — and anything typed into it
  // was filed under that chat and flushed to the server as its draft.
  retargetComposer(id);
  // AWAITED: the tab is a round trip now, and every caller of createSession
  // addresses the chat it produced — a detached open would let a caller push a
  // route or attach a file before the row exists.
  //
  // `create_chat` already opened this chat's tab server-side (the coordinator
  // writes the record and opens the tab under one lock), so this open answers
  // `created: false` and resolves as soon as that frame lands. It is not
  // redundant: it is what ACTIVATES the new chat, which is the whole point of
  // pressing New chat.
  await openChatTab(id, header.name === "" ? NEW_CHAT_NAME : header.name);

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
  const header = await forkChat.dispatch({ opID: newOpID(), parentChatID });
  if (header === null) {
    return;
  }
  seedChat(header);
  const id = header.id;
  setActive(id);
  // Same round-trip window as createSession's: the tangent is the active chat
  // now, so the composer has to be its own before the await, not after.
  retargetComposer(id);
  // The parent is resolved to its TAB, which is what a subject's `Parent` names.
  // A parent whose tab is not open here promotes the tangent to top level, the
  // same rule the strip applies to an orphan.
  await openChatTab(id, header.name === "" ? NEW_CHAT_NAME : header.name, {
    parentTabID: tabIdFor("chat", parentChatID),
  });
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
export function openPreviousSession(row: ResumableSessionRow): void {
  const chatID = row.chat_id ?? "";
  if (chatID === "") {
    return;
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
    void loadList().then(async () => {
      // AWAITED: opening the tab is a round trip, and activateChatView is what
      // loads the transcript into the view the tab reveals. `openChatTab` already
      // activates, so the explicit call is the belt for a tab that was open and
      // active already — where the activation is a no-op and this is what refetches.
      await openChatTab(chatID, get(chatID)?.name ?? row.title);
      activateChatView(chatID);
    });
    return;
  }
  void openChatTab(chatID, existing.name).then(() => {
    activateChatView(chatID);
  });
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

/** Wire the effect() that drives tab-rename reconciliation and
 *  send-button state. Also mounts the chat view (idempotent).
 *  The message list is rendered via mountChatView's own effect. */
export function installStoreSubscribers(): void {
  mountChatView();
  effect(() => {
    void activeSession.value;
    const active = getActive();
    if (active !== undefined) {
      refreshContextUI(active);
    }
    // getSessions() reads every session's per-entity signal, so this effect
    // re-runs on ANY session field change (name, thinking, current_mode_id) —
    // background chats included. That keeps a background tab's activity dot and
    // mode icon current without needing an active-session event.
    for (const s of getSessions()) {
      // ONE lookup per chat, reused by all three writers below. They are id-keyed
      // because the DOM row is, and a chat id is no longer that id.
      const tabID = tabIdFor("chat", s.id);
      if (tabID !== "") {
        // Reconcile tab name with server auto-rename / agent focus title.
        renameTab(tabID, s.name);
        // Per-tab activity dot. tabStatusFor owns the precedence; the pending-ask
        // half comes from the dock's own queue rather than the store, and reading
        // it here is also what subscribes this effect to a decision arriving or
        // being answered on a BACKGROUND chat — the case the feature exists for.
        setTabStatus(tabID, tabStatusFor(s, hasPendingDecision(s.id)));
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
      }
    }
  });
}
