// Client-side session store. Sessions are an ordered keyed collection; MESSAGES are
// deliberately not, because each session owns a Message[] whose per-block and per-tool
// streaming signals (store-signals.ts) are finer-grained than a per-message signal.

import type {
  Session,
  ChatHeader,
  Message,
  Block,
  Usage,
  ToolCall,
  CodeReference,
  RefusalInfo,
  FileChange,
  PendingSteer,
  SteerOrigin,
  SteerAnchor,
  SteerMark,
  ToolStatus,
} from "./types.js";
// From the generated wire rather than turns.ts, which imports this module: one spelling
// of the enum, and a type-only import adds no runtime edge.
import type { TurnOutcome } from "./wire/types.gen.js";
import { severityOf } from "./turn-severity.js";
import { parseStepSubtask } from "./step-subtask.js";
import {
  signal,
  computed,
  createCollection,
  batch,
  touch,
  SignalMap,
  type Signal,
} from "@cplieger/reactive";
import {
  streamingTextSigs,
  streamingReasoningSigs,
  blockTextSigs,
  blockThinkingSigs,
  blockKey,
  clearBlockSigsFor,
  toolCallSigs,
  toolCallSigKey,
} from "./store-signals.js";

// --- Messages reactivity: PER-CHAT transcript versions ---
// One version signal per chat, so a background chat's stream cannot repaint the visible
// transcript. Every bump carries a RenderCause, declared at the branch that knows what
// changed; causes merge upward per chat until the flush.
const messagesVersionSigs = new SignalMap<number>();

/** What a version bump was FOR — what the renderer may skip.
 *
 *   - `chunk`: text growth of a MOUNTED block; paint refreshes tail bookkeeping only.
 *   - `tool`: an existing tool call's update; keyed update of the owning message.
 *   - `fact`: a transcript fact flipped; full projection + reconcile.
 *   - `shape`: the message list's structure changed; the full pass.
 *   - `load`: the window was REPLACED by a fetched page; the full pass, plus the one fact
 *     the array cannot state — those rows are a REPLAY, so none of them is an arrival. */
export type RenderCause = "chunk" | "tool" | "fact" | "shape" | "load";

/** `load` outranks `shape`: shape's work is contained in load's, while load's REPLAY
 *  statement is not recoverable from the array, so dropping it would animate every row
 *  of a reopened conversation. */
const CAUSE_RANK: Record<RenderCause, number> = {
  chunk: 0,
  tool: 1,
  fact: 2,
  shape: 3,
  load: 4,
};

/** The per-chat cause accumulator. `msgID` survives only while every merged cause is
 *  `tool` for ONE message — the keyed-update address; two messages escalate to shape. */
const pendingCause = new Map<string, { cause: RenderCause; msgID?: string }>();

/** The cause the chat's CURRENT version was flushed with. Renderer-only read. */
const flushedCause = new Map<string, { cause: RenderCause; msgID?: string }>();

function mergeCause(chatID: string, cause: RenderCause, msgID?: string): void {
  const cur = pendingCause.get(chatID);
  if (cur === undefined) {
    pendingCause.set(chatID, msgID !== undefined ? { cause, msgID } : { cause });
    return;
  }
  if (cause === "tool" && cur.cause === "tool") {
    // Different messages escalate: one keyed update cannot refresh two turns.
    if (cur.msgID !== msgID) {
      pendingCause.set(chatID, { cause: "shape" });
    }
    return;
  }
  if (CAUSE_RANK[cause] > CAUSE_RANK[cur.cause]) {
    pendingCause.set(chatID, msgID !== undefined ? { cause, msgID } : { cause });
  }
}

/** THIS chat's transcript version. A tracked read subscribes to this chat's transcript
 *  changes and nothing else's. */
export function messagesVersionOf(chatID: string): Signal<number> {
  return messagesVersionSigs.ensure(chatID, 0);
}

/** The cause the current version was bumped for; `paint()` reads it untracked right after
 *  the version. A chat switch or an absent record reads as `shape`, the full pass. */
export function renderCauseOf(chatID: string): { cause: RenderCause; msgID?: string } {
  return flushedCause.get(chatID) ?? { cause: "shape" };
}

/** Flush the accumulator into a version bump. The SYNC path paints the MERGED cause and
 *  clears it, so a pending chunk flush never inherits a later shape. */
function flushCause(chatID: string): void {
  const merged = pendingCause.get(chatID);
  if (merged === undefined) {
    return;
  }
  pendingCause.delete(chatID);
  messagesScheduled.delete(chatID);
  flushedCause.set(chatID, merged);
  const sig = messagesVersionSigs.ensure(chatID, 0);
  sig.value = sig.peek() + 1;
}

/** Bump `chatID`'s transcript version synchronously. List-shape writers use this, in and
 *  out of module; per-delta paths go through `scheduleMessages`. The split is real: a
 *  message arriving must be in the DOM before the frame it was announced in, while a
 *  tick's worth of deltas should collapse into one repaint. */
export function bumpMessages(chatID: string, cause: RenderCause = "shape"): void {
  mergeCause(chatID, cause);
  flushCause(chatID);
}

/** Chats with a bump parked on the next microtask. `removeChat` deletes its id here, so a
 *  pending flush cannot re-mint the version signal it just cleared. */
const messagesScheduled = new Set<string>();

/** Coalesce one chat's per-delta bumps into a single repaint per microtask. */
function scheduleMessages(chatID: string, cause: RenderCause, msgID?: string): void {
  mergeCause(chatID, cause, msgID);
  if (messagesScheduled.has(chatID)) {
    return;
  }
  messagesScheduled.add(chatID);
  queueMicrotask(() => {
    if (messagesScheduled.delete(chatID)) {
      flushCause(chatID);
    }
  });
}

/** The cheap cause for a frame the transcript renders NOTHING for, or `shape`.
 *
 *  Keyed on the PARSE, because that is what `messages-blocks.ts` `isDroppedStep` drops on:
 *  the `wf:` PREFIX is a wider set whose other shapes reach the delegate-box fallback and
 *  DO render, so treating one as dropped would leave its box unmounted. A dropped block
 *  needs no structural work, so the arms below take `chunk`. */
function droppedFrameCause(subtaskID: string): RenderCause {
  return parseStepSubtask(subtaskID) !== null ? "chunk" : "shape";
}

// --- Model context sizes ---
export const MODEL_CONTEXT_SIZES: Record<string, number> = {};

export function parseContextSize(description: string): number | undefined {
  const m = /(\d+)\s*[Kk]\s*context/i.exec(description);
  if (m?.[1] !== undefined) {
    return parseInt(m[1], 10) * 1_000;
  }
  const m2 = /(\d+)\s*[Mm]\s*context/i.exec(description);
  if (m2?.[1] !== undefined) {
    return parseInt(m2[1], 10) * 1_000_000;
  }
  if (/\b1M\b/.test(description)) {
    return 1_000_000;
  }
  return undefined;
}

// --- Session state: the collection is the single source of truth ---
/** Ordered keyed collection of sessions. Structure ops fire `sessions.ids`; per-session
 *  field writes fire `signalFor(id)`. Module-private: consumers go through the typed
 *  accessors below and the `activeSession` computed. */
const sessions = createCollection<Session>((s) => s.id);
/** The active chat id. */
const activeId = signal("");
/** Active session, tracking the active id AND the active session's signal, so subscribers
 *  re-render only when that session or which session is active changes. */
export const activeSession = computed<Session | undefined>(() => {
  touch(sessions.ids); // also re-derive on structural changes (add/remove/setAll)
  const id = activeId.value;
  return id === "" ? undefined : sessions.signalFor(id)?.value;
});

const msgIndex = new Map<string, Map<string, number>>();

// --- Accessors ---
export function getSessions(): Session[] {
  return sessions.items();
}
export function getActiveId(): string {
  return activeId.peek();
}
/** The active chat id as a TRACKED read; `getActiveId` is the untracked peek. */
export function watchActiveId(): string {
  return activeId.value;
}
export function getActive(): Session | undefined {
  const id = activeId.peek();
  return id === "" ? undefined : sessions.get(id);
}
export function get(id: string): Session | undefined {
  return sessions.get(id);
}

/** One chat's session as a TRACKED read: re-runs on THIS session's field changes and on
 *  the session set's structure changing, never on another session's field churn. */
