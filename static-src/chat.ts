// ---------------------------------------------------------------------------
// Chat lifecycle: create, activate, delete, switch, send prompt. Also owns
// the chat-tab registration (openChatTab) and load-more pagination.
//
// The store is the single source of truth; this module just wires user
// actions to server commands and renders the active session.
// ---------------------------------------------------------------------------

import {
  getActiveId,
  getActive,
  get,
  getSessions,
  setActive,
  upsertHeader,
  contextSizeFor,
  defaultUsage,
  activeSession,
  removeChat,
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
  setTabIcon,
  TAB_VIEWS,
} from "./tabs.js";
import { submitPrompt } from "./prompt-queue.js";
import { chatSkeleton } from "./skeleton.js";
import { deferSkeleton } from "./skeleton-timing.js";
import { showModelPicker, hideModelPicker } from "./picker.js";
import { mountChatView, setLoadMore, scrollToBottom } from "./messages.js";
import { addAttachment, clearAttachments } from "./attachments.js";
import { setCurrentModel, getLastModel } from "./session-context.js";
import { applyLocalModel } from "./model-switcher.js";
import { refreshContextUI } from "./context-ui.js";
import { iconForMode } from "./roles.js";
import { $ } from "./dom.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { isRetentionEnabled } from "./retention.js";
import {
  archiveChat as archiveChatAction,
  deleteChat as deleteChatAction,
  restoreChat,
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

export function openChatTab(id: string, name: string): void {
  openTab({
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
      // Retention > 0: archive so the chat appears in History (the server
      // purges it after N days). Retention = 0 (OFF): delete permanently —
      // this is intentional "no retention" mode: 0 is the least-retention end
      // of the scale (ephemeral chats, lost on close, History button hidden),
      // NOT "keep forever" — higher N = more retention. Zero-message chats
      // were never persisted server-side, so just drop them locally.
      const s = get(id);
      if (isRetentionEnabled()) {
        archiveChat(id);
      } else if (s?.message_count === 0) {
        removeChat(id);
      } else {
        deleteChat(id);
      }
    },
  });
}

/** Archive a chat (move to archive dir). Used on tab close instead of
 *  delete so the chat appears in History for the retention window.
 *  Zero-message chats (freshly created, never used) are removed locally
 *  without hitting the server — the server has no persisted session to
 *  archive and would 500. */
function archiveChat(id: string): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  if (s.message_count === 0) {
    removeChat(id);
    return;
  }
  void archiveChatAction.dispatch(id);
  // Optimistic: store is updated immediately by the action's optimistic().
  // SSE chat_deleted will fire later but removeChat is a no-op (already gone).
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
  // Prefetch conflict records so badges on historical tool calls
  // are present as soon as the messages render. Dynamic import
  // keeps the conflicts module lazy — most chats never see one.
  void import("./conflicts.js")
    .then((m) => m.loadConflictsFor(id))
    .catch(() => {
      /* noop */
    });

  const session = get(id);
  if (session === undefined) {
    return;
  }

  if (session.usage.context_size === 0 && session.model !== "") {
    session.usage.context_size = contextSizeFor(session.model);
  }

  if (session.message_count === 0 && session.messages.length === 0) {
    showModelPicker(session.model, applyLocalModel);
  } else {
    // Hydrate messages from the server, then render. Defer the skeleton by
    // 150ms so a fast (cached) open never flashes it: deferSkeleton appends it
    // only if the load is still running at 150ms, and the cancel() call on
    // completion clears the pending timer and removes it if it was shown.
    const cancelSkeleton = deferSkeleton(() => {
      const skel = chatSkeleton();
      $.messages.appendChild(skel);
      return () => {
        skel.remove();
      };
    });
    void loadMessages(id).then((ok) => {
      cancelSkeleton();
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
          void loadMessages(chatID, oldest.ts).then(() => {
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
    pending_changes: [],
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

/** Restore an archived chat. Opens a tab and activates it after the
 *  sidebar store catches up, matching the tangent-fork pattern — the
 *  server broadcasts chat_created, but the SSE handler only updates
 *  the store (a generic chat_created is also emitted to every other
 *  connected client, none of which should auto-open tabs). The
 *  restoring client explicitly opens + activates so the user lands
 *  on the conversation they just resurrected instead of having to
 *  find it in the sidebar. */
export function restoreArchivedChat(id: string): void {
  void restoreChat.dispatch(id, {
    onSuccess: (d) => {
      if (!d.ok) {
        return;
      }
      void loadList().then(() => {
        const s = get(id);
        if (s === undefined) {
          return;
        }
        openChatTab(s.id, s.name);
        activateChatView(s.id);
      });
    },
  });
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
        // Reconcile tab name with server auto-rename.
        renameTab(s.id, s.name);
        // Per-tab "thinking" activity dot, driven by the per-chat thinking
        // signal (turn_ended also clears it explicitly in handlers/turn.ts).
        setTabStatus(s.id, s.thinking ? "thinking" : "");
        // Tab icon derived from the chat's current mode, so a mode change
        // (user- or agent-initiated) or a reload shows the right glyph.
        setTabIcon(s.id, iconForMode(s.current_mode_id));
      }
    }
  });
}
