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
  setTabIcon,
  TAB_VIEWS,
} from "./tabs.js";
import { submitPrompt } from "./submit.js";
import { chatSkeleton } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { showModelPicker, hideModelPicker } from "./picker.js";
import { mountChatView, setLoadMore, scrollToBottom, loadTurnRail } from "./messages.js";
import { addAttachment, clearAttachments } from "./attachments.js";
import { setCurrentModel, getLastModel } from "./session-context.js";
import { applyLocalModel } from "./model-switcher.js";
import { refreshContextUI } from "./context-ui.js";
import { iconForMode } from "./roles.js";
import { $ } from "./dom.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { isRetentionEnabled } from "./retention.js";
import {
  closeChat as closeChatAction,
  deleteChat as deleteChatAction,
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
export function openChatTab(id: string, name: string, opts?: { activate?: boolean }): void {
  openTab(
    {
      id,
      name,
      kind: "chat",
      // Derive the tab icon from the chat's current mode so a reloaded or
      // restored chat shows the right mode glyph; installStoreSubscribers keeps
      // it in sync on later (user- or agent-initiated) mode changes.
      iconSvg: iconForMode(get(id)?.current_mode_id ?? ""),
      view: TAB_VIEWS.chat,
      route: { kind: "chat", id },
      onShow: () => {
        activateChatView(id);
      },
      onClose: () => {
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
  setActive(id);
  hideModelPicker();
  clearAttachments();
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

/** Create a new chat. If initialPrompt is non-empty, send it immediately.
 *  Otherwise open the model picker and wait for the user to type. */
export function createSession(initialPrompt?: string): void {
  const id = `c-${String(Date.now())}-${Math.random().toString(36).slice(2, 8)}`;
  const model = getLastModel();
  // Template for upsertHeader + initial render; only the header fields
  // matter — the rest are defaults satisfying the Session type.
  const session: Session = {
    id,
    name: "New conversation",
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
  setActive(id);
  openChatTab(id, session.name);

  if (initialPrompt !== undefined && initialPrompt !== "") {
    setCurrentModel(model);
    sendPrompt(initialPrompt);
  } else {
    showModelPicker(model, applyLocalModel);
  }
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
  setTabIcon(id, iconForMode("plan"));
  void setMode.dispatch({ chatID: id, modeID: "plan" });
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
        // Per-tab activity dot: thinking while a turn runs, amber waiting
        // when the agent declared waiting_on_user (turn_ended re-derives
        // via the same tabStatusFor rule in handlers/turn.ts).
        setTabStatus(s.id, tabStatusFor(s));
        // Agent-declared "what I'm working on" as the tab tooltip.
        setTabTooltip(s.id, s.agent_status_text ?? "");
        // Tab icon derived from the chat's current mode, so a mode change
        // (user- or agent-initiated) or a reload shows the right glyph.
        setTabIcon(s.id, iconForMode(s.current_mode_id));
      }
    }
  });
}