export function watchSession(id: string): Session | undefined {
  touch(sessions.ids);
  return sessions.signalFor(id)?.value;
}

/** Whether `chatID`'s transcript already holds `messageID`.
 *
 *  The ACCEPTANCE test for a prompt this client sent: `CmdPrompt` persists and broadcasts
 *  the user row BEFORE the ACP call and nothing rolls that back, so an echo of our own
 *  message id is proof the server took the prompt whatever the POST went on to answer. */
export function hasMessage(chatID: string, messageID: string): boolean {
  return sessions.get(chatID)?.messages.some((m) => m.id === messageID) ?? false;
}

export function setSessions(v: Session[]): void {
  sessions.setAll(v);
  msgIndex.clear();
}

export function setActive(id: string): void {
  if (activeId.peek() === id) {
    return;
  }
  // Re-derives `activeSession`, so the messages renderer repaints the new chat's
  // #messages; without it the renderer keeps the previous chat's DOM.
  activeId.value = id;
  stampActivity(id);
}

// --- Eviction: reclaim idle background transcripts ---
// The sweep evicts the message window of a chat nobody can be reading. The session ROW
// survives with its header data, and `residency` is what makes the next activation
// refetch instead of trusting the hole.

/** How often the sweep looks for idle chats. */
export const EVICT_SWEEP_MS = 5 * 60 * 1000;
/** How long a chat must sit without activity before its window is evictable. */
export const EVICT_IDLE_MS = 30 * 60 * 1000;

/** When each chat last did anything a reader could be following. A side table rather than
 *  a Session field so a per-chunk stamp never churns the session signal; a chat with NO
 *  entry is treated as active. */
const lastActivity = new Map<string, number>();

function stampActivity(chatID: string): void {
  if (chatID !== "") {
    lastActivity.set(chatID, Date.now());
  }
}

/** Externally-owned reasons a chat must not be evicted, registered by the composition
 *  root so this module stays a leaf (importing tabs.ts or run-store.ts here would invert
 *  the dependency direction). Nothing registered means no external exemption. */
const evictionExemptions: ((chatID: string) => boolean)[] = [];

/** Register one exemption predicate. Returns its unregister. */
export function registerEvictionExemption(fn: (chatID: string) => boolean): () => void {
  evictionExemptions.push(fn);
  return () => {
    const i = evictionExemptions.indexOf(fn);
    if (i >= 0) {
      evictionExemptions.splice(i, 1);
    }
  };
}

/** Whether the sweep may evict this chat's window. Five exemptions, each alone decisive:
 *  the active chat, a busy chat, a chat with an EXECUTING run, a parked view, and an open
 *  subagent tab projecting the chat. */
function evictable(s: Session, now: number): boolean {
  if (s.messages.length === 0) {
    return false; // nothing resident to reclaim
  }
  if (s.id === activeId.peek()) {
    return false; // the active chat is being read
  }
  if (s.thinking) {
    return false; // busy: a live turn is streaming into this window
  }
  const at = lastActivity.get(s.id);
  if (at === undefined || now - at < EVICT_IDLE_MS) {
    return false; // recently active, or never observed — err toward keeping
  }
  return !evictionExemptions.some((fn) => fn(s.id));
}

function sweepEvictions(): void {
  // A hidden tab's clock keeps running but nothing is reclaimed until the reader returns:
  // eviction is for THEIR memory, and a wake-up burst of refetches is the cost avoided.
  // The capability read only skips on a positive "hidden"; a document-less runtime sweeps.
  const hidden = (globalThis as { readonly document?: { readonly hidden?: boolean } }).document
    ?.hidden;
  if (hidden === true) {
    return;
  }
  const now = Date.now();
  for (const s of getSessions()) {
    if (evictable(s, now)) {
      evictChatMessages(s.id);
    }
  }
}

let evictTimer: ReturnType<typeof setInterval> | undefined;

/** Start the sweep. Idempotent; the composition root calls it at boot. */
export function startEvictionSweep(): void {
  evictTimer ??= setInterval(sweepEvictions, EVICT_SWEEP_MS);
}

/** Stop the sweep (tests and symmetric teardown). */
export function stopEvictionSweep(): void {
  if (evictTimer !== undefined) {
    clearInterval(evictTimer);
    evictTimer = undefined;
  }
}

/** Drop every per-message streaming signal a chat's resident messages minted. Covers a
 *  chat leaving WHOLE (removal, eviction), where no reconcile ever runs for background
 *  rows; the renderer's disposeMessage covers rows that unmount. */
function clearMessageSignals(chatID: string, messages: readonly Message[]): void {
  for (const m of messages) {
    clearBlockSigsFor(m.id);
    streamingTextSigs.clear(m.id);
    streamingReasoningSigs.clear(m.id);
    for (const tc of m.tool_calls ?? []) {
      toolCallSigs.clear(toolCallSigKey(chatID, tc.id));
    }
  }
}

/** Evict a chat's message window, keeping the session ROW so header data stays. Everything
 *  keyed by the window goes with it: the msg index, the per-message signals, the snapshot
 *  watermark, and the chat's version signal. `residency: "evicted"` is what the next
 *  activation keys its refetch on. */
export function evictChatMessages(chatID: string): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  clearMessageSignals(chatID, s.messages);
  s.messages = [];
  s.has_more = s.message_count > 0;
  s.residency = "evicted";
  msgIndex.delete(chatID);
  clearSnapshotSeq(chatID);
  messagesScheduled.delete(chatID);
  pendingCause.delete(chatID);
  flushedCause.delete(chatID);
  messagesVersionSigs.clear(chatID);
}

/** Background ingest on an evicted chat leaves it PARTIAL, so only a successful
 *  newest-page load may claim `loaded` again. Called at every site that pushes a NEW
 *  message row into a session. */
function noteResidentMutation(s: Session): void {
  if (s.residency === "evicted") {
    s.residency = "partial";
  }
}

// --- Sync epoch: which loads survived the last transport gap ---

/** Counts transport replay gaps. Plain module state rather than a signal: consulted at
 *  activation and fetch time, never rendered. */
let syncEpochCount = 0;

export function syncEpoch(): number {
  return syncEpochCount;
}

/** Every window loaded under the old epoch is a claim this client can no longer support.
 *  Bumped BEFORE any heal starts: a fetch in flight captured the old number and stays
 *  stale, because its answer may predate events the gap dropped. */
export function bumpSyncEpoch(): void {
  syncEpochCount++;
}

/** The activation refetch gate: a window is trustworthy only if a newest-page load
 *  succeeded and no transport gap has intervened since its request went out. An absent
 *  `loadedEpoch` never equals the counter, so a never-loaded chat is stale by construction. */
export function transcriptStale(s: Session): boolean {
  return s.residency !== "loaded" || s.loadedEpoch !== syncEpochCount;
}

export function isThinking(id: string): boolean {
  return get(id)?.thinking ?? false;
}

/** Whether a chat holds no conversation at all: nothing on the record and nothing resident.
 *
 *  Both halves are load-bearing, which is why this is one predicate. `message_count` is the
 *  server's count and is 0 on a chat it has never heard of, while `messages` is the
 *  paginated window a `session/load` replay can fill before a header refresh restates the
 *  count. An absent chat is empty, so callers need no second null check. */
export function isEmptyChat(s: Session | undefined): boolean {
  return s === undefined || (s.message_count === 0 && s.messages.length === 0);
}

export function setThinking(id: string, v: boolean): void {
  if (get(id) === undefined) {
    return;
  }
  stampActivity(id);
  sessions.update(id, (s) => {
    const next: Session = {
      ...s,
      thinking: v,
      working_label: v ? s.working_label : "Thinking",
    };
    // A new turn invalidates every verdict the previous turn left behind: the agent's
    // declared status and both outcome latches are latched-until-next-turn for the same
    // reason, so they clear in one place.
    if (v) {
      delete next.agent_status;
      delete next.agent_status_text;
      delete next.turn_failed;
      delete next.turn_done;
      // The server's last liveness statement joins them: it described the PREVIOUS turn. Left
      // standing, a `turn_open: false` from before this turn started would make `turnLive` fall
      // back to `thinking` alone.
      delete next.turn_open;
    }
    return next;
  });
  // Transcript fact: `thinking` feeds the live-turn derivation the renderer paints from.
  scheduleMessages(id, "fact");
}

