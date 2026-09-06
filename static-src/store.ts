// ---------------------------------------------------------------------------
// Client-side session store.
//
// Sessions live in a createCollection<Session> (ordered, keyed by id, with a
// per-session signal + a structure/order signal). `activeId` is a signal;
// `activeSession` is a computed that tracks the active session's per-entity
// signal — subscribers read it to react only to active-session changes.
//
// MESSAGES are deliberately NOT a collection: each session owns a Message[]
// with sub-message (block/tool) streaming signals in store-signals.ts,
// finer-grained than a per-message signal. The messages renderer subscribes
// to its chat's `messagesVersionOf` signal (coarse "list shape changed") while
// the SignalMaps do the per-block/per-tool fine-grained work. Streaming paths
// coalesce renders per chat via scheduleMessages (queueMicrotask).
// ---------------------------------------------------------------------------

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
// Straight from the generated wire rather than through turns.ts, which imports
// this module: a type-only import adds no runtime edge, but keeping the source
// the same one turns.ts reads means there is one spelling of the enum.
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
// One version signal per chat (`ensure(id, 0)`). The transcript effect and the
// task-list pill subscribe to the ACTIVE chat's signal only, so a background
// chat's stream cannot repaint the visible transcript (the multi-tab freeze
// class), and a background consumer (the subagent page) tracks its own chat and
// gains live updates. Per-delta paths coalesce per chat per microtask via
// scheduleMessages; list-shape writers bump synchronously — the renderer's keyed
// reconcile must see an append before the frame it was announced in.
//
// EVERY bump carries a RenderCause, declared at the BRANCH that knows what
// changed, and the renderer branches on it instead of inferring change from
// array identity (which is either wrong — ingestMessage replaces in place — or
// O(resident history)). Causes MERGE upward per chat until the flush.
const messagesVersionSigs = new SignalMap<number>();

/** What a version bump was FOR — what the renderer may skip.
 *
 *   - `chunk`: pure text growth of a MOUNTED block; its signal effect already
 *     painted the text, so paint refreshes tail bookkeeping only.
 *   - `tool`: an existing tool call's update; the owning message's keyed
 *     update refreshes its card — no projection, no reconcile.
 *   - `fact`: a transcript fact flipped (thinking, turn latches, refusal, run
 *     identity); full projection + reconcile (facts feed deriveOutcome/isLive).
 *   - `shape`: the message list's structure changed; the full pass.
 *   - `load`: the window was REPLACED by a fetched page (`store-load.ts`, both
 *     the newest page and an older-page prepend). The full pass, plus the one
 *     fact the array cannot state — those rows are a REPLAY rather than a tail
 *     that arrived here, so none of them is an arrival.
 *
 *  A store branch that mutates no rendered state declares NO cause: the
 *  unknown-chat and snapshot-dedup returns in appendChunk, the
 *  message-not-resident return in setCodeReferences, and setTurnSummary's
 *  changed === false arm. */
export type RenderCause = "chunk" | "tool" | "fact" | "shape" | "load";

/** `load` outranks `shape` even though both mean the full pass: shape's work is
 *  contained in load's, while load's STATEMENT is not recoverable from the array,
 *  so a merge that dropped it would read a refetched window as an appended tail
 *  and animate every row of a reopened conversation. */
const CAUSE_RANK: Record<RenderCause, number> = {
  chunk: 0,
  tool: 1,
  fact: 2,
  shape: 3,
  load: 4,
};

/** The per-chat cause accumulator: what the NEXT flush will paint for.
 *  `msgID` survives only while every merged cause is `tool` for ONE message —
 *  the keyed-update address; tool causes for two messages escalate to shape. */
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
    // Two tool updates in one tick: same message stays addressable, different
    // messages escalate — one keyed update cannot refresh two turns.
    if (cur.msgID !== msgID) {
      pendingCause.set(chatID, { cause: "shape" });
    }
    return;
  }
  if (CAUSE_RANK[cause] > CAUSE_RANK[cur.cause]) {
    pendingCause.set(chatID, msgID !== undefined ? { cause, msgID } : { cause });
  }
}

/** THIS chat's transcript version. Reading `.value` inside an effect subscribes
 *  it to the chat's transcript changes and nothing else's. */
export function messagesVersionOf(chatID: string): Signal<number> {
  return messagesVersionSigs.ensure(chatID, 0);
}

/** The cause the current version was bumped for. `paint()` reads it (untracked)
 *  right after reading the version; a chat switch or an absent record reads as
 *  `shape`, the full pass. */
export function renderCauseOf(chatID: string): { cause: RenderCause; msgID?: string } {
  return flushedCause.get(chatID) ?? { cause: "shape" };
}

/** Flush the accumulator into a version bump. The SYNC path paints the MERGED
 *  cause and clears it, so a pending chunk flush never inherits a later shape;
 *  the microtask flush skips a chat whose accumulator is already empty. */
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

/** Bump `chatID`'s transcript version synchronously.
 *
 *  List-shape writers here and the out-of-module callers (`store-load.ts`
 *  after a page fetch, `chat-search.ts` around a reveal, fold-state writers,
 *  run-store's settle path) use this; per-delta paths go through
 *  `scheduleMessages`. The timing split is real: a message arriving must be in
 *  the DOM before the frame it was announced in, while a tick's worth of
 *  deltas should collapse into one repaint. */
export function bumpMessages(chatID: string, cause: RenderCause = "shape"): void {
  mergeCause(chatID, cause);
  flushCause(chatID);
}

/** Chats with a bump parked on the next microtask. `removeChat` deletes its id
 *  here, so a pending flush cannot re-mint the version signal it just cleared. */
const messagesScheduled = new Set<string>();

/** Coalesce one chat's per-delta bumps into a single repaint per microtask,
 *  merging causes upward as they accumulate. */
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
 *  ONE predicate for the store's whole "will this be drawn" question, keyed on the
 *  PARSE because that is what `messages-blocks.ts` `isDroppedStep` drops on — the
 *  `wf:` PREFIX is a wider set (`wf::path`, `wf:id:`, a future id shape), and those
 *  ids reach the delegate-box fallback and DO render, so treating one as dropped
 *  would leave its box unmounted until an unrelated full pass arrived. `isStepSubtask`
 *  answers the other question (is this the chat's own work) for `handlers/messages.ts`.
 *
 *  A dropped block needs no structural work, so the arms below take `chunk` rather
 *  than scheduling a full projection plus reconcile per step frame — on the very
 *  transcript the drop exists to make cheaper. The RUN TAB still repaints, because
 *  `flushCause` bumps the same per-chat version its view effect reads whatever the
 *  cause. */
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
/** Ordered keyed collection of sessions. Structure ops fire `sessions.ids`;
 *  per-session field writes (via collection.update) fire `signalFor(id)`.
 *  Module-private: consumers go through the typed accessors/mutators below
 *  and the `activeSession` computed. */
const sessions = createCollection<Session>((s) => s.id);
/** The active chat id. */
const activeId = signal("");
/** Active session, tracking the active id AND the active session's signal.
 *  Active-session UI subscribers read `activeSession.value` to re-render only
 *  when the active session (or which session is active) changes. */
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
/** The active chat id as a TRACKED read: an effect calling this re-runs when the
 *  active chat changes. `getActiveId` stays the untracked peek. */
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

/** One chat's session as a TRACKED read: an effect calling this re-runs when
 *  THIS session's fields change and when the session set's structure changes
 *  (the row appearing or vanishing) — never on another session's field churn.
 *  The tab strip's per-row effects (chat.ts) are the consumer. */
export function watchSession(id: string): Session | undefined {
  touch(sessions.ids);
  return sessions.signalFor(id)?.value;
}

/** Whether `chatID`'s transcript already holds `messageID`.
 *
 *  The ACCEPTANCE test for a prompt this client sent, and the reason it is a
 *  store question rather than a caller's loop: `CmdPrompt` persists the user row
 *  and broadcasts it BEFORE the ACP call, and nothing rolls that back, so an echo
 *  of our own message id is proof the server took the prompt whatever the POST
 *  went on to answer. Two callers rest on that — the dead-POST rescue in
 *  actions/chat.ts and the failed-send restore in submit.ts.
 *
 *  A linear scan, deliberately not `msgIndex`: that index is a lookup cache with
 *  its own rebuild points, and a read this rare does not earn a dependency on
 *  when it was last refreshed. */
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
  // Setting activeId re-derives `activeSession` (and the messages renderer,
  // which the transcript effect tracks via watchActiveId, repaints the new
  // chat's #messages). Without this the renderer would keep the previous
  // chat's DOM until something else bumped a version.
  activeId.value = id;
  stampActivity(id);
}

