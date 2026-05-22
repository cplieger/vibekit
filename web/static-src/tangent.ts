// ---------------------------------------------------------------------------
// Tangent mode: fork a side conversation from the current chat.
//
// Parent chat is frozen (no input). Tangent appears as a sub-item in
// the sidebar. Two resolution paths: "Keep & return" (merge last Q&A
// pair to parent) or "Discard" (delete tangent, unfreeze parent).
// ---------------------------------------------------------------------------

import { getActive, get, loadList, version } from "./store.js";
import { effect } from "./signals.js";
import * as transport from "./transport.js";
import { openChatTab, activateChatView } from "./chat.js";
import { confirm } from "./confirm.js";

/** Wire the fork pill in the chat prompt row. The pill lives in the
 *  per-conversation pill cluster alongside Attach / Follow / Autopilot
 *  — it only acts on the active chat, so universal toolbar placement
 *  was wrong. */
export function initTangent(): void {
  const btn = document.getElementById("fork-pill");
  if (btn === null) return;

  btn.addEventListener("click", () => {
    const session = getActive();
    if (session === undefined) return;
    if (session.frozen === true) return; // already has a tangent
    forkCurrentChat(session.id);
  });

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
  const tangentID = `tangent-${Date.now().toString(36)}`;
  void transport.send({
    type: "fork_chat",
    chat_id: session.id,
    payload: { tangent_id: tangentID },
  }).then((result) => {
    if (!result.ok) return;
    void loadList().then(() => {
      openChatTab(tangentID, `Tangent: ${session.name}`, session.agent);
      activateChatView(tangentID);
    });
  });
}

/** Send merge_tangent command for the active tangent chat. */
export function mergeTangent(): void {
  const session = getActive();
  if (session === undefined || session.is_tangent !== true) return;
  void transport.send({
    type: "merge_tangent",
    chat_id: session.id,
  }).then((result) => {
    if (!result.ok) return;
    // Switch to the parent chat.
    if (session.parent_chat_id !== undefined && session.parent_chat_id !== "") {
      void loadList().then(() => {
        activateChatView(session.parent_chat_id!);
      });
    }
  });
}

/** Send discard_tangent command for the active tangent chat. */
export async function discardTangent(): Promise<void> {
  const session = getActive();
  if (session === undefined || session.is_tangent !== true) return;
  const ok = await confirm("Discard this tangent? Changes won't be merged back.", "Discard", "destructive");
  if (!ok) return;
  void transport.send({
    type: "discard_tangent",
    chat_id: session.id,
  }).then((result) => {
    if (!result.ok) return;
    if (session.parent_chat_id !== undefined && session.parent_chat_id !== "") {
      void loadList().then(() => {
        activateChatView(session.parent_chat_id!);
      });
    }
  });
}
