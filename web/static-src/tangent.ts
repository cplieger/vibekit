// ---------------------------------------------------------------------------
// Tangent mode: fork a side conversation from the current chat.
//
// Parent chat is frozen (no input). Tangent appears as a sub-item in
// the sidebar. Two resolution paths: "Keep & return" (merge last Q&A
// pair to parent) or "Discard" (delete tangent, unfreeze parent).
// ---------------------------------------------------------------------------

import { getActive, get, loadList, version } from "./store.js";
import { effect } from "./signals.js";
import { forkChatAction, mergeTangentAction, discardTangentAction } from "./actions/chat.js";
import { bindLoadingState } from "./actions/index.js";
import { openChatTab, activateChatView } from "./chat.js";
import { confirm } from "./confirm.js";
import { error as toastError } from "./toast.js";

/** Wire the fork pill in the chat prompt row. The pill lives in the
 *  per-conversation pill cluster alongside Attach / Follow / Autopilot
 *  — it only acts on the active chat, so universal toolbar placement
 *  was wrong. */
export function initTangent(): void {
  const btn = document.getElementById("fork-pill") as HTMLButtonElement | null;
  if (btn === null) return;

  btn.addEventListener("click", () => {
    const session = getActive();
    if (session === undefined) return;
    if (session.frozen === true) return; // already has a tangent
    forkCurrentChat(session.id);
  });
  bindLoadingState("chat.fork", btn);

  // Show/hide fork button based on active session state.
  effect(() => {
    version.value;
    const session = getActive();
    if (session === undefined) {
      btn.classList.add("hidden");
      return;
    }
    // Hide if frozen (parent of active tangent) or if this is a tangent.
    const hide = session.frozen === true || session.is_tangent === true;
    btn.classList.toggle("hidden", hide);

    // Frozen state: dim the prompt form.
    const form = document.getElementById("prompt-form");
    if (form !== null) {
      form.classList.toggle("frozen", session.frozen === true);
    }
  });
}

/** Imperative fork entry-point. Called by the fork pill click handler
 *  and by app.ts so both wire through the same code path. */
export function forkCurrentChat(chatID: string): void {
  const session = get(chatID);
  if (session === undefined) return;
  if (session.frozen === true) return;
  const tangentID = `tangent-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
  void forkChatAction.dispatch({ chatID: session.id, tangentID }, {
    onSuccess: () => {
      void loadList().then(() => {
        // Read fresh session state — name/agent may have changed while
        // the fork was scope-queued behind another chat mutation.
        const fresh = get(chatID);
        const name = fresh?.name ?? session.name;
        const agent = fresh?.agent ?? session.agent;
        openChatTab(tangentID, `Tangent: ${name}`, agent);
        activateChatView(tangentID);
      });
    },
    onError: () => {
      // Server may have accepted the fork but the response was lost.
      // Refresh the chat list to detect server-side state mismatch.
      console.warn("[tangent] fork dispatch failed — refreshing list to detect server-side state");
      void loadList();
    },
  });
}

/** Send merge_tangent command for the active tangent chat. */
export function mergeTangent(): void {
  const session = getActive();
  if (session === undefined || session.is_tangent !== true) return;
  const parentID = session.parent_chat_id;
  void mergeTangentAction.dispatch(session.id, {
    onSuccess: () => {
      if (parentID !== undefined && parentID !== "") {
        void loadList().then(() => {
          activateChatView(parentID);
        });
      }
    },
  });
}

/** Send discard_tangent command for the active tangent chat. */
export async function discardTangent(): Promise<void> {
  const session = getActive();
  if (session === undefined || session.is_tangent !== true) return;
  const ok = await confirm("Discard this tangent? Changes won't be merged back.", "Discard", "destructive");
  if (!ok) return;
  const result = await discardTangentAction.dispatch(session.id);
  if (result === null) {
    toastError("Couldn't discard tangent");
    return;
  }
  if (session.parent_chat_id !== undefined && session.parent_chat_id !== "") {
    void loadList().then(() => {
      activateChatView(session.parent_chat_id!);
    });
  }
}