// --- Eviction: reclaim idle background transcripts ---
//
// A long-lived tab accumulates one paginated message window per chat ever
// opened. The sweep below evicts the window of a chat nobody can be reading —
// the session ROW survives with its header data, and `residency` is what makes
// the next activation refetch instead of trusting the hole.

/** How often the sweep looks for idle chats. */
export const EVICT_SWEEP_MS = 5 * 60 * 1000;
/** How long a chat must sit without activity before its window is evictable. */
export const EVICT_IDLE_MS = 30 * 60 * 1000;

/** When each chat last did anything a reader could be following: activation,
 *  a turn starting or ending, a message or chunk landing. A side table rather
 *  than a Session field so a per-chunk stamp never churns the session signal.
 *  A chat with NO entry is treated as active (err toward keeping). */
const lastActivity = new Map<string, number>();

function stampActivity(chatID: string): void {
  if (chatID !== "") {
    lastActivity.set(chatID, Date.now());
  }
}

/** Externally-owned reasons a chat must not be evicted, registered by the
 *  composition root so this module stays a leaf (importing tabs.ts or
 *  run-store.ts from here would invert the dependency direction). app.ts
 *  registers the executing-run and subagent-tab predicates; the parked-view
 *  predicate registers here when parked views land. Nothing registered means
 *  no external exemption — the predicate set defaults to false. */
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

/** Whether the sweep may evict this chat's window. FIVE exemptions, each alone
 *  decisive: the active chat, a busy/streaming chat, a chat with an EXECUTING run
 *  (registered predicate), a parked view, and an open subagent tab projecting
 *  the chat (both registered predicates). */
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
  // Visibility-aware: a hidden tab's clock keeps running but nothing is
  // reclaimed until the reader returns — eviction is for THEIR memory, and a
  // wake-up burst of refetches on tab return is the cost being avoided. The
  // capability read (never a typeof-object test) only skips on a positive
  // "hidden"; a document-less runtime sweeps.
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

/** Drop every per-message streaming signal a chat's resident messages minted:
 *  block signals, the per-message text/reasoning pair, and tool-call signals.
 *  The store-level half of the leak fix — the renderer's disposeMessage covers
 *  rows that unmount, and this covers a chat leaving WHOLE (removal, eviction),
 *  where no reconcile ever runs for background rows. */
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

/** Evict a chat's message window, keeping the session ROW (header data stays:
 *  name, model, usage, message_count). Everything keyed by the window goes with
 *  it — the msg index, the per-message signals, the snapshot watermark, and the
 *  chat's version signal + scheduled marker (the same set `removeChat` drops).
 *  `residency: "evicted"` is what the next activation keys its refetch on, and
 *  `has_more` re-derives from the server count so pagination furniture stays
 *  honest until then. */
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

/** Background ingest on an evicted chat leaves it PARTIAL: some messages are
 *  resident, the window around them is not, so only a successful newest-page
 *  load may claim `loaded` again. Called at every site that pushes a NEW
 *  message row into a session. */
function noteResidentMutation(s: Session): void {
  if (s.residency === "evicted") {
    s.residency = "partial";
  }
}

// --- Sync epoch: which loads survived the last transport gap ---

/** Counts transport replay gaps. Plain module state rather than a signal:
 *  it is consulted at activation and fetch time, never rendered. */
let syncEpochCount = 0;

export function syncEpoch(): number {
  return syncEpochCount;
}

/** The `transport:gap` door's half: events were lost, so every window and
 *  session-wide index loaded under the old epoch is a claim this client can no
 *  longer support. Bumped BEFORE any heal starts — a fetch in flight at that
 *  moment captured the old number and stays stale, because its answer may
 *  predate events the gap dropped. */
export function bumpSyncEpoch(): void {
  syncEpochCount++;
}

/** The activation refetch gate: a window is trustworthy only if a newest-page
 *  load succeeded (residency), and no transport gap has intervened since that
 *  load's request went out (loadedEpoch). An absent loadedEpoch never equals
 *  the counter, so a never-loaded chat is stale by construction. */
export function transcriptStale(s: Session): boolean {
  return s.residency !== "loaded" || s.loadedEpoch !== syncEpochCount;
}

export function isThinking(id: string): boolean {
  return get(id)?.thinking ?? false;
}

/** Whether a chat holds no conversation at all: nothing on the record and
 *  nothing resident.
 *
 *  BOTH halves are load-bearing, which is why this is one predicate rather than
 *  a field read at each site. `message_count` is the server's count and is 0 on
 *  a chat it has never heard of (every new chat is client-side only until its
 *  first prompt), while `messages` is the paginated window a `session/load`
 *  replay can fill before a header refresh restates the count.
 *
 *  An absent chat is empty: an id with no row holds no conversation either, so
 *  callers gating on "is there something here" need no second null check. */
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
    // A new turn invalidates every verdict the PREVIOUS turn left behind: the
    // agent's declared status (e.g. a lingering waiting_on_user), which it
    // re-declares via focus updates as the turn progresses, and the two outcome
    // latches, because work is happening again. All of them are
    // latched-until-next-turn for the same reason, so they clear in one place.
    if (v) {
      delete next.agent_status;
      delete next.agent_status_text;
      delete next.turn_failed;
      delete next.turn_done;
      // The server's last liveness statement joins them, for the same reason: it
      // described the PREVIOUS turn, and a new turn makes it stale. Left standing,
      // a `turn_open: false` from before this turn started would make `turnLive`
      // fall back to `thinking` alone, which is what this whole field exists to
      // stop being the only input.
      delete next.turn_open;
    }
    return next;
  });
  // Transcript fact: `thinking` feeds the live-turn derivation the renderer
  // paints from, so the flip repaints the chat's transcript.
  scheduleMessages(id, "fact");
}

/** Record the server's statement about whether this chat has a turn open.
 *
 *  Written by `store-load.ts` from `GET /api/chats/{id}`'s `turn_open` (newest page
 *  only — an older-page fetch is a scroll-up and asserts nothing about liveness) and
 *  by the `turn_ended` handler, which sets false because a closer ran, so the record
 *  is final and the carrier's own persist echo is already on its way. */
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
 *  THE ONE READER of `turn_open`, and the projection's liveness input. It composes
 *  two facts on two different clocks, which is why neither alone is the answer:
 *  `thinking` is this client's own memory of a stream it has watched (true only once
 *  a frame arrives, so false through every reload), and `turn_open` is the server's
 *  last statement (a fact when it arrives, stale afterwards).
 *
 *  The defect it fixes: between `GET /api/chats/{id}` painting and the HELD
 *  `turn_state` frame releasing, a turn whose reply is still in the server's
 *  in-memory buffer reads `thinking: false`, derives `unknown` — "nothing closed this
 *  turn" — and mounts a footer glyph plus a `.turn-notice` row before the running
 *  frame corrects it. The client was deriving a TERMINAL verdict during a window in
 *  which it provably could not know one. With liveness known, that window collapses
 *  into `running`, which is the TRUTH, and `running`'s treatment already is "no
 *  settled mark" on every surface — so nothing needed a suppression rule.
 *
 *  `turn_open`'s lifetime, three writers and one deliberate non-writer:
 *
 *   - `store-load.ts` `loadMessages`, newest page only: writes the response's value.
 *     The statement arrives with the window it qualifies, which is what removes the
 *     ordering — there is no gap between the transcript painting and the verdict
 *     arriving, because they are one payload.
 *   - `handlers/turn.ts` `turn_ended`: writes false. It goes at the CALL SITE rather
 *     than inside `clearTurnState`, because that function also runs on
 *     `transport:gap`.
 *   - `setThinking(id, true)`: DELETES it, beside the two outcome latches, because a
 *     new turn invalidates every verdict the previous turn left behind.
 *   - the `transport:gap` handler: writes NOTHING, deliberately. Clearing it there
 *     would drop the last server statement at the exact moment `thinking` is also
 *     cleared, which is the gap-path flash this exists to remove; the refetch that
 *     follows restates it.
 *
 *  Three residuals, each failing in the safe direction (no settled mark rather than a
 *  wrong one): a resident window reactivated after a restart repaints from the store
 *  with the previous `turn_open` for one round trip; a `turn_ended` lost to a gap
 *  leaves it true until the next refetch, so the newest turn reads `running`; and an
 *  older server sends no field, which decodes absent and behaves as before. */
