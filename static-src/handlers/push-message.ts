// Service-worker push messages.
//
// The worker posts to the page in two situations wanting opposite treatment:
//
//   "arrived"  a push landed while this page was focused, so the worker
//              showed no OS notification (the sanctioned exception to
//              "every push must show a notification" — Chrome's
//              userVisibleOnly would otherwise substitute a generic
//              background notice). Right surface: an ephemeral toast.
//
//   "clicked"  the user tapped a notification. The worker focuses this page
//              and hands over the chat id; the route is built here because
//              router.ts is the single source of truth and the worker
//              compiles standalone with no imports.
//
// Sending the id rather than a URL is also why an existing tab changes
// route instead of reloading.
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

/** Where a clicked notification goes. A PR subject opens the git view;
 *  the route is built here because router.ts is the single source of
 *  truth and the worker compiles standalone with no imports. */
export function routePushMessage(msg: PushPageMessage): void {
  if ((msg.subject ?? "").startsWith(PR_SUBJECT_PREFIX)) {
    openChangeSet();
    return;
  }
  if (msg.chatId === "") {
    return;
  }
  // openChatTab is idempotent by id; activating routes the URL through
  // the tab store's own subscriber, so no manual pushRoute is needed.
  void openChatTab(msg.chatId, get(msg.chatId)?.name ?? msg.title);
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
