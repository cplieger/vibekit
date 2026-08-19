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
import { openChangeSet } from "../navigate.js";
import * as toast from "../toast.js";

interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived";
  chatId: string;
  /** The notification's subject when it has no chat behind it — a pull request's
   *  CI flip. Carries a kind prefix so the route below is keyed on what the subject
   *  IS rather than on a URL the server would have had to assemble. */
  subject?: string;
  title: string;
  body: string;
}

/** Subject-key prefix for a pull request (vibekit.PRSubjectPrefix, and sw.ts's own
 *  copy). Asserted against the Go constant by push-subject.test.ts. */
const PR_SUBJECT_PREFIX = "pr:";

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

/** Where a clicked notification goes.
 *
 *  A PR subject opens the git view, which is where its pull requests are; the
 *  route is built here rather than in the worker because router.ts is the single
 *  source of truth for routes and the worker compiles standalone with no imports.
 *  Exported for its test: this is the whole of D83's reuse for the new kind. */
export function routePushMessage(msg: PushPageMessage): void {
  if ((msg.subject ?? "").startsWith(PR_SUBJECT_PREFIX)) {
    // The git view is where pull requests live, and openChangeSet is the router's
    // own door to it — so the notification lands on the same surface a transcript
    // click would, rather than on a second one built for this.
    openChangeSet();
    return;
  }
  if (msg.chatId === "") {
    return;
  }
  // Reuse the tab if the chat is already open; openChatTab is idempotent
  // by id and activating it routes the URL through the tab store's own
  // subscriber, so no manual pushRoute is needed here.
  openChatTab(msg.chatId, get(msg.chatId)?.name ?? msg.title);
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
      routePushMessage(msg);
      return;
    }
    toast.info(notice(msg));
  });
}