export function turnLive(s: Session): boolean {
  return s.thinking || s.turn_open === true;
}

/** Latch that this chat's last TURN failed — an outcome `outcomeLatch` grades
 *  `failed`. Cleared by the next `setThinking(id, true)`, so there is no explicit
 *  unlatch — a failure stands until work resumes.
 *
 *  A turn is the ONLY producer, and the three other callers re-derive that same
 *  verdict through that one table rather than widening it: `relatchTurnVerdict`
 *  off the persisted `turn_outcome`, `latchFieldsFor` off the chat header, and
 *  `chat.send_prompt` restoring the boolean snapshot it took. The
 *  `error` handler used to latch this for every frame naming the chat, and it
 *  stopped touching turn state when `endsTurn` was removed (handlers/turn.ts
 *  records that), so no `error`-frame producer remains — which is the breadth
 *  tabs.ts's narrow "turn failed" dot phrase is written against. */
export function setTurnFailed(id: string): void {
  const s = get(id);
  if (s === undefined || s.turn_failed === true) {
    return; // no-op: don't churn the session signal on a replayed error frame
  }
  sessions.update(id, (prev) => ({ ...prev, turn_failed: true }));
  scheduleMessages(id, "fact"); // feeds the turn-outcome derivation
}

/** Clear the failure latch without starting a turn. Only the transport-gap
 *  reconciler needs this: after a dropped stream the client cannot tell which
 *  of its latched verdicts still hold, and it clears `thinking` and
 *  `agent_status` there for the same reason. */
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

/** Latch that this chat's last turn finished.
 *
 *  The mirror of `setTurnFailed`, and it exists for the same reason: `turn_ended`
 *  always arrives, while the agent's own `completed` status only arrives when the
 *  model calls its status tool. Without the latch, "this chat finished" held only
 *  for the turns where it did.
 *
 *  The only condition the caller applies is `outcomeLatch`'s grade. It used to
 *  apply a second one — skip the chat the reader is watching — and that is what
 *  made a turn completing in front of you fall back to hollow `idle`. */
export function setTurnDone(id: string): void {
  const s = get(id);
  if (s === undefined || s.turn_done === true) {
    return; // no-op: don't churn the session signal on a replayed turn_ended
  }
  sessions.update(id, (prev) => ({ ...prev, turn_done: true }));
  scheduleMessages(id, "fact");
}

/** Clear the finished latch. ONE caller, the transport-gap reconciler, for the
 *  same reason it clears the failure latch — after a dropped stream the client can
 *  no longer support either claim. The ordinary clear is not a call at all: the
 *  next `setThinking(id, true)` drops it with every other verdict the previous
 *  turn left behind.
 *
 *  Opening the chat deliberately does NOT clear it. Seeing a finished turn does
 *  not un-finish it, and the green dot standing until the next turn is what
 *  web-terminal-kiro's engine-side latch does. What keeps a watched chat out of
 *  the title count is the acknowledgement pass in attention.ts, not this. */
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

/** Map a persisted turn outcome onto the latch it sets, or "" for an outcome that
 *  latches nothing.
 *
 *  ONE table, and ALL THREE producers of this verdict call it: the live
 *  `turn_ended` handler (handlers/turn.ts), `relatchTurnVerdict` re-deriving from
 *  the loaded TRANSCRIPT, and `latchFieldsFor` deriving from the chat HEADER. All
 *  three used to spell the mapping out for themselves, and they disagreed — a
 *  second enumeration of one vocabulary with nothing holding the copies together,
 *  which is what put an interrupted turn on `idle` live and on `failed` after a
 *  reload of the same chat. `attention.ts`'s favicon and title cue reads the dot
 *  this feeds, so it inherits whatever the three agree on.
 *
 *  It DERIVES from `severityOf` rather than enumerating outcomes itself, and that
 *  is the fix for a measured defect rather than tidiness. Every arm here used to be
 *  hand-written, and `interrupted` landed in the default: so a turn stopped by a
 *  network error — which `messages-events.ts` renders as a red divider,
 *  `turnFailureText` puts in the turn's notice, `29-turns.css` gives a filled glyph
 *  and a lead word, and `fold-state.ts` promises never to fold — latched nothing at
 *  all, and `tabStatusFor` fell through `turn_failed` → `thinking` → `agent_status`
 *  → `turn_done` to `idle`. `12-tabs.css` paints `idle` as a transparent disc with a
 *  1.5px ring, so the reported symptom was a chat with a clear inline error message
 *  showing an EMPTY CIRCLE beside it, and no favicon cue either.
 *
 *  THE HOLLOW RING MEANS THE CHAT HAS NOT INITIATED (user ruling, 2026-09-04),
 *  which is what decides the last two arms. A chat that has run a turn may never
 *  paint `idle` — whatever became of that turn — so only two things reach the
 *  floor: a chat with no turn at all, and a record with no outcome to read (a
 *  transcript written before the field existed, which the same ruling accepts,
 *  because there is genuinely no state to pull).
 *
 *  So each severity, and note that THREE of the four latch:
 *   - `clean` finished with an answer.
 *   - `broken` did not, and someone should look.
 *   - `stopped` — a cancel the user asked for, or an `unknown` stop reason —
 *     latches DONE, because a turn ended here. `done` is the transport's verdict
 *     that a turn FINISHED and never the agent's claim that it succeeded (see
 *     `tabStatusFor`, and `tabs.ts`'s phrase for it, "turn finished"), so it is
 *     the honest answer for a stop: `failed` would blame the user for cancelling,
 *     and `idle` would report that nothing ever happened in a chat they watched
 *     work. `runStatusFor` below already made this exact call for a run's dot,
 *     with the same reasoning, so the two cannot disagree about what a
 *     cancellation means. THIS SUPERSEDES an earlier ruling that a cancel
 *     "finished nothing" and so should latch neither: that reasoning was about
 *     whether green over-claims success, and it lost to the ring's own meaning —
 *     the reader cannot tell a cancelled chat from one that never started.
 *     It reaches the ATTENTION CUE as well, ratified separately on the same day:
 *     `done` is a `CueStatus`, so both of these now raise the favicon variant and
 *     the `(N)` title count on a device that has not observed them. See
 *     `attention.ts`'s `CueStatus` for why that is one rule rather than two.
 *   - `running` alone latches nothing, and does not reach the floor either: a
 *     turn in flight is `working` through `thinking`. It cannot arrive here from
 *     a persisted record at all (a stored message never carries it), so the arm
 *     exists to keep a live-projection value from claiming a verdict. */
