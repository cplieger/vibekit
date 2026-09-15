// The subject vocabulary: what a notification is ABOUT, and the one place a subject
// becomes a destination. DOM-free, so the service worker compiles it too.

import type { Route } from "./route-path.js";

/** vibekit.PRSubjectPrefix. The ONLY TypeScript copy. */
const PR_SUBJECT_PREFIX = "pr:";

/** vibekit.RunSubjectPrefix. The ONLY TypeScript copy. */
const RUN_SUBJECT_PREFIX = "run:";

/** The two subject fields as they travel (vibekit.PushSubject, push_types.go:83-86). */
interface PushWire {
  readonly chatId: string;
  readonly subject: string;
}

/** What a notification is ABOUT, after parsing. */
export type PushTarget =
  | { readonly kind: "chat"; readonly chatID: string }
  | { readonly kind: "pr"; readonly identity: string }
  | { readonly kind: "run"; readonly workflowID: string }
  | { readonly kind: "workspace" };

export function parsePushTarget(wire: PushWire): PushTarget {
  if (wire.subject.startsWith(PR_SUBJECT_PREFIX)) {
    const identity = wire.subject.slice(PR_SUBJECT_PREFIX.length);
    return identity === "" ? { kind: "workspace" } : { kind: "pr", identity };
  }
  if (wire.subject.startsWith(RUN_SUBJECT_PREFIX)) {
    return runTarget(wire.subject.slice(RUN_SUBJECT_PREFIX.length));
  }
  return chatTarget(wire.chatId);
}

/** The one producer of a destination: both halves of the app spend this, so the
 *  worker's URL and the page's tabs cannot name different places. */
export function pushTargetRoute(target: PushTarget): Route {
  switch (target.kind) {
    case "chat":
      return { kind: "chat", id: target.chatID };
    case "pr":
      return { kind: "git", tab: "prs", pr: target.identity };
    case "run":
      return { kind: "run", id: target.workflowID };
    case "workspace":
      return { kind: "chat", id: "" };
  }
}

/** The OS coalescing tag: one tray slot per SUBJECT, so a permission ask on one chat
 *  cannot silently replace the finished note on another. */
export function pushTargetTag(target: PushTarget): string {
  switch (target.kind) {
    case "chat":
      return `vibekit:${target.chatID}`;
    case "pr":
      return `vibekit:${PR_SUBJECT_PREFIX}${target.identity}`;
    case "run":
      return `vibekit:${RUN_SUBJECT_PREFIX}${target.workflowID}`;
    case "workspace":
      return "vibekit";
  }
}

/** Whether a tag names a chat or a run, the two targets a pending set speaks for. A
 *  pull request's banner has no ask to be settled by and is never retracted, and the
 *  constant tag is the cue this page showed for no one chat. */
export function settleableTag(tag: string): boolean {
  return tag.startsWith("vibekit:") && !tag.startsWith(`vibekit:${PR_SUBJECT_PREFIX}`);
}

/** A pull request's identity as both halves of the app spell it: the subject key
 *  minus its prefix. Twin of the composition in vibekit.PRSubject.
 *
 *  OPAQUE, and never parsed: `forgeID` is itself `<kind>:<host>` (forges.MakeID), so
 *  the key contains a colon and is not self-delimiting. The PRs tab COMPARES the
 *  identity it builds for each of its own rows against the one that travelled. */
export function prIdentity(forgeID: string, repo: string, number: number): string {
  return `${forgeID}:${repo}#${String(number)}`;
}

export function chatTarget(chatID: string): PushTarget {
  // `run:<workflowId>` is a BRIDGE KEY, not a chat: internal/agent/run_host.go
  // registers a parentless run's bridge under it (`runChatPrefix`), so that run's asks
  // are broadcast on it and arrive as the envelope chat id. Refused here rather than at
  // each caller for run-store.ts noteRunChat's stated reason — "which chat launched this
  // run" is this module's own question, and a caller downstream cannot tell a real id
  // from a synthetic one afterwards.
  if (chatID.startsWith(RUN_SUBJECT_PREFIX)) {
    return runTarget(chatID.slice(RUN_SUBJECT_PREFIX.length));
  }
  return chatID === "" ? { kind: "workspace" } : { kind: "chat", chatID };
}

export function runTarget(workflowID: string): PushTarget {
  return workflowID === "" ? { kind: "workspace" } : { kind: "run", workflowID };
}

/** The target for an ASK, which carries both an envelope chat id and a run
 *  attribution. The run wins whenever it is present: the chat id is where the ask
 *  TRAVELS, the run id is what it is ABOUT. */
export function askTarget(chatID: string, runID: string | undefined): PushTarget {
  return runID !== undefined && runID !== "" ? runTarget(runID) : chatTarget(chatID);
}
