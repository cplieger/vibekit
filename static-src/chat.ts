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
  contextSizeFor,
  defaultUsage,
  activeSession,
  removeChat,
  tabStatusFor,
  clearTurnDone,
  markGhostChat,
  isGhostChat,
} from "./store.js";
import { loadList, loadMessages } from "./store-load.js";
import { effect, el } from "@cplieger/reactive";
import type { Session } from "./types.js";
import { ensureBound } from "./banner-stack.js";
import {
  openTab,
  activateTab,
  hasTab,
  getActiveTabId,
  renameTab,
  setTabStatus,
  setTabTooltip,
  TAB_VIEWS,
} from "./tabs.js";
import { hasPendingDecision, dropDecisions } from "./decision-dock.js";
import { submitPrompt } from "./submit.js";
import { chatSkeleton } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { showModelPicker, hideModelPicker } from "./picker.js";
import { mountChatView, setLoadMore, scrollToBottom, loadTurnRail } from "./messages.js";
import { addAttachment } from "./attachments.js";
import {
  saveComposerState,
  restoreComposerState,
  seedComposerDraft,
  flushComposerDraft,
  dropComposerState,
} from "./composer-state.js";
import { setCurrentModel, getLastModel } from "./session-context.js";
import { labelForMode } from "./roles.js";
import { applyLocalModel } from "./model-switcher.js";
import { refreshContextUI } from "./context-ui.js";
import { $ } from "./dom.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { isRetentionEnabled } from "./retention.js";
import {
  closeChat as closeChatAction,
  deleteChat as deleteChatAction,
  forkChat,
  resumeSession,
  setMode,
} from "./actions/chat.js";

// --- Bus: activate chat from other modules without importing chat.ts ---

onBus(BUS_ACTIVATE_CHAT, (p) => {
  activateChatView(p.chatID);
  if (p.then !== undefined) {
    p.then();
  }
});

// --- Chat tab registration ---

/** Open (or activate) a chat tab. Pass `{ activate: false }` for the boot
 *  restore loop: activation runs activateChatView (messages fetch +
 *  conflicts prefetch), so bulk-opening N chats active would fan out 2N
 *  requests at boot (B8) — the boot path activates exactly one tab at
 *  the end instead. */
export function openChatTab(
  id: string,
  name: string,
  opts?: { activate?: boolean; parentId?: string },
): void {
  // The dot's initial state, derived HERE because this is where the chat store is
  // in scope. Without it a restored tab painted the `idle` floor and stayed there:
  // the boot path loads the session list BEFORE opening its tabs, so the store
  // effect that normally paints has already run by the time the rows exist and
  // nothing makes it run again. `""` means this client holds no row for the id
  // yet, which is every brand-new chat, and createTabEl's own `idle` floor is the
  // right answer for it.
  const dot = tabStatusFor(get(id), hasPendingDecision(id));
  openTab(
    {
      id,
      name,
      kind: "chat",
      // A side conversation hangs off the chat it came from. `owns` stays at its
      // default (true) deliberately: the side chat owns its own bridge, so closing
      // its tab must tear that bridge down the way any chat tab does. `owns:
      // false` is for a tab that only WATCHES work another chat owns.
      ...(opts?.parentId === undefined ? {} : { parentId: opts.parentId }),
      ...(dot === "" ? {} : { dotStatus: dot }),
      view: TAB_VIEWS.chat,
      route: { kind: "chat", id },
      onShow: () => {
        activateChatView(id);
      },
      onClose: () => {
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
          // Closing kills the work (user decision): the turn, the chat's runs,
          // the process — server-side, before the local row goes. The record
          // survives; reopening session/loads it back.
          void closeChatAction.dispatch(id);
          removeChat(id);
        } else if (s?.message_count === 0) {
          removeChat(id);
        } else {
          // delete_chat implies the same teardown server-side.
          deleteChat(id);
        }
        // Local only: a close that kept the record kept its draft with it, and
        // reopening the chat seeds the text back from the server.
        dropComposerState(id);
      },
    },
    opts,
  );
}

/** Permanently delete a chat (retention = 0, non-empty) on tab close — the
 *  "no retention" / ephemeral mode. Optimistic via the action; the SSE
 *  chat_deleted echo is a no-op once the store row is already gone. */
function deleteChat(id: string): void {
  void deleteChatAction.dispatch(id);
}