export function outcomeLatch(outcome: TurnOutcome | undefined): "done" | "failed" | "" {
  // ABSENCE is answered here and never by `severityOf`, which grades OUTCOMES: its
  // default arm reads an unrecognised value as `stopped` so a value the wire adds
  // later cannot read as a turn that worked, and `undefined` lands in that same arm
  // without meaning the same thing. "The wire said something I cannot grade" is a
  // turn that ran; "the record carries no outcome" is the one case the hollow ring
  // is still correct for. Grading them alike latched `done` on every legacy record.
  //
  // `undefined` is the whole of absence and no empty-string arm is needed: the
  // server drops the field with `omitempty`, and `TurnOutcome` is a generated
  // closed union whose decoder rejects any value not in it, so `""` cannot arrive.
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

/** The latch fields to spread into a `Session` being rebuilt from a `ChatHeader`.
 *
 *  This is what makes a chat tab's dot survive a reconnect. The two latches are
 *  CLIENT memory, so a fresh page load, a brand-new browser session and a
 *  transport gap all arrive with nothing to derive them from — and every door that
 *  rebuilds a Session from a header alone (`loadList` on boot and on every SSE
 *  `connected`, `upsertHeader` on a `chat_created` / `chat_updated` frame) used to
 *  produce a session with neither latch set, so `tabStatusFor` fell through to
 *  `idle`. `idle` paints a hollow ring where `done` paints a solid fill, which is
 *  the "empty circle" a finished turn came back as.
 *
 *  Three rules, in order, and the order is the whole content:
 *
 *   1. AN EXISTING LATCH IS CARRIED OVER UNCHANGED. It was set by a live
 *      `turn_ended` on this page, so it is newer than anything a header read can
 *      carry. Both are carried when both are somehow set, so this can only ever
 *      preserve a latch and never clear one — which is what lets the caller spread
 *      the result over an existing session safely.
 *   2. A LIVE TURN SEEDS NOTHING. `thinking` invalidates every prior verdict, and
 *      the header's outcome describes the turn BEFORE the one now running, so
 *      seeding it would paint `done` over a working chat on a mid-turn reload.
 *   3. OTHERWISE THE HEADER'S OUTCOME DECIDES, through `outcomeLatch`.
 *
 *  Built conditionally rather than with `undefined` values, because
 *  `exactOptionalPropertyTypes` makes an explicit `undefined` a different thing
 *  from an absent key — and an explicit `undefined` spread over an existing
 *  session would DELETE the latch rule 1 exists to keep. */
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

/** Re-derive the outcome latches from the PERSISTED record: the newest message
 *  carrying a `turn_outcome` says how this chat's last turn ended, and the
 *  latches are re-set from it exactly as the live `turn_ended` handler would
 *  have set them.
 *
 *  The latches are client memory, so every page load and every transport gap
 *  dropped them — and a chat with a live workflow floods the replay ring with
 *  step frames, which made every reconnect on such a chat a gap. The measured
 *  symptom was a finished turn's green dot falling to the hollow idle ring the
 *  moment the connection blinked, on exactly the chats doing background work.
 *  The outcome IS durable (persisted at finalize, survives reload), so the
 *  latch can be too: called after every newest-page message load, which is the
 *  gap door's own heal path and every activation.
 *
 *  Refuses to overwrite: a live turn (`thinking`) invalidates every prior
 *  verdict, and a latch already set is newer than anything the page carries.
 *  Which outcome latches what is `outcomeLatch`'s to say, not this function's. */
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
    // Same table the header seed reads, so the transcript-derived and
    // header-derived paths cannot disagree about what an outcome means.
    const latch = outcomeLatch(outcome);
    if (latch === "done") {
      setTurnDone(id);
    } else if (latch === "failed") {
      setTurnFailed(id);
    }
    return;
  }
}

/** Derive the chat tab's activity-dot state. ONE rule, shared by the store
 *  effect (chat.ts) and the turn_ended / error handlers, so no two writers can
 *  disagree about what a chat's dot means.
 *
 *  Order is precedence, most urgent first, and each rung earns its place:
 *
 *   - `input` outranks `working` because the two COEXIST — a permission ask
 *     arrives mid-turn and `thinking` stays true until the turn ends — so
 *     putting working first would mask every ask behind the state that needs
 *     nothing from anyone. That masking is a documented cost elsewhere in the
 *     app: the push notice for a permission cannot be silenced precisely
 *     because "a background chat waiting on an approval renders identically to
 *     one that is working". This is the fix for that, so the order is the
 *     whole point rather than a detail.
 *   - `failed` and `working` are mutually exclusive by construction (the error
 *     handler clears thinking, and a new turn clears the latch), so their
 *     relative order is inert; failed is listed first so a future path that
 *     sets both reports the failure rather than hiding it.
 *   - `waiting` outranks `done` because a chat that asked you something wants
 *     more than one that merely finished, and the two CAN coexist now: the
 *     agent can declare waiting_on_user and then the turn can end, which sets
 *     the `turn_done` latch under a live waiting.
 *   - `done` has TWO producers and they are not equivalent. The agent's
 *     `completed` status is the higher-fidelity signal and the one to prefer,
 *     but it only arrives when the model calls `update_session_information`, so
 *     a turn ending without one used to fall to `idle` — which made "this chat
 *     finished" true only sometimes. `turn_done` is the transport's own verdict
 *     (`turn_ended` always arrives) and it is what makes the promise hold; see
 *     types.ts for its clear discipline. Neither producer asks who is watching:
 *     a turn that completes on the tab in front of you paints green there too,
 *     which is the state web-terminal-kiro's engine latches the same way.
 *   - `idle` MEANS THE CHAT HAS NOT INITIATED (user ruling, 2026-09-04), and it
 *     is the FLOOR for a real chat rather than an absence — a chat tab always
 *     shows a dot, which keeps the strip's leading column aligned and makes
 *     "nothing has happened here" readable rather than inferred. Only an unknown
 *     chat yields "". Exactly three things reach it now, and a turn's OUTCOME is
 *     not among them whatever it was (`outcomeLatch` grades all six persisted
 *     outcomes to a latch): a chat before its first turn, a record carrying no
 *     outcome to read, and the window after a transport gap clears the latches
 *     before `loadList` re-seeds them from the header.
 *
 *  `pendingAsk` is passed rather than read because the queue of unanswered
 *  decisions lives in `decision-dock.ts`, which imports this module — so the
 *  dependency has to point the other way at the call site. */
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

/** The pause classes the dot vocabulary distinguishes.
 *
 *  A classified value rather than the raw `pauseReason`, so this module never learns
 *  KAS's pause sentences OR its node signals: `run-store.ts` owns that rule
 *  (`isNeedInputPark`, over the reason and the node tree both — its reason half is
 *  pinned cross-language against the server's own predicate) and hands the answer over
 *  already decided. */
export type RunPauseClass = "" | "need_input";

/** The same dot vocabulary for a workflow RUN, which owns a `run:<workflowId>` tab
 *  and has no `Session` behind it.
 *
 *  Its own function rather than a branch inside `tabStatusFor` because the inputs
 *  share not one field: a run has no `thinking`, no `agent_status` and no `turn_*`
 *  latch. What it shares is the OUTPUT vocabulary and the precedence, which is why
 *  it lives here, next to its sibling.
 *
 *  `pendingAsk` and `pause` are both passed rather than read, for the reason
 *  `tabStatusFor`'s is: `decision-dock.ts` and `run-store.ts` both import this
 *  module, so the reads have to happen at the call site.
 *
 *  THE FIVE STATES, and each mapping is a decision:
 *
 *   - `running` is the ONE state that holds a process, so it is the one that
 *     spins.
 *   - `paused` is `waiting` — stopped, not finished — EXCEPT for a step parked on
 *     a person, which is `input`. That is the same dot an unanswered card raises,
 *     because it is the same fact about the run; the two inputs exist because
 *     either can be present without the other, and the park is the one that
 *     survives a client that never received the card.
 *   - `failed` and `aborted` are `failed`; `cancelled` and an unrecognised
 *     terminal status are `done`, because the user asked for the first and the
 *     second is still an ending the toast names verbatim.
 *   - a status this client has not fetched paints NOTHING: not knowing yet is a
 *     different thing from knowing nothing is happening.
 *
 *  The `pause` argument is consulted ONLY in the `paused` arm, which is what keeps
 *  a stale reason on a finished run from painting it yellow. */
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

