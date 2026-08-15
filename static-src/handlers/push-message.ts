// Service-worker push messages.
//
// The worker posts to the page in two situations, and they want opposite
// treatment:
//
//   reason "arrived"  a push landed while this page was FOCUSED, so the worker
//                     showed no OS notification. That is the one sanctioned
//                     exception to "every push must show a notification"
//                     (Chrome enforces userVisibleOnly and would otherwise
//                     substitute its own generic background notice), and the
//                     right surface when the user is already looking is an
//                     ephemeral toast, not a tray banner.
//
//   reason "clicked"  the user tapped a notification, so they asked to be taken
//                     somewhere. The worker focuses this page and hands over the
//                     chat id; the ROUTE is built here because router.ts is the
//                     single source of truth for routes and the worker compiles
//                     standalone with no imports.
//
// Sending the id rather than a URL is also why an existing tab changes route
// instead of reloading, and why a second window is never opened for a page that
// is already up.
// ---------------------------------------------------------------------------

import { openChatTab } from "../chat.js";
import { get } from "../store.js";
import * as toast from "../toast.js";

interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived";
  chatId: string;
  title: string;
  body: string;
}

function isPushMessage(d: unknown): d is PushPageMessage {
  if (typeof d !== "object" || d === null) {
    return false;
  }
  const m = d as Partial<PushPageMessage>;
  return (
    m.type === "push" &&
    (m.reason === "clicked" || m.reason === "arrived") &&
    typeof m.chatId === "string"
  );
}

/** The toast text. Title and body both come from the server, which builds them
 *  from a fixed vocabulary ("Permission needed", "Agent finished"), so this is
 *  a join rather than a formatter. */
function notice(msg: PushPageMessage): string {
  const body = msg.body.trim();
  return body === "" ? msg.title : body;
}

export function initPushMessages(): void {
  if (!("serviceWorker" in navigator)) {
    return;
  }
  navigator.serviceWorker.addEventListener("message", (event: MessageEvent) => {
    const msg: unknown = event.data;
    if (!isPushMessage(msg)) {
      return;
    }
    if (msg.reason === "clicked") {
      if (msg.chatId === "") {
        return;
      }
      // Reuse the tab if the chat is already open; openChatTab is idempotent
      // by id and activating it routes the URL through the tab store's own
      // subscriber, so no manual pushRoute is needed here.
      openChatTab(msg.chatId, get(msg.chatId)?.name ?? msg.title);
      return;
    }
    toast.info(notice(msg));
  });
}