function activateChatView(id: string): void {
  // Save-then-restore against the shared composer, the same pair activateFile
  // runs against the shared editor textarea. The save MUST precede setActive:
  // it reads the outgoing chat's id, which nothing can recover afterwards.
  saveComposerState();
  // Opening the chat is what settles a `done` dot: the mark means "this finished
  // while you were away", so arriving is the event that answers it. The store
  // effect below re-derives the dot off this write, so no explicit repaint.
  clearTurnDone(id);
  setActive(id);
  hideModelPicker();
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

  if (session.message_count === 0 && session.messages.length === 0) {
    showModelPicker(session.model, applyLocalModel);
    seedEmptyChatDraft(id);
  } else {
    // Hydrate messages from the server, then render. Defer the skeleton by
    // 150ms so a fast (cached) open never flashes it: skeletonTiming appends
    // it only if the load is still running at 150ms, and the cancel() call on
    // completion clears the pending timer and removes it if it was shown.
    // (min-visible stays 0 — the skeleton shares the messages container.)
    const skeleton = skeletonTiming(() => {
      const skel = chatSkeleton();
      $.messages.appendChild(skel);
      return () => {
        skel.remove();
      };
    });
    void loadMessages(id).then((ok) => {
      skeleton.cancel();
      if (getActiveId() !== id) {
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
      // The chat's record is in now, so its stored draft can be adopted. Only
      // matters when this device holds none for the chat — after a reload — and
      // it deliberately loses to a draft the user has started typing since the
      // activation.
      seedComposerDraft(id);
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
 *  record is auto-created by `set_mode` or `set_effort` before the first prompt,
 *  so the user can pick a mode, type half a message, reload, and come back to a
 *  persisted zero-message chat whose draft the server is holding. Without this
 *  the box came back empty, which defeats the reason the draft is server-side at
 *  all.
 *
 *  A GHOST chat is skipped: its id exists nowhere but this tab, so the GET would
 *  404 on every New chat click. The re-check after the await is the same guard
 *  the loaded branch uses — the user can switch chats while it is in flight, and
 *  the seed writes into the shared composer. */
function seedEmptyChatDraft(id: string): void {
  if (isGhostChat(id)) {
    return;
  }
  void loadMessages(id).then((ok) => {
    if (ok && getActiveId() === id) {
      seedComposerDraft(id);
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

export function sendPrompt(text: string): void {
  hideModelPicker();
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  void submitPrompt(chatID, text);
}

// --- Session lifecycle ---

const NEW_CHAT_NAME = "New conversation";

/** Mint a client-side chat id and seed its store header.
 *
 *  Every chat id is client-generated; the chat becomes a SERVER record on its
 *  first prompt, which is also where its name comes from (the 80-char truncation
 *  in command/prompt.go). Nothing here is persisted, so there is no create_chat
 *  round trip to make. */
function seedLocalChat(model: string): string {
  const id = `c-${String(Date.now())}-${Math.random().toString(36).slice(2, 8)}`;
  // Template for upsertHeader + initial render; only the header fields
  // matter — the rest are defaults satisfying the Session type.
  const session: Session = {
    id,
    name: NEW_CHAT_NAME,
    model,
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: defaultUsage(),
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
  session.usage.context_size = contextSizeFor(model);
  upsertHeader({
    id: session.id,
    name: session.name,
    model: session.model,
    acp_session_id: "",
    usage: session.usage,
    created_at: Date.now(),
    updated_at: Date.now(),
    message_count: 0,
  });
  // Nothing on the server answers to this id yet, so nothing should ask it about
  // the chat. The first frame naming it clears the mark (store.upsertHeader).
  markGhostChat(id);
  return id;
}

/** Create a new chat. If initialPrompt is non-empty, send it immediately.
 *  Otherwise open the model picker and wait for the user to type. */
export function createSession(initialPrompt?: string): void {
  const model = getLastModel();
  const id = seedLocalChat(model);
  setActive(id);
  openChatTab(id, NEW_CHAT_NAME);

  if (initialPrompt !== undefined && initialPrompt !== "") {
    setCurrentModel(model);
    sendPrompt(initialPrompt);
  } else {
    showModelPicker(model, applyLocalModel);
  }
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
 *    - A SUB-TAB of its parent, and promotable out of that with `promoteTab`
 *      (the tab context menu's "Promote to its own tab"), which clears
 *      `parentId`. Until then the parent's close cascade takes it with it.
 *    - It inherits model, mode and effort from the parent — read SERVER-side off
 *      the parent's record, because the record is the truth about all three and a
 *      tab's projection can be stale. Nothing is dispatched from here for them.
 *    - Nothing seeds a first prompt. There is no selection to seed one from: with
 *      the whole conversation inherited, a selected phrase chooses nothing.
 *
 *  The name is left to the ordinary precedence (the agent's focus title, else the
 *  first prompt's truncation), which is why nothing here names it. */
export function openTangentChat(parentChatID: string): void {
  if (parentChatID === "" || get(parentChatID) === undefined) {
    return;
  }
  const model = get(parentChatID)?.model ?? getLastModel();
  const id = seedLocalChat(model);
  setActive(id);
  openChatTab(id, NEW_CHAT_NAME, { parentId: parentChatID });
  setCurrentModel(model);
  void forkChat.dispatch({ chatID: id, parentChatID });
}

export function switchSession(id: string): void {
  if (id === getActiveId() && getActiveTabId() === id) {
    return;
  }
  activateTab(id);
}

/** Attach a workspace file to the active chat's next prompt. Shows as
 *  a removable pill below the textarea. The user still types their
 *  message; the attachment is sent alongside on submit. Switches to the
 *  chat tab if needed so the user sees where their attachment went. */
export function attachPathToActiveChat(path: string): void {
  if (getActiveId() === "") {
    createSession();
  }
  const id = getActiveId();
  if (id !== "" && getActiveTabId() !== id) {
    activateTab(id);
  }
  addAttachment(path);
  $.promptInput.focus();
}

/** Open a session the previous-session picker listed.
 *
 *  Two paths, and the difference is whether vibekit already knows the session:
 *
 *    - `chat_id` set: a chat already owns it (possibly as a RETIRED id in its
 *      chain), so this is just opening that chat. Adopting it again would
 *      create a second chat pointing at one session.
 *    - no `chat_id`: the session is KAS's alone — started from the TUI, or its
 *      chat was deleted while retention kept the session. Adopt it as a new
 *      chat bound to that session id; the transcript then arrives from the
 *      session/load replay, so nothing is copied here.
 *
 *  The adopting client opens and activates the tab itself: the server
 *  broadcasts a generic chat_created to every client, and none of the others
 *  should auto-open a tab. */
export function openPreviousSession(row: ResumableSessionRow): void {
  if (row.chat_id !== undefined && row.chat_id !== "") {
    const existing = get(row.chat_id);
    openChatTab(row.chat_id, existing?.name ?? row.title);
    activateChatView(row.chat_id);
    return;
  }
  const chatID = `c-${String(Date.now())}-${Math.random().toString(36).slice(2, 8)}`;
  void resumeSession.dispatch(
    { chatID, sessionID: row.session_id, name: row.title },
    {
      onSuccess: () => {
        void loadList().then(() => {
          const s = get(chatID);
          openChatTab(chatID, s?.name ?? row.title);
          activateChatView(chatID);
        });
      },
    },
  );
}

/** Create a new chat pre-set to the "plan" workflow mode — the share-target
 *  `?agent=planner` shortcut. On v3 (KAS) "planner" is the bundled Plan mode,
 *  not a v2 agent: the mode is persisted on the empty chat and applied at
 *  session/new (StartOpts.Mode). Mirrors role-picker's selectMode. */
export function createPlannerSession(): void {
  createSession();
  const id = getActiveId();
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
      if (hasTab(s.id)) {
        // Reconcile tab name with server auto-rename / agent focus title.
        renameTab(s.id, s.name);
        // Per-tab activity dot. tabStatusFor owns the precedence; the pending-ask
        // half comes from the dock's own queue rather than the store, and reading
        // it here is also what subscribes this effect to a decision arriving or
        // being answered on a BACKGROUND chat — the case the feature exists for.
        setTabStatus(s.id, tabStatusFor(s, hasPendingDecision(s.id)));
        // The tooltip carries the chat's MODE and the agent's "what I'm working
        // on", in that order. The mode half is here because the dot took the slot
        // the per-mode role glyph used to hold, and for a BACKGROUND chat that was
        // the only place a role read out at all — the mode pill and its picker are
        // active-chat only. It restores the information with no element, no width
        // and no second visual vocabulary in the 9px column. Pointer-only, so it is
        // a convenience rather than a full replacement; a second glyph on the row
        // was the alternative and it re-spends the width the dot just claimed,
        // worst on mobile where the strip is a drawer.
        setTabTooltip(s.id, tabTooltipFor(s));
      }
    }
  });
}