/** The same dot vocabulary for a SUBAGENT, whose tab is a `subagent` sub-tab under
 *  the chat that dispatched it.
 *
 *  A third function beside its two siblings rather than a branch inside either,
 *  for the reason `runStatusFor` is one: a delegate shares the OUTPUT vocabulary
 *  and nothing else. It has no `thinking`, no `agent_status`, no `turn_*` latch and
 *  no `pauseReason` — its whole state is its INVOCATION TOOL CALL's `ToolStatus`,
 *  which is a generated closed union, so the four arms below are exhaustive and no
 *  `default` is needed to catch a value the decoder would have rejected.
 *
 *  `undefined` means THIS CLIENT HOLDS NO INVOCATION for the delegate, which is a
 *  different statement from "it has not started": the invocation is persisted with
 *  its `agent_subtask_id`, so absence here is a resident-window fact (a chat whose
 *  messages have not been fetched, or a turn evicted from the paginated window)
 *  rather than anything about the delegate. It answers "" for the same reason
 *  `runStatusFor` answers "" for a run it has not fetched — not knowing is not the
 *  same as knowing nothing is happening — and it is answered FIRST so no arm below
 *  has to consider absence.
 *
 *  `pending` and `in_progress` are `isToolActive`'s pair, deliberately: that is
 *  what the transcript's own delegate card spins for, so the dot and the card
 *  cannot disagree about what in-flight means. `done` is the settled disc and
 *  carries the transport's "it finished" rather than any claim that it succeeded,
 *  exactly as it does for a chat and for a run. Nothing maps to `idle`: the hollow
 *  ring means a chat has not initiated (see `outcomeLatch`), and a delegate someone
 *  opened a tab for has certainly run. */
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

/** Record the agent-declared activity status/description for a chat
 *  (chat_status SSE, sourced from the KAS focus_update channel). Empty
 *  strings clear the respective field. */
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
//
// TWO FIELDS, TWO LIFETIMES, and that split is the whole shape here.
// `session.steers` is what the agent has NOT read: the bottom dock's rows, whose
// lifetime is the turn. `session.steer_marks` is what has LEFT the dock — read,
// or dropped unread at a boundary — rendered inside the turn transcript at the
// block it landed on, and whose lifetime is the loaded transcript. An entry moves
// from the first to the second and never back.
//
// The client writes INTENT and the server writes FACT. `recordSteerSent` is the
// intent half (a `pending` row on submit, under the id the client can derive) and
// `forgetSteer` un-writes it when the POST fails. Everything else is a frame:
// `steer_queued` confirms, `steer_injected` promotes, `steer_cleared` and every
// turn boundary drop. The confirm is idempotent in every branch, so the
// optimistic row and the confirmed one can never both render.
//
// What this replaced: a client-side FIFO with five mutators, a re-entrant drain
// guard, a 409-after-turn_ended race check and a promote-to-front used to jump
// the queue by CANCELLING the running turn. All of it existed to work around not
// being able to reach a live turn. Reactive because both are fields on the
// session, so pending-steers.ts renders straight off `session.steers` and
// messages-blocks.ts interleaves straight off `session.steer_marks`.

/** The steer id KAS will return for a message id.
 *
 *  `internal/vibekit/commands.go:329-330` documents the convention (`"steer-" +
 *  messageID`) and `internal/command/steer_test.go` pins it. Deriving it is what
 *  lets the optimistic row be reconciled by a plain id match: the POST's reply
 *  does carry the authoritative id, but `transportAction` discards the body, so
 *  it is unreachable from here. `recordSteerQueued`'s text fallback is the safety
 *  net if the convention ever drifts. */
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

/** Record a steer this client has just POSTed, before any server frame.
 *
 *  The row the user is owed for pressing Send. `pending` says the id is DERIVED
 *  rather than confirmed, which is what the dock reads to withhold Edit and
 *  Discard: there is no server-side id to clear yet. Rolled back by
 *  `forgetSteer` when the POST fails. */
export function recordSteerSent(id: string, messageID: string, text: string): void {
  const s = get(id);
  if (s === undefined || messageID === "") {
    return;
  }
  // `user` is a FACT here, not a guess: this row is this device's own POST. It is
  // the one place the client writes an origin, and the server's own frame
  // confirms it under the same id a round trip later.
  const entry: PendingSteer = {
    id: steerIDFor(messageID),
    text,
    origin: "user",
    pending: true,
  };
  const existing = s.steers ?? [];
  // Idempotent by id: submit.ts reuses one message id for a retry of a failed
  // attempt, and that retry must refresh the row rather than add a second.
  const at = existing.findIndex((e) => e.id === entry.id);
  const next = at >= 0 ? existing.map((e, i) => (i === at ? entry : e)) : [...existing, entry];
  sessions.update(id, (cur) => ({ ...cur, steers: next }));
  scheduleMessages(id, "fact"); // the dock row is transcript-adjacent state
}

/** Un-draw one steer row. The rollback half of `recordSteerSent`.
 *
 *  Removes by id and leaves everything else alone, so a 409 for the message just
 *  sent cannot take a sibling still waiting with it. */
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

/** Adopt KAS's own confirmation of a steer into the dock.
 *
 *  THREE BRANCHES, one outcome — exactly one row per message:
 *
 *    1. The id matches (the ordinary case, because the client derives the id KAS
 *       returns): adopt the text and clear `pending`.
 *    2. No id match but the OLDEST pending row carries the same text: adopt the
 *       server's id onto it. The fallback for a KAS whose prefix convention has
 *       drifted, so the reconcile does not depend on that convention.
 *    3. Neither: append it confirmed. This is the frame for a steer another
 *       device sent, or one this client sent before a reload.
 *
 *  Idempotent in all three: a replayed `steer_queued` re-enters branch 1 and
 *  refreshes the row rather than adding one, so a corrected text still lands. An
 *  id already PROMOTED is not resurrected — `steer_marks` is checked first,
 *  because a reconnect replays the queued frame for a steer the agent has since
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
  // The frame's origin wins in every branch, including the adopt: the server
  // resolved it against the ledger of what it sent, and the optimistic row's
  // `user` was this device's claim about the same fact.
  const entry: PendingSteer = { id: steer.id, text: steer.text, origin: steer.origin };
  const next =
    adoptAt >= 0 ? existing.map((e, i) => (i === adoptAt ? entry : e)) : [...existing, entry];
  sessions.update(id, (cur) => ({ ...cur, steers: next }));
  scheduleMessages(id, "fact");
}

/** Promote a steer the agent has READ out of the dock and into the transcript.
 *
 *  It LEAVES `steers` — the dock holds only what is still waiting — and gains a
 *  `steer_marks` entry anchored at the block the running turn had reached, which
 *  is what makes the note render chronologically rather than at the end.
 *
 *  TWO FRAMES, one id. KAS's steering channel sends the read frame (text, no
 *  ack); the agent's acknowledgement marker on the text stream sends the ack
 *  frame (ack, no text) once it has acted. So each field is adopted only when
 *  the frame carries it, or the second frame would blank the first's text — and
 *  the second must not re-anchor a note that is already placed, or the reader
 *  would watch it jump down the turn.
 *
 *  Tolerates an id it has never seen by creating the mark from the frame's own
 *  text: `steer_injected` can legitimately arrive without its `steer_queued`
 *  (another device sent it, or this one connected mid-turn), and dropping it
 *  would leave the transcript showing the agent change course with nothing
 *  explaining why. An ack-only frame for an id with no mark and no text IS
 *  ignored — with no text there is nothing to label the note with. */
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
    // The same text fallback `recordSteerQueued` uses, for the sequence where
    // the injected frame beats the queued one to an optimistic row.
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
  // No `origin` here: it is written once, on the frame that CREATES the mark. The
  // ledger behind it is TTL'd, so a late second frame can answer `agent` for a
  // message the first correctly named as the user's.
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