/** Record the server's statement about whether this chat has a turn open. Written from
 *  `GET /api/chats/{id}`'s `turn_open` (newest page only — an older-page fetch is a
 *  scroll-up and asserts nothing about liveness) and by the `turn_ended` handler. */
export function setTurnOpen(id: string, open: boolean): void {
  const s = get(id);
  if (s === undefined || s.turn_open === open) {
    return; // no-op: do not churn the session signal on a repeated statement
  }
  sessions.update(id, (prev) => ({ ...prev, turn_open: open }));
  // Transcript fact: it feeds `turnLive`, which is the projection's liveness input.
  scheduleMessages(id, "fact");
}

/** Is a turn RUNNING on this chat, as far as anything here can know?
 *
 *  THE ONE READER of `turn_open`, and the projection's liveness input. Neither input alone
 *  is the answer: `thinking` is this client's own memory of a stream it has watched (false
 *  through every reload), and `turn_open` is the server's last statement (a fact when it
 *  arrives, stale afterwards). Without both, the window between the chat GET painting and
 *  the HELD `turn_state` frame releasing derives a TERMINAL verdict it provably cannot
 *  know, and mounts a footer glyph over a turn that is still running. */
export function turnLive(s: Session): boolean {
  return s.thinking || s.turn_open === true;
}

/** Latch that this chat's last TURN failed — an outcome `outcomeLatch` grades `failed`.
 *  Cleared by the next `setThinking(id, true)`, so a failure stands until work resumes.
 *  A turn is the ONLY producer; the other callers re-derive the same verdict through
 *  `outcomeLatch` rather than widening it. */
export function setTurnFailed(id: string): void {
  const s = get(id);
  if (s === undefined || s.turn_failed === true) {
    return; // no-op: don't churn the session signal on a replayed error frame
  }
  sessions.update(id, (prev) => ({ ...prev, turn_failed: true }));
  scheduleMessages(id, "fact"); // feeds the turn-outcome derivation
}

/** Clear the failure latch without starting a turn. Only the transport-gap reconciler
 *  needs this: after a dropped stream the client cannot tell which of its latched verdicts
 *  still hold. */
export function clearTurnFailed(id: string): void {
  if (get(id)?.turn_failed !== true) {
    return;
  }
  sessions.update(id, (prev) => {
    const next: Session = { ...prev };
    delete next.turn_failed;
    return next;
  });
  scheduleMessages(id, "fact");
}

/** Latch that this chat's last turn finished. The mirror of `setTurnFailed`, and it exists
 *  for the same reason: `turn_ended` always arrives, while the agent's own `completed`
 *  status only arrives when the model calls its status tool, so without the latch "this
 *  chat finished" held only for the turns where it did. */
export function setTurnDone(id: string): void {
  const s = get(id);
  if (s === undefined || s.turn_done === true) {
    return; // no-op: don't churn the session signal on a replayed turn_ended
  }
  sessions.update(id, (prev) => ({ ...prev, turn_done: true }));
  scheduleMessages(id, "fact");
}

/** Clear the finished latch. ONE caller, the transport-gap reconciler: after a dropped
 *  stream the client can no longer support the claim. The ordinary clear is the next
 *  `setThinking(id, true)`. Opening the chat deliberately does NOT clear it — what keeps a
 *  watched chat out of the title count is the acknowledgement pass in attention.ts. */
export function clearTurnDone(id: string): void {
  if (get(id)?.turn_done !== true) {
    return;
  }
  sessions.update(id, (prev) => {
    const next: Session = { ...prev };
    delete next.turn_done;
    return next;
  });
  scheduleMessages(id, "fact");
}

/** Map a persisted turn outcome onto the latch it sets, or "" for one that latches nothing.
 *  ONE table, and all three producers of this verdict call it — the live `turn_ended`
 *  handler, `relatchTurnVerdict`, and `latchFieldsFor` — so they cannot disagree.
 *
 *  THE HOLLOW RING MEANS THE CHAT HAS NOT INITIATED, which decides the floor: a chat that
 *  has run a turn may never paint `idle`, so only a chat with no turn and a record with no
 *  outcome reach it. `stopped` therefore latches DONE, because `done` is the transport's
 *  verdict that a turn FINISHED, never the agent's claim that it succeeded. */
export function outcomeLatch(outcome: TurnOutcome | undefined): "done" | "failed" | "" {
  // ABSENCE is answered here and never by `severityOf`, which grades OUTCOMES: its default
  // arm reads an unrecognised value as `stopped` so a value the wire adds later cannot read
  // as a turn that worked. "The wire said something I cannot grade" is a turn that ran; "the
  // record carries no outcome" is the one case the hollow ring is still correct for.
  if (outcome === undefined) {
    return "";
  }
  switch (severityOf(outcome)) {
    case "clean":
    case "stopped":
      return "done";
    case "broken":
      return "failed";
    case "running":
      return "";
  }
}

/** The latch fields to spread into a `Session` being rebuilt from a `ChatHeader`. What makes
 *  a chat tab's dot survive a reconnect, since both latches are CLIENT memory. Three rules,
 *  in order: an existing latch is carried over unchanged (a live `turn_ended` on this page
 *  is newer than any header read, so this can only add a latch, never clear one); a live
 *  turn seeds nothing, because the header's outcome describes the turn before the one now
 *  running; otherwise the header's outcome decides, through `outcomeLatch`. Built
 *  conditionally because under `exactOptionalPropertyTypes` an explicit `undefined` spread
 *  over an existing session would DELETE the latch rule 1 exists to keep. */
export function latchFieldsFor(
  existing: Session | undefined,
  h: ChatHeader,
): { turn_done?: true; turn_failed?: true } {
  if (existing?.turn_failed === true || existing?.turn_done === true) {
    const carried: { turn_done?: true; turn_failed?: true } = {};
    if (existing.turn_failed === true) {
      carried.turn_failed = true;
    }
    if (existing.turn_done === true) {
      carried.turn_done = true;
    }
    return carried;
  }
  if (existing?.thinking === true) {
    return {};
  }
  switch (outcomeLatch(h.last_turn_outcome)) {
    case "done":
      return { turn_done: true };
    case "failed":
      return { turn_failed: true };
    default:
      return {};
  }
}

/** Re-derive the outcome latches from the PERSISTED record: the newest message carrying a
 *  `turn_outcome` says how this chat's last turn ended.
 *
 *  The latches are client memory, so every page load and every transport gap dropped them,
 *  while the outcome itself is durable. Called after every newest-page message load, which
 *  is the gap door's own heal path. Refuses to overwrite a live turn (`thinking`) or a
 *  latch already set, which is newer than anything the page carries. */
export function relatchTurnVerdict(id: string): void {
  const s = get(id);
  if (s === undefined || s.thinking || s.turn_done === true || s.turn_failed === true) {
    return;
  }
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const outcome = s.messages[i]?.turn_outcome;
    if (outcome === undefined) {
      continue;
    }
    const latch = outcomeLatch(outcome);
    if (latch === "done") {
      setTurnDone(id);
    } else if (latch === "failed") {
      setTurnFailed(id);
    }
    return;
  }
}

/** Derive the chat tab's activity-dot state. ONE rule, shared by the store effect and the
 *  turn_ended / error handlers. Order is precedence, and `pendingAsk` is a parameter
 *  because `decision-dock.ts` imports this module.
 *   - `input` outranks `working` because the two COEXIST — a permission ask arrives mid-turn
 *     and `thinking` stays true — so working first would mask every ask.
 *   - `waiting` outranks `done`, which can coexist: the agent declares waiting_on_user and
 *     then the turn ends, setting `turn_done` under a live waiting.
 *   - `idle` MEANS THE CHAT HAS NOT INITIATED, so a chat tab always shows a dot. */
export type TabDotState = "" | "input" | "failed" | "working" | "waiting" | "done" | "idle";

export function tabStatusFor(s: Session | undefined, pendingAsk = false): TabDotState {
  if (s === undefined) {
    return "";
  }
  if (pendingAsk) {
    return "input";
  }
  if (s.turn_failed === true) {
    return "failed";
  }
  if (s.thinking) {
    return "working";
  }
  if (s.agent_status === "waiting_on_user") {
    return "waiting";
  }
  if (s.agent_status === "completed" || s.turn_done === true) {
    return "done";
  }
  return "idle";
}

/** The pause classes the dot vocabulary distinguishes. A classified value rather than the
 *  raw `pauseReason`, so this module never learns KAS's pause sentences or its node
 *  signals: `run-store.ts` owns that rule and hands the answer over already decided. */
