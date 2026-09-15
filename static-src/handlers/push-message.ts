// Service-worker push messages, in three kinds wanting different treatment.
//
//   "arrived"  a push landed while this page was focused, so the worker showed no
//              OS notification (the sanctioned exception to "every push must show
//              one" — Chrome's userVisibleOnly would substitute a generic
//              background notice). Right surface: an ephemeral toast.
//   "clicked"  the user tapped one. The subject becomes a route through
//              push-subject.ts, the same one the worker spends, and
//              notification-open.ts hands it to the route applier.
//   "subscription_changed"  the browser rotated the push subscription, so the
//              presence tag derived from its endpoint moved; the page re-derives.
// ---------------------------------------------------------------------------

import { openPushTarget } from "../notification-open.js";
import { parsePushTarget } from "../push-subject.js";
import * as toast from "../toast.js";

interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived" | "subscription_changed";
  chatId: string;
  /** The notification's subject when it has no chat behind it — a pull request's
   *  CI flip. Carries a kind prefix so the route below is keyed on what the subject
   *  IS rather than on a URL the server would have had to assemble. */
  subject?: string;
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
    (m.reason === "clicked" || m.reason === "arrived" || m.reason === "subscription_changed") &&
    typeof m.chatId === "string"
  );
}

/** Where a clicked notification goes. */
export function routePushMessage(msg: PushPageMessage): void {
  openPushTarget(parsePushTarget({ chatId: msg.chatId, subject: msg.subject ?? "" }));
}

/** The toast text. Title and body both come from the server, which builds them
 *  from a fixed vocabulary ("Permission needed", "Agent finished"), so this is
 *  a join rather than a formatter. */
function notice(msg: PushPageMessage): string {
  const body = msg.body.trim();
  return body === "" ? msg.title : body;
}

/** `onSubscriptionChanged` runs when the worker reports a rotated subscription; the
 *  caller owns the tag re-derivation (app.ts adoptPushTag). */
export function initPushMessages(onSubscriptionChanged: () => void): void {
  if (!("serviceWorker" in navigator)) {
    return;
  }
  navigator.serviceWorker.addEventListener("message", (event: MessageEvent) => {
    const msg: unknown = event.data;
    if (!isPushMessage(msg)) {
      return;
    }
    if (msg.reason === "subscription_changed") {
      onSubscriptionChanged();
      return;
    }
    if (msg.reason === "clicked") {
      routePushMessage(msg);
      return;
    }
    // A run completion is already on screen: handlers/run.ts toastCompletion renders
    // it on a focused page and renders it better, carrying the verdict as the toast
    // LEVEL where a push body is one info toast. A second toast is one fact twice.
    // The class does not exist for agent_finished, whose foreground channel is
    // notifyIfHidden and so is already silent on a focused page.
    if (parsePushTarget({ chatId: msg.chatId, subject: msg.subject ?? "" }).kind === "run") {
      return;
    }
    toast.info(notice(msg));
  });
}