/** Drop steers at a turn boundary: out of the dock, into the transcript as
 *  UNDELIVERED.
 *
 *  Named ids drop just those; an empty or absent list drops the chat's whole set,
 *  which is what a turn boundary means. Each one keeps its text and earns a
 *  `dropped` mark, because "I sent this and the agent never read it" is exactly
 *  the kind of fact the transcript is for — and the note offers to put the text
 *  back in the composer, so it is one click from being re-sent as a prompt.
 *
 *  An id already in `steer_marks` is HOUSEKEEPING and a no-op: KAS clears its
 *  buffer at every boundary, so `steer_cleared` routinely names ids the model
 *  already read, and those must keep their existing mark rather than gain a
 *  second one claiming they were missed.
 *
 *  KNOWN LIMITATION, accepted: another device's explicit discard reaches this
 *  client only as `steer_cleared`, so it renders as "not delivered". The label is
 *  still true — the agent never read it — it just does not say who took it back.
 *  Distinguishing the two would be a wire flag for a rare multi-device case. THIS
 *  device's own discard leaves no row, because `chat.clear_steers` removes the
 *  entries optimistically at dispatch, so by the time the frame lands there is
 *  nothing here to promote. */
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
    // The dock entry's own origin, which came from the queued frame the server
    // stamped: a dropped steer has no injected frame to read one off.
    .map((e): SteerMark => ({ id: e.id, text: e.text, origin: e.origin, dropped: true, anchor }));
  sessions.update(id, (cur) =>
    withSteers(added.length > 0 ? { ...cur, steer_marks: [...marks, ...added] } : cur, rest),
  );
  scheduleMessages(id, "fact"); // dropped marks render in the turn
}

/** Remove every CONFIRMED waiting steer, returning a snapshot to restore from.
 *
 *  The optimistic half of `chat.clear_steers`, and the reason an explicit discard
 *  leaves no transcript row: the entries are gone before the server's
 *  `steer_cleared` frame arrives, so `dropSteers` finds nothing to promote as
 *  "not delivered". Taking a message back is not the agent missing it.
 *
 *  A `pending` entry STAYS: `_session/steer/clear` drains KAS's buffer, and a
 *  steer still in flight is not in that buffer yet, so removing it locally would
 *  hide a message that is still on its way.
 *
 *  Returns the array as it was, not the entries taken, so the rollback restores
 *  the exact order rather than reconstructing it. Empty means nothing changed. */
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

/** Forget the dock's contents WITHOUT promoting them. The `transport:gap` path.
 *
 *  A gap means the frames that resolved these steers may be among the ones we
 *  lost, so promoting them would assert "the agent never read this" on no
 *  evidence. Existing marks stay: those are facts already established. */
export function forgetSteers(id: string): void {
  const s = get(id);
  if (s?.steers === undefined) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, []));
  scheduleMessages(id, "fact");
}

/** Where a steer read RIGHT NOW belongs: after everything the turn's assistant
 *  message has produced so far.
 *
 *  An empty `msgID` means the steer was read before the turn produced anything;
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

/** Bind every anchor-less mark to a newly-arrived assistant message.
 *
 *  A steer read before the turn produced anything belongs ABOVE that turn's
 *  first block, and until the message exists there is no id to say so with.
 *  Called from `ingestMessage`'s push branch through `sessions.update` rather
 *  than a version bump, because only `sessions.update` re-derives
 *  `activeSession` — which is what the renderers' value-dedup computeds read. */
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