export type RunPauseClass = "" | "need_input";

/** The same dot vocabulary for a workflow RUN, which owns a `run:<workflowId>` tab and has
 *  no `Session` behind it. Its own function rather than a branch in `tabStatusFor` because
 *  the inputs share not one field; what it shares is the OUTPUT vocabulary and precedence.
 *
 *  `paused` is `waiting` — stopped, not finished — EXCEPT for a step parked on a person,
 *  which is `input`, the same dot an unanswered card raises; the park is the half that
 *  survives a client that never received the card. `pause` is read only in the `paused`
 *  arm, so a stale reason on a finished run cannot paint it yellow. */
export function runStatusFor(
  status: string | undefined,
  pendingAsk = false,
  pause: RunPauseClass = "",
): TabDotState {
  if (pendingAsk) {
    return "input";
  }
  switch (status) {
    case undefined:
    case "":
      return "";
    case "running":
      return "working";
    case "paused":
      return pause === "need_input" ? "input" : "waiting";
    case "failed":
    case "aborted":
      return "failed";
    default:
      return "done";
  }
}

/** The same dot vocabulary for a SUBAGENT, whose tab is a sub-tab under the chat that
 *  dispatched it. A delegate's whole state is its INVOCATION TOOL CALL's `ToolStatus`, a
 *  generated closed union, so the arms below are exhaustive and need no `default`.
 *
 *  `undefined` means THIS CLIENT HOLDS NO INVOCATION, which is a resident-window fact
 *  rather than anything about the delegate, and is answered FIRST so no arm below has to
 *  consider absence. Nothing maps to `idle`: a delegate someone opened a tab for has run. */
export function subagentStatusFor(status: ToolStatus | undefined): TabDotState {
  if (status === undefined) {
    return "";
  }
  switch (status) {
    case "pending":
    case "in_progress":
      return "working";
    case "completed":
      return "done";
    case "failed":
      return "failed";
  }
}

/** Record the agent-declared activity status/description for a chat (chat_status SSE, from
 *  the KAS focus_update channel). Empty strings clear the respective field. */
export function setAgentStatus(id: string, status: string, text: string): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  if ((s.agent_status ?? "") === status && (s.agent_status_text ?? "") === text) {
    return; // no-op: don't churn the session signal
  }
  sessions.update(id, (prev) => {
    const next: Session = { ...prev };
    if (status === "") {
      delete next.agent_status;
    } else {
      next.agent_status = status;
    }
    if (text === "") {
      delete next.agent_status_text;
    } else {
      next.agent_status_text = text;
    }
    return next;
  });
}

export function setWorkingLabel(id: string, label: string): void {
  if (get(id)?.working_label === label) {
    return;
  }
  sessions.update(id, (s) => ({ ...s, working_label: label }));
  scheduleMessages(id, "fact"); // the resume control's fallback label
}

// --- Mid-turn steers (the dock's waiting rows + the transcript's marks) ---
// TWO FIELDS, TWO LIFETIMES. `session.steers` is what the agent has NOT read: the bottom
// dock's rows, whose lifetime is the turn. `session.steer_marks` is what has LEFT the
// dock, rendered inside the turn transcript at the block it landed on, whose lifetime is
// the loaded transcript. An entry moves from the first to the second and never back.
// The client writes INTENT (`recordSteerSent`, un-written by `forgetSteer`) and every
// other mutator here adopts a server FACT.

/** The steer id KAS will return for a message id. `internal/vibekit/commands.go` documents
 *  the convention (`"steer-" + messageID`) and `internal/command/steer_test.go` pins it.
 *  Deriving it is what lets the optimistic row be reconciled by a plain id match: the
 *  POST's reply carries the authoritative id, but `transportAction` discards the body. */
export function steerIDFor(messageID: string): string {
  return `steer-${messageID}`;
}

/** Number of steers waiting for the agent, confirmed or still in flight. */
export function steerCount(id: string): number {
  return get(id)?.steers?.length ?? 0;
}

/** The steers that have left the dock and now belong to the transcript. */
export function steerMarks(id: string): readonly SteerMark[] {
  return get(id)?.steer_marks ?? [];
}

/** Record a steer this client has just POSTed, before any server frame. `pending` says the
 *  id is DERIVED rather than confirmed, which is what the dock reads to withhold Edit and
 *  Discard — there is no server-side id to clear yet. Rolled back by `forgetSteer`. */
export function recordSteerSent(id: string, messageID: string, text: string): void {
  const s = get(id);
  if (s === undefined || messageID === "") {
    return;
  }
  // `user` is a FACT here, not a guess: this row is this device's own POST.
  const entry: PendingSteer = {
    id: steerIDFor(messageID),
    text,
    origin: "user",
    pending: true,
  };
  const existing = s.steers ?? [];
  // Idempotent by id: submit.ts reuses one message id when retrying a failed attempt, and
  // that retry must refresh the row rather than add a second.
  const at = existing.findIndex((e) => e.id === entry.id);
  const next = at >= 0 ? existing.map((e, i) => (i === at ? entry : e)) : [...existing, entry];
  sessions.update(id, (cur) => ({ ...cur, steers: next }));
  scheduleMessages(id, "fact"); // the dock row is transcript-adjacent state
}

/** Un-draw one steer row; the rollback half of `recordSteerSent`. Removes by id and leaves
 *  everything else alone, so a 409 cannot take a sibling still waiting with it. */
export function forgetSteer(id: string, steerID: string): void {
  const s = get(id);
  if (s?.steers === undefined) {
    return;
  }
  const rest = s.steers.filter((e) => e.id !== steerID);
  if (rest.length === s.steers.length) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, rest));
  scheduleMessages(id, "fact");
}

/** Adopt KAS's own confirmation of a steer into the dock. Three branches, one outcome —
 *  exactly one row per message: the id matches (adopt the text, clear `pending`); no id
 *  match but the OLDEST pending row carries the same text (adopt the server's id, the
 *  fallback if the prefix convention drifts); neither (append it confirmed — another
 *  device, or this one before a reload). Idempotent in all three. `steer_marks` is checked
 *  FIRST because a reconnect replays the queued frame for a steer the agent has since
 *  read, and branch 3 would otherwise put a delivered message back in the dock. */
export function recordSteerQueued(
  id: string,
  steer: { id: string; text: string; origin: SteerOrigin },
): void {
  const s = get(id);
  if (s === undefined || steer.id === "") {
    return;
  }
  if ((s.steer_marks ?? []).some((m) => m.id === steer.id)) {
    return;
  }
  const existing = s.steers ?? [];
  const at = existing.findIndex((e) => e.id === steer.id);
  const adoptAt =
    at >= 0 ? at : existing.findIndex((e) => e.pending === true && e.text === steer.text);
  // The frame's origin wins in every branch: the server resolved it against the ledger of
  // what it sent, where the optimistic row's `user` was this device's claim.
  const entry: PendingSteer = { id: steer.id, text: steer.text, origin: steer.origin };
  const next =
    adoptAt >= 0 ? existing.map((e, i) => (i === adoptAt ? entry : e)) : [...existing, entry];
  sessions.update(id, (cur) => ({ ...cur, steers: next }));
  scheduleMessages(id, "fact");
}

/** Promote a steer the agent has READ out of the dock and into the transcript, anchored at
 *  the block the running turn had reached so the note renders chronologically.
 *
 *  TWO FRAMES, one id: KAS's steering channel sends the read frame (text, no ack), the
 *  acknowledgement marker on the text stream sends the ack frame (ack, no text). Each field
 *  is adopted only when its frame carries it, or the second would blank the first's text —
 *  and the second must not re-anchor a placed note. An id never seen is tolerated; an
 *  ack-only frame for an id with no mark and no text is ignored, having nothing to label. */