/** Write `steers` onto a session, DELETING the field when the list is empty.
 *
 *  Deleted rather than left as an empty array so a session compares equal to one
 *  that never had steers — pending-steers.ts's `computed` dedups by value, and an
 *  empty array would repaint on every clear. `steer_marks` gets the same
 *  treatment for the same reason (messages.ts reads it per repaint). */
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
    // Server-authoritative re-sync of the header fields (preserve messages,
    // thinking and working_label, which are client/stream-owned).
    sessions.update(h.id, (s) => {
      const next: Session = {
        ...s,
        name: h.name,
        // `model` is the ONE header field the CLIENT can legitimately be ahead of
        // the server on: a pick before the first prompt applies locally and rides
        // that prompt (see applyLocalModel), so until then the record genuinely
        // has no model. `Model` is `omitempty` on the wire, which makes "not set"
        // and "cleared" the same frame, and taking it as a clear is what reset the
        // pill to "auto" and unselected every row in the model list the moment
        // `set_effort` or `set_mode` auto-created the record for a chat whose
        // model only the client knew. Absent means no news — the same rule
        // ingestMessage applies to message content.
        model: h.model !== undefined && h.model !== "" ? h.model : s.model,
        acp_session_id: h.acp_session_id ?? "",
        current_mode_id: h.current_mode_id ?? "",
        available_modes: h.available_modes ?? [],
        available_models: h.available_models ?? [],
        supervised_mode: h.supervised_mode ?? false,
        effort: h.effort ?? "",
        // The live effort vocabulary and the tier the session runs at. Absent
        // means the server has no session catalog to report (a chat with no
        // bridge, or a header built before session/new answered), NOT that the
        // tiers went away — same rule as `model` above.
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
      // AFTER the spread of `s`, so an already-set latch survives: the helper
      // carries an existing one over and can only ever add, never clear.
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
    // A chat this client has never seen live: the header's outcome is the ONLY
    // thing that can tell its dot from a chat that has never run a turn.
    ...latchFieldsFor(undefined, h),
  };
  if (h.compaction_watermark !== undefined) {
    s.compaction_watermark = h.compaction_watermark;
  }
  // New sessions go to the front of the list (matches the previous unshift).
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
  // Both reactive writes below — sessions.remove (fires sessions.ids) and the
  // activeId reassignment — feed the `activeSession` computed. Batch them so
  // active-session subscribers re-derive ONCE, not twice; otherwise removing
  // the active chat double-fires the computed and the messages renderer flashes
  // a transient teardown of the new chat's DOM.
  batch(() => {
    sessions.remove(id);
    msgIndex.delete(id);
    // The two per-chat side tables keyed outside the session object. Both doc
    // comments already say "or chat removed"; this is where that becomes true.
    clearSnapshotSeq(id);
    clearLiveTurnMessage(id);
    // Every per-message streaming signal the chat's window minted, block
    // signals included: the renderer's disposeMessage only reaches rows a
    // reconcile removes, and a background chat's rows never see one.
    clearMessageSignals(id, doomed.messages);
    lastActivity.delete(id);
    // The chat's version signal, its cause bookkeeping, and any bump parked on
    // the next microtask go with it — a flush after this must not re-mint the
    // signal.
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

/** Re-insert a previously-removed session at a specific index (or at the head
 *  if no index given). Used by optimistic action rollbacks. Idempotent. */
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

/** Spread helper: include `agent_subtask_id` on a block only when non-empty
 *  (exactOptionalPropertyTypes forbids setting an optional field to
 *  undefined; "" is the top-level default and is simply omitted). */
function subtaskField(id: string | undefined): { agent_subtask_id?: string } {
  return nonEmptyStr(id) ? { agent_subtask_id: id } : {};
}

/** Ensure an assistant message has a `blocks` array so the renderer has ONE
 *  path. v3 server messages already carry blocks; legacy replays (content /
 *  reasoning / tool_calls only) get synthesized blocks — thinking, then text,
 *  then a tool_use per tool call. Non-assistant or already-block messages pass
 *  through unchanged. */
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

/** Merge a freshly-ingested server message over the existing one: adopt the
 *  incoming's non-empty fields, never clobber non-empty with empty. This is
 *  what lets the streamed assistant message and its final message_appended
 *  (same id) coexist — the final's sanitized fields win, but an empty
 *  message_created can't wipe streamed content. */
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
  // The allowlist is exhaustive by construction: an unlisted field is silently
  // dropped on the second ingest of the same id, and a user message with
  // attachments is ingested twice — once from the prompt's own message_appended
  // and again from a chat refetch or the reconnect replay.
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

/** Where a NEW message belongs in the array.
 *
 *  A row the server has PERSISTED goes before the in-flight turn's message,
 *  because that is where the chat file has it: the server writes the file before
 *  it broadcasts, and the reply is not in that file until turn_ended.
 *
 *  A row that OPENS a turn is exempt — `projectTurns` starts a turn on a user
 *  message, and a prompt sent during a live reply genuinely follows it. */
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

/** Ingest a server-canonical message. message_created / message_appended /
 *  message_updated ALL route here. Upsert by id with a merge that never drops
 *  a message and never overwrites non-empty content with empty:
 *  - absent  → normalize (ensure blocks) + insert at insertIndexFor.
 *  - present → mergeMessage (adopt incoming's non-empty fields).
 *  Fixes the "final message never renders" (old append no-op), the
 *  "message_created wipes streamed content" (old blind replace), and the
 *  out-of-order chunk-before-created bugs. */
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
      // Every index at or past the splice point moved, so the map is rebuilt
      // rather than patched.
      rebuildMsgIndex(chatID, s.messages);
    }
    s.message_count = Math.max(s.message_count, s.messages.length);
    bumpMessages(chatID);
    if (incoming.role === "assistant" && (incoming.plan ?? []).length === 0) {
      // A steer read before this turn produced anything is anchored at no
      // message; this is the first moment there is an id to give it. A PLAN row is
      // skipped though it is RoleAssistant too — the anchor means "the reply this
      // steer was read into" — or it captures every pending mark and the reply's
      // own message_created finds none left. AFTER the push, and through
      // `sessions.update` rather than a version bump, because the anchor must be
      // readable off `activeSession` by the time the transcript repaints.
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

/** message_appended → merge path (was a dedup no-op that dropped the final
 *  sanitized message).
 *
 *  It is also the PERSIST echo: the server writes the chat file before it
 *  broadcasts this, so an id arriving here is no longer the client's only copy
 *  and stops being the in-flight turn. That makes the `turn_ended` clear below
 *  belt-and-braces rather than the mechanism. */
export function appendMessage(chatID: string, msg: Message): void {
  if (liveTurnMessage(chatID) === msg.id) {
    clearLiveTurnMessage(chatID);
  }
  ingestMessage(chatID, msg, true);
}

/** message_created / message_updated → merge path (was a blind replace that
 *  wiped streamed content). */
export function upsertMessage(chatID: string, msg: Message): void {
  ingestMessage(chatID, msg, false);
}

/** Stamp the just-ended turn's summary (credits / elapsed / changed files)
 *  onto the chat's last assistant message. The renderer projects it into a
 *  keyed `.turn-footer` under that turn — replacing the old un-keyed direct
 *  DOM write in handlers/turn.ts that double-rendered on SSE replay and
 *  vanished on refresh. Applies to any chat (not just the active one) so a
 *  background turn's footer is present when the user switches to it. The
 *  server persists the same fields at flush time so it also survives reload.
 *
 *  Skipped when a persisted row in the same turn body already carries the outcome:
 *  the assistant message beside such a carrier is a SEGMENT of that turn, and
 *  `turnLedger` sums across the body. */
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
  // TRAILING user rows belong to a LATER turn that has produced nothing yet — a
  // prompt persists its row before it asks for the chat's admission slot — so the
  // walk steps over them and stops at the user row that OPENS the turn being
  // summarised. What lies between is this turn's body.
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
    // `target === undefined` is what keeps the veto INSIDE this turn. `projectTurns`
    // closes a turn on an outcome-bearing row as well as on a user row, so a
    // transcript ending [.., event(turn_outcome), assistant] has its carrier in the
    // PREVIOUS turn; ungated, that vetoed the stamp for the new headerless turn and
    // its live footer showed no credits until a reload. The sealed-then-empty case
    // is unaffected: on a backwards walk its carrier is met before any assistant.
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
  // Same non-empty guard as the numbers above: an absent model means the server
  // could not name one, and stamping "" would make a turn look attributed.
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

/** Set the chat's reasoning-effort level. Per-chat like model, mode and
 *  supervised; the effort control renders from here rather than from a module
 *  signal, so a tab switch reads the new chat's level instead of carrying the
 *  previous one over. */
export function setEffort(chatID: string, effort: string): void {
  const s = get(chatID);
  if (s === undefined || (s.effort ?? "") === effort) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, effort }));
}

/** Set session model and notify subscribers. Used by switchModel. The
 *  usage.context_size is derived from the model, so it is refreshed in the
 *  same update — callers must never mutate `session.usage` on a stale
 *  reference (sessions.update replaces the object; writes to the old one
 *  are invisible to subscribers and re-renders). */
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

/** Per-chat chunk-sequence watermark from a connect-time turn_state
 *  snapshot: chunks with seq <= the watermark are already folded into
 *  the snapshot message and must be dropped, not re-appended. One
 *  in-flight turn per chat, so the map is keyed by chat id; entries
 *  clear on turn_ended (handlers/turn.ts) and are naturally superseded
 *  when a new turn's message id stops matching. */
const snapshotSeqs = new Map<string, { messageID: string; seq: number }>();

/** Record a turn_state snapshot's chunk watermark for a chat. */
export function setSnapshotSeq(chatID: string, messageID: string, seq: number): void {
  snapshotSeqs.set(chatID, { messageID, seq });
}

/** Drop the chunk watermark (turn finished or chat removed). */
export function clearSnapshotSeq(chatID: string): void {
  snapshotSeqs.delete(chatID);
}

/**
 * Reserve the block indices below `upto` whose own frame has not arrived yet.
 *
 * `block_index` is the server's position in ONE chronological array that the
 * client fills from TWO event streams — text and thinking over `message_chunk`,
 * tool calls over `tool_call` — so a frame can legitimately name an index past
 * the end of what has arrived. A pad keeps the array aligned with the server's
 * numbering AND reserves the DOM position, which is load-bearing rather than
 * defensive: the block mounter is append-only (`messages-blocks.ts`), so a block
 * whose frame lands late cannot be inserted between two mounted siblings. The
 * slot has to exist before its content does, and the text fills into it after.
 *
 * The kind is a GUESS, because nothing here knows what a missing frame will turn
 * out to be. `text` is the guess because it mounts a FILLABLE node; `thinking`
 * would mount nothing at all (`mountThinking` drops an empty settled trace), and
 * a slot that turned out to be text would then have nowhere to put it.
 * `isPadBlock` is what lets the real frame correct the guess when it lands.
 */
function padBlocks(blocks: Block[], upto: number): void {
  while (blocks.length < upto) {
    blocks.push({ type: "text" });
  }
}

/** Whether `b` is still a pad: a kind, and nothing behind it.
 *
 *  Conservative by construction — every real block carries the content that
 *  created it (`text`, `thinking`, or `tool_call_id`, each set in the same
 *  literal as its `type`), so this cannot misread one as a pad. */
function isPadBlock(b: Block): boolean {
  return b.text === undefined && b.thinking === undefined && b.tool_call_id === undefined;
}

/** The assistant message a chat's CURRENT turn is streaming into, while the
 *  server still holds it in memory and nowhere else.
 *
 *  The server accumulates a turn in an in-memory buffer and appends it to the
 *  chat file once, at turn_ended, so `GET /api/chats/{id}` cannot carry it and
 *  the client's copy is the only one there is. `loadMessages` replaces the array
 *  with that page, so it has to know WHICH local message the page is entitled to
 *  omit — and POSITION cannot answer that, which is what this map exists for.
 *  The agent persists messages DURING a turn (every plan update, a compaction or
 *  safety event, the cancel badge), and each of them lands after the streaming
 *  reply locally while sitting inside the page, so a rule of "keep everything
 *  after the newest id the page carries" steps past the reply and the replace
 *  deletes it. One plan update was enough.
 *
 *  One entry per chat, because the server keeps one buffer per chat. Set by all
 *  three ways an unpersisted message enters the store — message_created, a chunk
 *  that beat it, and a reconnect's turn_state — and cleared the moment the server
 *  echoes the same id as persisted (message_appended), at turn end, and with the
 *  chat. */
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
  // Snapshot dedup: a chunk the connect-time turn_state already folded
  // in (raced the snapshot on the server side) must not double-append.
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
    // A chunk that beat its own message_created. This is one of the three doors
    // an unpersisted message comes through, so it marks the turn like the other
    // two — otherwise a refetch in that window has no way to tell this message
    // from one the server deliberately omitted.
    noteLiveTurnMessage(chatID, messageID);
  }
  let refusalStamped = false;
  if (refusal !== undefined && msg.refusal === undefined) {
    // Model refusal (kiro-cli 2.13): the tagged chunk marks the whole turn.
    // Stamped once; forces a full repaint below so the message-level refusal
    // callout mounts (per-block signals only carry text deltas).
    msg.refusal = refusal;
    refusalStamped = true;
  }
  if (isReasoning) {
    msg.reasoning = (msg.reasoning ?? "") + delta;
  } else {
    msg.content = (msg.content ?? "") + delta;
  }
  // Mirror the delta into the chronological blocks array using the
  // server-provided block_index. The server guarantees consecutive
  // chunks for the same kind (and same subtask) share an index; a
  // tool_call, a kind switch, or a subtask switch bumps to a new index.
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
    // A PAD'S KIND IS A GUESS (see padBlocks), so the first real delta for the
    // slot is what decides it. Without this the guess stuck and
    // `syncMountedText` then read the wrong field: a thinking delta merged into
    // a `text` pad left `existing.text` undefined, so the block rendered as an
    // empty row and its reasoning was dropped outright rather than collapsed.
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
    // A message the store has never seen: the renderer must mount its row. Not
    // exempted for a step below — a step's first frame really does open a new turn
    // card in the launching chat, headerless and empty, and that card has to mount.
    scheduleMessages(chatID, "shape");
    return;
  }
  // A WORKFLOW STEP's blocks are DROPPED by the dispatcher, so a delta for one needs
  // no structural pass: there is nothing to mount and nothing to re-type.
  const stepCause = droppedFrameCause(subtaskID);
  if (refusalStamped) {
    // Message-level FACT changed (not just a block's text): the refusal feeds
    // deriveOutcome and the callout mounts on a full pass.
    scheduleMessages(chatID, "fact");
  }
  if (newBlock || padRepaired) {
    // A new block pushed, or a pad's guessed kind corrected: block STRUCTURE
    // changed, and only the full pass mounts or re-types a block.
    scheduleMessages(chatID, stepCause);
  }
  // Fire the per-block signal (fine-grained — only the block at blockIndex
  // re-renders) before the legacy per-message signal.
  const blockK = blockKey(messageID, blockIndex);
  const blockMap = isReasoning ? blockThinkingSigs : blockTextSigs;
  const blockSig = blockMap.get(blockK);
  if (blockSig !== undefined) {
    const fullText = isReasoning
      ? (msg.blocks[blockIndex]?.thinking ?? "")
      : (msg.blocks[blockIndex]?.text ?? "");
    blockSig.value = { full: fullText, delta };
    // Pure growth of a MOUNTED block: the signal effect painted the text, so
    // the paint this schedules refreshes tail bookkeeping only.
    scheduleMessages(chatID, "chunk");
  }
  if (isReasoning) {
    const sig = streamingReasoningSigs.get(messageID);
    if (sig !== undefined) {
      sig.value = msg.reasoning ?? "";
      scheduleMessages(chatID, "chunk");
    } else if (blockSig === undefined) {
      // Signal-absent fallback: nothing is mounted to carry the text, so the
      // full pass is what puts it on screen — unless nothing is MEANT to be, which
      // is a dropped step.
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

/** Attach licensed-code attributions to an in-flight assistant message
 *  (v3 code_references SSE). The server sends the full deduped list each
 *  time, so we replace rather than append. No-op if the target message
 *  isn't in the store yet — the refs still persist server-side and render
 *  on reload from Message.code_references. */
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
    // HONOUR `blockIndex` HERE TOO. This used to hard-code the tool_use block at
    // index 0, so a turn whose first frame to reach the client was a `tool_call`
    // at index 2 started life misaligned by two, and every index after it was
    // wrong for the rest of the turn — the hole the cascade below then fed on.
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
    // A new message: the full pass mounts its row (synchronous — an arrival
    // must be in the DOM before the frame that announced it).
    bumpMessages(chatID, "shape");
    return;
  }
  msg.tool_calls ??= [];
  msg.blocks ??= [];
  // The EXISTING-message arms below take the cheap cause for a call the transcript
  // draws nothing for, exactly as `appendChunk` does for a delta. The
  // `msg === undefined` arm above deliberately does not: a step's first frame really
  // does open a turn card in the launching chat, and that card has to mount.
  //
  // The update path is what makes this load-bearing rather than symmetric. No card is
  // mounted for a step's call, so `ensureToolCallSig` is never reached for one (the
  // mount sites are `messages-tools.ts` and mountToolCard / mountTodo / bindSubagent
  // in `messages-blocks.ts`) — so every `tool_call_update` for every step of every run
  // falls into the signal-absent arm at the foot of this function. Before the drop the
  // mounted card owned a signal and updates took the cheap `tool` cause.
  const stepCause = droppedFrameCause(call.agent_subtask_id ?? "");
  const tcIdx = msg.tool_calls.findIndex((tc) => tc.id === call.id);
  if (tcIdx === -1) {
    msg.tool_calls.push(call);
    // First time we see this tool call — pin it to the chronological block index
    // the server reported, REPLACING whatever pad is sitting there.
    //
    // The replacement is the fix for a CASCADE, not a tidy-up. This used to read
    // `if (msg.blocks[blockIndex] === undefined) push(...)`, which is false
    // immediately after its own padding above, so the tool_use block was never
    // written: the card was dropped from the transcript AND `msg.blocks.length`
    // stayed one short of the server's next index, so the next tool call padded
    // again and dropped itself the same way. One hole early in a turn therefore
    // corrupted every tool call after it, and the abandoned pads rendered as
    // zero-height rows that still cost the block column's gap. Measured on a live
    // turn: 128 tool_use blocks server-side became 2 tool groups and 122 empty
    // rows, about 1464px of dead space in one card.
    //
    // Assigned rather than conditionally pushed: the server's index is
    // authoritative for what belongs at that position, and a silent skip here is
    // exactly what hid this for so long.
    //
    // A PAD is overwritten; a REAL block is not. The two cases are different
    // failures and only the pad case is this method's to fix: replacing a
    // standing text or thinking block would delete transcript content the
    // server already streamed, which is worse than the misalignment above.
    // `isPadBlock` is safe to lean on because every real block carries the
    // content that created it.
    padBlocks(msg.blocks, blockIndex);
    const standing = msg.blocks[blockIndex];
    if (standing === undefined || isPadBlock(standing)) {
      msg.blocks[blockIndex] = {
        type: "tool_use",
        tool_call_id: call.id,
        ...subtaskField(call.agent_subtask_id),
      };
    }
    // First sighting of this call, and `stepCause` is what decides the pass: a
    // drawn call needs the full one that mounts its card, a dropped step's needs
    // none, because nothing mounts for it.
    scheduleMessages(chatID, stepCause);
    return;
  }
  const prev = msg.tool_calls[tcIdx];
  msg.tool_calls[tcIdx] = call;
  // Late identity attachments are STRUCTURAL, not status updates (the server
  // attaches both ids on updates when the initial call lacked them —
  // translate/streaming_tools.go):
  //
  //  - a first `agent_subtask_id` decides container MEMBERSHIP (top-level list
  //    vs subagent box), which is the BLOCK's field, and the tool fast path
  //    never re-homes a card — so the block updates here and the full pass
  //    re-homes. An id never changes once set. A `wf:` id has no destination to
  //    be re-homed INTO, so it takes the cheap cause with the rest.
  //  - a first `workflow_id` is what the block dispatcher keys a run card on, so
  //    the card has to be built for a call that arrived without one: `fact`.
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
    // The card's own effect repaints it; the tool paint refreshes the owning
    // turn's keyed state only — never a projection, never a mount.
    scheduleMessages(chatID, "tool", messageID);
  } else {
    // Signal-absent fallback: nothing is mounted for this card, so the full
    // pass is what puts its update on screen — unless nothing is MEANT to be,
    // which is a dropped step.
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