export function promoteSteer(
  id: string,
  steerID: string,
  text: string,
  origin: SteerOrigin,
  ack?: string,
): void {
  const s = get(id);
  if (s === undefined || steerID === "") {
    return;
  }
  const existing = s.steers ?? [];
  let at = existing.findIndex((e) => e.id === steerID);
  if (at < 0 && text !== "") {
    // The text fallback `recordSteerQueued` uses, for an injected frame that beat the queued one.
    at = existing.findIndex((e) => e.pending === true && e.text === text);
  }
  const rest = at >= 0 ? existing.filter((_, i) => i !== at) : existing;
  const dockText = at >= 0 ? (existing[at]?.text ?? "") : "";
  const marks = s.steer_marks ?? [];
  const mi = marks.findIndex((m) => m.id === steerID);
  if (mi < 0) {
    const body = text !== "" ? text : dockText;
    if (body === "") {
      return;
    }
    const mark: SteerMark = {
      id: steerID,
      text: body,
      origin,
      ...(ack !== undefined && ack !== "" ? { ack } : {}),
      anchor: anchorFor(s),
    };
    sessions.update(id, (cur) => withSteers({ ...cur, steer_marks: [...marks, mark] }, rest));
    scheduleMessages(id, "fact"); // a mark renders inside the turn
    return;
  }
  // No `origin` here: it is written once, on the frame that CREATES the mark. The ledger
  // behind it is TTL'd, so a late second frame can answer `agent` for the user's message.
  const nextMarks = marks.map((m, i) =>
    i === mi
      ? {
          ...m,
          ...(text !== "" ? { text } : {}),
          ...(ack !== undefined && ack !== "" ? { ack } : {}),
        }
      : m,
  );
  sessions.update(id, (cur) => withSteers({ ...cur, steer_marks: nextMarks }, rest));
  scheduleMessages(id, "fact");
}

/** Drop steers at a turn boundary: out of the dock, into the transcript as UNDELIVERED.
 *  Named ids drop just those; an empty or absent list drops the chat's whole set. Each one
 *  keeps its text and earns a `dropped` mark, which the note offers to put back in the
 *  composer.
 *
 *  An id already in `steer_marks` is HOUSEKEEPING and a no-op: KAS clears its buffer at
 *  every boundary, so `steer_cleared` routinely names ids the model already read, and those
 *  must keep their existing mark rather than gain one claiming they were missed. */
export function dropSteers(id: string, steerIDs?: readonly string[]): void {
  const s = get(id);
  if (s?.steers === undefined) {
    return;
  }
  const named = steerIDs === undefined || steerIDs.length === 0 ? undefined : new Set(steerIDs);
  const going = s.steers.filter((e) => named === undefined || named.has(e.id));
  if (going.length === 0) {
    return;
  }
  const goingIDs = new Set(going.map((e) => e.id));
  const rest = s.steers.filter((e) => !goingIDs.has(e.id));
  const marks = s.steer_marks ?? [];
  const held = new Set(marks.map((m) => m.id));
  const anchor = anchorFor(s);
  const added = going
    .filter((e) => !held.has(e.id))
    // The dock entry's origin, from the queued frame the server stamped: a dropped steer has
    // no injected frame to read one off.
    .map((e): SteerMark => ({ id: e.id, text: e.text, origin: e.origin, dropped: true, anchor }));
  sessions.update(id, (cur) =>
    withSteers(added.length > 0 ? { ...cur, steer_marks: [...marks, ...added] } : cur, rest),
  );
  scheduleMessages(id, "fact"); // dropped marks render in the turn
}

/** Remove every CONFIRMED waiting steer, returning a snapshot to restore from. The
 *  optimistic half of `chat.clear_steers`, and the reason an explicit discard leaves no
 *  transcript row: the entries are gone before `steer_cleared` arrives, so `dropSteers`
 *  finds nothing to promote as "not delivered". A `pending` entry STAYS — it is not in
 *  KAS's buffer yet, so removing it locally would hide a message still on its way.
 *  Returns the array as it was, so the rollback restores the exact order. */
export function dropConfirmedSteers(id: string): readonly PendingSteer[] {
  const s = get(id);
  if (s?.steers === undefined) {
    return [];
  }
  const prev = s.steers;
  const rest = prev.filter((e) => e.pending === true);
  if (rest.length === prev.length) {
    return [];
  }
  sessions.update(id, (cur) => withSteers(cur, rest));
  scheduleMessages(id, "fact");
  return prev;
}

/** Put a `dropConfirmedSteers` snapshot back. The rollback half. */
export function restoreSteers(id: string, prev: readonly PendingSteer[]): void {
  if (prev.length === 0 || get(id) === undefined) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, prev));
  scheduleMessages(id, "fact");
}

/** Forget the dock's contents WITHOUT promoting them. The `transport:gap` path: a gap
 *  means the frames that resolved these steers may be among the ones lost, so promoting
 *  them would assert "the agent never read this" on no evidence. Existing marks stay. */
export function forgetSteers(id: string): void {
  const s = get(id);
  if (s?.steers === undefined) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, []));
  scheduleMessages(id, "fact");
}

/** Where a steer read RIGHT NOW belongs: after everything the turn's assistant message has
 *  produced so far. An empty `msgID` means it was read before the turn produced anything;
 *  `rebindPendingAnchors` binds it to the first assistant message that arrives. */
function anchorFor(s: Session): SteerAnchor {
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const m = s.messages[i];
    if (m?.role === "assistant") {
      return { msgID: m.id, blockIndex: (m.blocks ?? []).length };
    }
  }
  return { msgID: "", blockIndex: 0 };
}

/** Bind every anchor-less mark to a newly-arrived assistant message. Goes through
 *  `sessions.update` rather than a version bump, because only `sessions.update` re-derives
 *  `activeSession`, which is what the renderers' value-dedup computeds read. */
function rebindPendingAnchors(chatID: string, msgID: string): void {
  const marks = get(chatID)?.steer_marks;
  if (marks?.some((m) => m.anchor.msgID === "") !== true) {
    return;
  }
  const next = marks.map((m) =>
    m.anchor.msgID === "" ? { ...m, anchor: { msgID, blockIndex: 0 } } : m,
  );
  sessions.update(chatID, (cur) => ({ ...cur, steer_marks: next }));
}

/** Write `steers` onto a session, DELETING the field when the list is empty, so a session
 *  compares equal to one that never had steers: pending-steers.ts's `computed` dedups by
 *  value and an empty array would repaint on every clear. `steer_marks` likewise. */
function withSteers(s: Session, steers: readonly PendingSteer[]): Session {
  const copy = { ...s };
  if (steers.length === 0) {
    delete copy.steers;
  } else {
    copy.steers = [...steers];
  }
  if (copy.steer_marks?.length === 0) {
    delete copy.steer_marks;
  }
  return copy;
}

/** Rebuild the message index for a session. Exported for store-load.ts. */
export function rebuildMsgIndex(sessionID: string, messages: Message[]): void {
  const idx = new Map<string, number>();
  for (let i = 0; i < messages.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    idx.set(messages[i]!.id, i);
  }
  msgIndex.set(sessionID, idx);
}

function getMsgIndex(sessionID: string, messages: Message[]): Map<string, number> {
  let mi = msgIndex.get(sessionID);
  if (mi === undefined) {
    mi = new Map<string, number>();
    for (let i = 0; i < messages.length; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      mi.set(messages[i]!.id, i);
    }
    msgIndex.set(sessionID, mi);
  }
  return mi;
}

// --- SSE-driven mutations ---
export function upsertHeader(h: ChatHeader): void {
  const existing = get(h.id);
  if (existing !== undefined) {
    // Server-authoritative re-sync of the header fields; messages, thinking and
    // working_label are client/stream-owned.
    sessions.update(h.id, (s) => {
      const next: Session = {
        ...s,
        name: h.name,
        // `model` is the ONE header field the CLIENT can legitimately be ahead of the server on:
        // a pick before the first prompt applies locally and rides that prompt, so until then the
        // record genuinely has no model. `Model` is `omitempty` on the wire, which makes "not set"
        // and "cleared" the same frame, and taking it as a clear is what reset the pill to "auto".
        // Absent means no news — the same rule ingestMessage applies to message content.
        model: h.model !== undefined && h.model !== "" ? h.model : s.model,
        acp_session_id: h.acp_session_id ?? "",
        current_mode_id: h.current_mode_id ?? "",
        available_modes: h.available_modes ?? [],
        available_models: h.available_models ?? [],
        supervised_mode: h.supervised_mode ?? false,
        effort: h.effort ?? "",
        // Absent means the server has no session catalog to report (a chat with no bridge, or a
        // header built before session/new answered), NOT that the tiers went away.
        effort_levels: h.effort_levels ?? s.effort_levels ?? [],
        effort_active: h.effort_active ?? s.effort_active ?? "",
        usage: h.usage,
        message_count: Math.max(s.message_count, h.message_count),
      };
      if (h.compaction_watermark !== undefined) {
        next.compaction_watermark = h.compaction_watermark;
      } else {
        delete next.compaction_watermark;
      }
      // AFTER the spread of `s`, so an already-set latch survives: the helper can only add.
      Object.assign(next, latchFieldsFor(s, h));
      return next;
    });
    return;
  }
  const s: Session = {
    id: h.id,
    name: h.name,
    model: h.model ?? "",
    acp_session_id: h.acp_session_id ?? "",
    current_mode_id: h.current_mode_id ?? "",
    available_modes: h.available_modes ?? [],
    available_models: h.available_models ?? [],
    supervised_mode: h.supervised_mode ?? false,
    effort: h.effort ?? "",
    effort_levels: h.effort_levels ?? [],
    effort_active: h.effort_active ?? "",
    usage: h.usage,
    message_count: h.message_count,
    messages: [],
    has_more: h.message_count > 0,
    thinking: false,
    working_label: "Thinking",
    // A chat this client has never seen live: the header's outcome is the ONLY thing that can
    // tell its dot from a chat that has never run a turn.
    ...latchFieldsFor(undefined, h),
  };
  if (h.compaction_watermark !== undefined) {
    s.compaction_watermark = h.compaction_watermark;
  }
  sessions.prepend([s]);
}

export function setCurrentMode(id: string, modeID: string): void {
  const s = get(id);
  if (s === undefined || s.current_mode_id === modeID) {
    return;
  }
  sessions.update(id, (cur) => ({ ...cur, current_mode_id: modeID }));
}

export function removeChat(id: string): void {
  const doomed = get(id);
  if (doomed === undefined) {
    return;
  }
  const wasActive = activeId.peek() === id;
  const order = sessions.ids.peek();
  // Both reactive writes below feed the `activeSession` computed. Batch them so subscribers
  // re-derive ONCE; otherwise removing the active chat double-fires it and the messages
  // renderer flashes a transient teardown of the new chat's DOM.
  batch(() => {
    sessions.remove(id);
    msgIndex.delete(id);
    clearSnapshotSeq(id);
    clearLiveTurnMessage(id);
    // Every per-message streaming signal the chat's window minted: the renderer's
    // disposeMessage only reaches rows a reconcile removes, and a background chat's never see one.
    clearMessageSignals(id, doomed.messages);
    lastActivity.delete(id);
    // A flush parked on the next microtask must not re-mint the signal cleared here.
    messagesScheduled.delete(id);
    pendingCause.delete(id);
    flushedCause.delete(id);
    messagesVersionSigs.clear(id);
    if (wasActive) {
      const remaining = order.filter((x) => x !== id);
      activeId.value = remaining[0] ?? "";
    }
  });
}

/** Re-insert a previously-removed session at `atIndex`, or at the head. For optimistic
 *  action rollbacks. Idempotent. */
export function reinsertSession(session: Session, atIndex?: number): void {
  if (sessions.has(session.id)) {
    return;
  }
  const order = [...sessions.items()];
  const target = atIndex !== undefined ? Math.max(0, Math.min(atIndex, order.length)) : 0;
  order.splice(target, 0, session);
  sessions.setAll(order);
}

function nonEmptyStr(v: string | undefined): v is string {
  return v !== undefined && v !== "";
}

/** Spread helper: include `agent_subtask_id` only when non-empty, since
 *  exactOptionalPropertyTypes forbids setting an optional field to undefined. */
function subtaskField(id: string | undefined): { agent_subtask_id?: string } {
  return nonEmptyStr(id) ? { agent_subtask_id: id } : {};
}

/** Ensure an assistant message has a `blocks` array so the renderer has ONE path. Legacy
 *  replays (content / reasoning / tool_calls only) get synthesized blocks — thinking, then
 *  text, then a tool_use per tool call. Anything else passes through unchanged. */
export function normalizeMessage(m: Message): Message {
  if (m.role !== "assistant" || (m.blocks !== undefined && m.blocks.length > 0)) {
    return m;
  }
  const tools = m.tool_calls ?? [];
  if (!nonEmptyStr(m.content) && !nonEmptyStr(m.reasoning) && tools.length === 0) {
    return m; // e.g. a plan-only assistant message — nothing to synthesize
  }
  const blocks: Block[] = [];
  if (nonEmptyStr(m.reasoning)) {
    blocks.push({ type: "thinking", thinking: m.reasoning });
  }
  if (nonEmptyStr(m.content)) {
    blocks.push({ type: "text", text: m.content });
  }
  for (const tc of tools) {
    blocks.push({ type: "tool_use", tool_call_id: tc.id, ...subtaskField(tc.agent_subtask_id) });
  }
  return { ...m, blocks };
}

/** Merge a freshly-ingested server message over the existing one: adopt the incoming's
 *  non-empty fields, never clobber non-empty with empty. This is what lets the streamed
 *  assistant message and its final `message_appended` (same id) coexist. */
function mergeMessage(existing: Message, incoming: Message): Message {
  const merged: Message = { ...existing };
  if (nonEmptyStr(incoming.content)) {
    merged.content = incoming.content;
  }
  if (nonEmptyStr(incoming.reasoning)) {
    merged.reasoning = incoming.reasoning;
  }
  if (incoming.blocks !== undefined && incoming.blocks.length > 0) {
    merged.blocks = incoming.blocks;
  }
  if (incoming.tool_calls !== undefined && incoming.tool_calls.length > 0) {
    merged.tool_calls = incoming.tool_calls;
  }
  if (incoming.plan !== undefined && incoming.plan.length > 0) {
    merged.plan = incoming.plan;
  }
  if (incoming.code_references !== undefined && incoming.code_references.length > 0) {
    merged.code_references = incoming.code_references;
  }
  // The allowlist is exhaustive by construction: an unlisted field is silently dropped on
  // the second ingest of the same id, and a user message with attachments is ingested twice.
  if (incoming.attachments !== undefined && incoming.attachments.length > 0) {
    merged.attachments = incoming.attachments;
  }
  if (incoming.refusal !== undefined) {
    merged.refusal = incoming.refusal;
  }
  if (incoming.event_kind !== undefined) {
    merged.event_kind = incoming.event_kind;
  }
  if (incoming.ts > 0) {
    merged.ts = incoming.ts;
  }
  return merged;
}

/** Where a NEW message belongs in the array. A row the server has PERSISTED goes before
 *  the in-flight turn's message, because that is where the chat file has it: the server
 *  writes the file before it broadcasts. A row that OPENS a turn is exempt — a prompt sent
 *  during a live reply genuinely follows it. */
function insertIndexFor(s: Session, incoming: Message): number {
  if (incoming.role === "user") {
    return s.messages.length;
  }
  const liveID = liveTurnMessage(s.id);
  if (liveID === undefined) {
    return s.messages.length;
  }
  const at = msgIndex.get(s.id)?.get(liveID);
  return at ?? s.messages.length;
}

/** Ingest a server-canonical message; message_created / message_appended /
 *  message_updated all route here. Upsert by id with a merge that never drops a message
 *  and never overwrites non-empty content with empty: absent inserts at `insertIndexFor`
 *  after normalizing, present goes through `mergeMessage`. */
function ingestMessage(chatID: string, incoming: Message, persisted: boolean): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  stampActivity(chatID);
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(incoming.id) ?? -1;
  if (idx === -1) {
    noteResidentMutation(s);
    const at = persisted ? insertIndexFor(s, incoming) : s.messages.length;
    if (at === s.messages.length) {
      mi.set(incoming.id, at);
      s.messages.push(normalizeMessage(incoming));
    } else {
      s.messages.splice(at, 0, normalizeMessage(incoming));
      // Every index at or past the splice point moved, so the map is rebuilt, not patched.
      rebuildMsgIndex(chatID, s.messages);
    }
    s.message_count = Math.max(s.message_count, s.messages.length);
    bumpMessages(chatID);
    if (incoming.role === "assistant" && (incoming.plan ?? []).length === 0) {
      // The first moment there is an id to anchor a steer read before this turn produced
      // anything. A PLAN row is skipped though it is RoleAssistant too — the anchor means "the
      // reply this steer was read into" — or it captures every pending mark and the reply's own
      // message_created finds none left.
      rebindPendingAnchors(chatID, incoming.id);
    }
    return;
  }
  const existing = s.messages[idx];
  if (existing !== undefined) {
    s.messages[idx] = mergeMessage(existing, normalizeMessage(incoming));
  }
  bumpMessages(chatID);
}

/** message_appended → merge path. It is also the PERSIST echo: the server writes the chat
 *  file before it broadcasts this, so an id arriving here is no longer the client's only
 *  copy and stops being the in-flight turn. */
export function appendMessage(chatID: string, msg: Message): void {
  if (liveTurnMessage(chatID) === msg.id) {
    clearLiveTurnMessage(chatID);
  }
  ingestMessage(chatID, msg, true);
}

/** message_created / message_updated → merge path. */
export function upsertMessage(chatID: string, msg: Message): void {
  ingestMessage(chatID, msg, false);
}

/** Stamp the just-ended turn's summary (credits / elapsed / changed files) onto the chat's
 *  last assistant message; the renderer projects it into a keyed `.turn-footer`. Applies to
 *  any chat, so a background turn's footer is present when the user switches to it.
 *
 *  Skipped when a persisted row in the same turn body already carries the outcome: the
 *  assistant message beside such a carrier is a SEGMENT of that turn, and `turnLedger`
 *  sums across the body. */
export function setTurnSummary(
  chatID: string,
  data: {
    credits?: number;
    elapsedMs?: number;
    changedFiles?: Record<string, FileChange>;
    model?: string;
  },
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  // TRAILING user rows belong to a LATER turn that has produced nothing yet — a prompt
  // persists its row before it asks for the chat's admission slot — so the walk steps over
  // them and stops at the user row that OPENS the turn being summarised.
  let target: Message | undefined;
  let inBody = false;
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const m = s.messages[i];
    if (m === undefined) {
      continue;
    }
    if (m.role === "user") {
      if (inBody) {
        break;
      }
      continue;
    }
    inBody = true;
    // `target === undefined` is what keeps the veto INSIDE this turn. `projectTurns` closes a
    // turn on an outcome-bearing row as well as on a user row, so a transcript ending
    // [.., event(turn_outcome), assistant] has its carrier in the PREVIOUS turn; ungated, that
    // vetoed the stamp for the new headerless turn.
    if (m.turn_outcome !== undefined && target === undefined) {
      return;
    }
    if (m.role === "assistant" && target === undefined) {
      target = m;
    }
  }
  if (target === undefined) {
    return;
  }
  let changed = false;
  if (data.credits !== undefined && data.credits > 0) {
    target.turn_credits = data.credits;
    changed = true;
  }
  if (data.elapsedMs !== undefined && data.elapsedMs > 0) {
    target.turn_elapsed_ms = data.elapsedMs;
    changed = true;
  }
  if (data.changedFiles !== undefined && Object.keys(data.changedFiles).length > 0) {
    target.changed_files = data.changedFiles;
    changed = true;
  }
  // Same non-empty guard as the numbers above: an absent model means the server could not
  // name one, and stamping "" would make a turn look attributed.
  if (data.model !== undefined && data.model !== "") {
    target.turn_model = data.model;
    changed = true;
  }
  if (changed) {
    bumpMessages(chatID);
  }
}

export function setSupervisedMode(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.supervised_mode === enabled) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, supervised_mode: enabled }));
}

/** Set the chat's reasoning-effort level. Per-chat like model, mode and supervised, so a
 *  tab switch reads the new chat's level instead of carrying the previous one over. */
export function setEffort(chatID: string, effort: string): void {
  const s = get(chatID);
  if (s === undefined || (s.effort ?? "") === effort) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, effort }));
}

/** Set session model and notify subscribers. `usage.context_size` derives from the model,
 *  so it is refreshed in the same update — callers must never mutate `session.usage` on a
 *  stale reference, since sessions.update replaces the object. */
export function setModel(chatID: string, model: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({
    ...cur,
    model,
    usage: { ...cur.usage, context_size: contextSizeFor(model) },
  }));
}

/** Set session name and notify subscribers. */
export function setName(chatID: string, name: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, name }));
}

/** Return the current index of a session in the list, or -1. */
export function indexOfSession(id: string): number {
  return sessions.ids.peek().indexOf(id);
}

/** Per-chat chunk-sequence watermark from a connect-time turn_state snapshot: chunks with
 *  seq <= the watermark are already folded into the snapshot message and must be dropped,
 *  not re-appended. One in-flight turn per chat, so the map is keyed by chat id. */
const snapshotSeqs = new Map<string, { messageID: string; seq: number }>();

/** Record a turn_state snapshot's chunk watermark for a chat. */
export function setSnapshotSeq(chatID: string, messageID: string, seq: number): void {
  snapshotSeqs.set(chatID, { messageID, seq });
}

/** Drop the chunk watermark (turn finished or chat removed). */
export function clearSnapshotSeq(chatID: string): void {
  snapshotSeqs.delete(chatID);
}

/** Reserve the block indices below `upto` whose own frame has not arrived yet.
 *
 *  `block_index` is the server's position in ONE chronological array the client fills from
 *  TWO event streams, so a frame can legitimately name an index past the end of what has
 *  arrived. The pad reserves the DOM position, which is load-bearing rather than defensive:
 *  the block mounter is append-only, so a block whose frame lands late cannot be inserted
 *  between two mounted siblings. The kind is a GUESS — `text` because it mounts a FILLABLE
 *  node where `thinking` would mount nothing; `isPadBlock` lets the real frame correct it. */
function padBlocks(blocks: Block[], upto: number): void {
  while (blocks.length < upto) {
    blocks.push({ type: "text" });
  }
}

/** Whether `b` is still a pad: a kind, and nothing behind it. Every real block carries the
 *  content that created it, so this cannot misread one as a pad. */
function isPadBlock(b: Block): boolean {
  return b.text === undefined && b.thinking === undefined && b.tool_call_id === undefined;
}

/** The assistant message a chat's CURRENT turn is streaming into, while the server still
 *  holds it in memory and nowhere else.
 *
 *  `loadMessages` replaces the array with a fetched page, so it has to know WHICH local
 *  message the page is entitled to omit, and POSITION cannot answer that: the agent
 *  persists messages DURING a turn, and each lands after the streaming reply locally while
 *  sitting inside the page, so "keep everything after the newest id the page carries" steps
 *  past the reply and the replace deletes it. One entry per chat, one buffer per chat. */
const liveTurnMsgIDs = new Map<string, string>();

/** Record the message id a chat's in-flight turn is accumulating into. */
export function noteLiveTurnMessage(chatID: string, messageID: string): void {
  if (messageID === "") {
    return;
  }
  liveTurnMsgIDs.set(chatID, messageID);
}

/** Drop the in-flight marker (turn persisted or finished, or chat removed). */
export function clearLiveTurnMessage(chatID: string): void {
  liveTurnMsgIDs.delete(chatID);
}

/** The id of the chat's unpersisted in-flight assistant message, if any. */
export function liveTurnMessage(chatID: string): string | undefined {
  return liveTurnMsgIDs.get(chatID);
}

export function appendChunk(
  chatID: string,
  messageID: string,
  delta: string,
  isReasoning: boolean,
  blockIndex: number,
  subtaskID: string,
  seq = 0,
  refusal?: RefusalInfo,
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  // Snapshot dedup: a chunk the connect-time turn_state already folded in must not
  // double-append.
  const wm = snapshotSeqs.get(chatID);
  if (wm?.messageID === messageID && seq > 0 && seq <= wm.seq) {
    return;
  }
  stampActivity(chatID);
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  let isNew = false;
  if (msg === undefined) {
    noteResidentMutation(s);
    msg = { id: messageID, role: "assistant", ts: Date.now(), content: "", blocks: [] };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
    isNew = true;
    // A chunk that beat its own message_created. One of the three doors an unpersisted
    // message comes through, so it marks the turn like the other two — otherwise a refetch in
    // that window cannot tell this message from one the server deliberately omitted.
    noteLiveTurnMessage(chatID, messageID);
  }
  let refusalStamped = false;
  if (refusal !== undefined && msg.refusal === undefined) {
    // Model refusal: the tagged chunk marks the whole turn. Stamped once; forces a full
    // repaint so the message-level callout mounts, since per-block signals only carry text.
    msg.refusal = refusal;
    refusalStamped = true;
  }
  if (isReasoning) {
    msg.reasoning = (msg.reasoning ?? "") + delta;
  } else {
    msg.content = (msg.content ?? "") + delta;
  }
  // The server guarantees consecutive chunks of the same kind and subtask share a
  // block_index; a tool_call, a kind switch, or a subtask switch bumps to a new one.
  msg.blocks ??= [];
  const blockKind = isReasoning ? "thinking" : "text";
  let newBlock = false;
  let padRepaired = false;
  if (msg.blocks[blockIndex] === undefined) {
    padBlocks(msg.blocks, blockIndex);
    msg.blocks.push({
      type: blockKind,
      ...subtaskField(subtaskID),
      ...(isReasoning ? { thinking: delta } : { text: delta }),
    });
    newBlock = true;
  } else {
    const existing = msg.blocks[blockIndex];
    // A PAD'S KIND IS A GUESS (see padBlocks), so the first real delta for the slot decides
    // it. Without this the guess stuck and a thinking delta merged into a `text` pad rendered
    // an empty row with its reasoning dropped outright.
    if (isPadBlock(existing)) {
      existing.type = blockKind;
      Object.assign(existing, subtaskField(subtaskID));
      padRepaired = true;
    }
    if (isReasoning) {
      existing.thinking = (existing.thinking ?? "") + delta;
    } else {
      existing.text = (existing.text ?? "") + delta;
    }
  }

  if (isNew) {
    // A message the store has never seen: the renderer must mount its row. Not exempted for a
    // step below — a step's first frame really does open a headerless turn card in the
    // launching chat, and that card has to mount.
    scheduleMessages(chatID, "shape");
    return;
  }
  // A WORKFLOW STEP's blocks are DROPPED by the dispatcher: nothing to mount, nothing to
  // re-type, so a delta for one needs no structural pass.
  const stepCause = droppedFrameCause(subtaskID);
  if (refusalStamped) {
    // Message-level FACT changed: the refusal feeds deriveOutcome and its callout mounts on a
    // full pass.
    scheduleMessages(chatID, "fact");
  }
  if (newBlock || padRepaired) {
    // A new block pushed, or a pad's guessed kind corrected: only the full pass mounts or
    // re-types a block.
    scheduleMessages(chatID, stepCause);
  }
  // Fine-grained first — only the block at blockIndex re-renders — then the per-message signal.
  const blockK = blockKey(messageID, blockIndex);
  const blockMap = isReasoning ? blockThinkingSigs : blockTextSigs;
  const blockSig = blockMap.get(blockK);
  if (blockSig !== undefined) {
    const fullText = isReasoning
      ? (msg.blocks[blockIndex]?.thinking ?? "")
      : (msg.blocks[blockIndex]?.text ?? "");
    blockSig.value = { full: fullText, delta };
    // Pure growth of a MOUNTED block: the signal effect painted the text, so this paint
    // refreshes tail bookkeeping only.
    scheduleMessages(chatID, "chunk");
  }
  if (isReasoning) {
    const sig = streamingReasoningSigs.get(messageID);
    if (sig !== undefined) {
      sig.value = msg.reasoning ?? "";
      scheduleMessages(chatID, "chunk");
    } else if (blockSig === undefined) {
      // Signal-absent fallback: nothing is mounted to carry the text, so the full pass is what
      // puts it on screen — unless nothing is MEANT to be, which is a dropped step.
      scheduleMessages(chatID, stepCause);
    }
  } else {
    const sig = streamingTextSigs.get(messageID);
    if (sig !== undefined) {
      sig.value = msg.content ?? "";
      scheduleMessages(chatID, "chunk");
    } else if (blockSig === undefined) {
      // Signal-absent fallback, as above.
      scheduleMessages(chatID, stepCause);
    }
  }
}

/** Attach licensed-code attributions to an in-flight assistant message. The server sends
 *  the full deduped list each time, so replace rather than append. No-op if the target is
 *  not resident — the refs still persist server-side and render on reload. */
export function setCodeReferences(chatID: string, messageID: string, refs: CodeReference[]): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  if (idx === -1) {
    return;
  }
  const msg = s.messages[idx];
  if (msg === undefined) {
    return;
  }
  msg.code_references = refs;
  bumpMessages(chatID);
}

export function upsertToolCall(
  chatID: string,
  messageID: string,
  call: ToolCall,
  blockIndex: number,
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  if (msg === undefined) {
    noteResidentMutation(s);
    // HONOUR `blockIndex` here too: hard-coding the tool_use block at index 0 left a turn
    // whose first frame was a `tool_call` at index 2 misaligned for the rest of the turn.
    const blocks: Block[] = [];
    padBlocks(blocks, blockIndex);
    blocks[blockIndex] = {
      type: "tool_use",
      tool_call_id: call.id,
      ...subtaskField(call.agent_subtask_id),
    };
    msg = {
      id: messageID,
      role: "assistant",
      ts: Date.now(),
      content: "",
      tool_calls: [call],
      blocks,
    };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
    // A new message: the full pass mounts its row. Synchronous, because an arrival must be in
    // the DOM before the frame that announced it.
    bumpMessages(chatID, "shape");
    return;
  }
  msg.tool_calls ??= [];
  msg.blocks ??= [];
  // The existing-message arms take the cheap cause for a call the transcript draws nothing
  // for; the `msg === undefined` arm above deliberately does not. No card is mounted for a
  // step's call, so `ensureToolCallSig` is never reached for one and every
  // `tool_call_update` for every step falls into the signal-absent arm at the foot.
  const stepCause = droppedFrameCause(call.agent_subtask_id ?? "");
  const tcIdx = msg.tool_calls.findIndex((tc) => tc.id === call.id);
  if (tcIdx === -1) {
    msg.tool_calls.push(call);
    // First sighting of this tool call: pin it to the server's block index, REPLACING whatever
    // pad sits there. A conditional push is false immediately after its own padding above, so
    // the tool_use block was never written AND `msg.blocks.length` stayed one short of the
    // server's next index — one hole early in a turn corrupted every call after it. A PAD is
    // overwritten; a REAL block is not, because replacing a standing text or thinking block
    // would delete transcript content the server already streamed.
    padBlocks(msg.blocks, blockIndex);
    const standing = msg.blocks[blockIndex];
    if (standing === undefined || isPadBlock(standing)) {
      msg.blocks[blockIndex] = {
        type: "tool_use",
        tool_call_id: call.id,
        ...subtaskField(call.agent_subtask_id),
      };
    }
    // `stepCause` decides the pass: a drawn call needs the full one that mounts its card, a
    // dropped step's needs none.
    scheduleMessages(chatID, stepCause);
    return;
  }
  const prev = msg.tool_calls[tcIdx];
  msg.tool_calls[tcIdx] = call;
  // Late identity attachments are STRUCTURAL, not status updates (the server attaches both
  // ids on updates when the initial call lacked them): a first `agent_subtask_id` decides
  // container MEMBERSHIP, which is the BLOCK's field, and the tool fast path never re-homes
  // a card; a first `workflow_id` is what the block dispatcher keys a run card on.
  const subtaskAttached =
    nonEmptyStr(call.agent_subtask_id) && !nonEmptyStr(prev?.agent_subtask_id);
  if (subtaskAttached) {
    const blk = msg.blocks.find((b) => b.type === "tool_use" && b.tool_call_id === call.id);
    if (blk !== undefined) {
      Object.assign(blk, subtaskField(call.agent_subtask_id));
    }
    scheduleMessages(chatID, stepCause);
  }
  if (nonEmptyStr(call.workflow_id) && !nonEmptyStr(prev?.workflow_id)) {
    scheduleMessages(chatID, "fact");
  }
  const sig = toolCallSigs.get(toolCallSigKey(chatID, call.id));
  if (sig !== undefined) {
    sig.value = call;
    // The card's own effect repaints it; the tool paint refreshes the owning turn's keyed
    // state only — never a projection, never a mount.
    scheduleMessages(chatID, "tool", messageID);
  } else {
    // Signal-absent fallback: nothing is mounted for this card, so the full pass puts its
    // update on screen — unless nothing is MEANT to be, which is a dropped step.
    scheduleMessages(chatID, stepCause);
  }
}

// --- Utilities ---
export function contextSizeFor(modelID: string): number {
  return MODEL_CONTEXT_SIZES[modelID] ?? 0;
}

export function defaultUsage(): Usage {
  return {
    context_pct: 0,
    context_size: 0,
    credits: 0,
    turn_count: 0,
    last_turn_ms: 0,
    has_real_data: false,
  };
}
