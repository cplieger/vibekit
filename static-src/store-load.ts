// HTTP load operations for the session store: hydrates the state store.ts owns.

import type { Session, ChatHeader, Message } from "./types.js";
import { apiGetTyped, apiGetTypedOrError } from "./api-client.js";
import { asObject, decodeArray, optBool, optNum, reqBool, type Decoder } from "./validators.js";
import { decodeChatHeader, decodeMessage } from "./wire/decoders.gen.js";
import { registerCleanup } from "./actions/index.js";
import {
  setSessions,
  derivedHasMore,
  get,
  getSessions,
  rebuildMsgIndex,
  bumpMessages,
  normalizeMessage,
  liveTurnMessage,
  chunkWatermark,
  setChunkWatermark,
  noteLiveTurnMessage,
  noteTruncatedSnapshot,
  upsertMessage,
  relatchTurnVerdict,
  latchFieldsFor,
  upsertHeader,
  republishWindowToolCalls,
} from "./store.js";
import { healSettledChat } from "./turn-teardown.js";
import { noteLoaded, syncEpoch } from "./tab-freshness.js";

// --- Inline decoders ---
const decodeChatListResponseLocal: Decoder<{ chats?: ChatHeader[] }> = (v) => {
  const o = asObject(v, "$.chat_list");
  const out: { chats?: ChatHeader[] } = {};
  if (o["chats"] !== undefined) {
    out.chats = decodeArray(o["chats"], decodeChatHeader, "$.chat_list.chats");
  }
  return out;
};

/** The chat's in-flight turn as the transcript GET carries it: the reply already accumulated
 *  server-side, which reaches the chat file only at turn end so `messages` cannot hold it. */
interface LiveTurnPage {
  readonly message: Message;
  readonly chunk_seq: number;
  readonly truncated: boolean;
}

/** Decode the optional `live_turn` sibling, optional-tolerant per this decoder's stated
 *  convention. Absent means either no turn is running or an older server, and both mean
 *  "nothing to adopt"; a MALFORMED one is refused by `decodeMessage` rather than half-read,
 *  because a message with no id is one the store cannot merge or dedup against. */
function decodeLiveTurn(raw: unknown): LiveTurnPage | undefined {
  if (raw === undefined || raw === null) {
    return undefined;
  }
  const o = asObject(raw, "$.chat_get.live_turn");
  return {
    message: decodeMessage(o["message"]),
    chunk_seq: optNum(o, "chunk_seq", "$.chat_get.live_turn") ?? 0,
    // The server sends this unconditionally, so an absent marker means an older server,
    // which capped nothing. It may never be read as "complete" when present.
    truncated: o["truncated"] === true,
  };
}

const decodeChatGetResponseLocal: Decoder<{
  chat: ChatHeader;
  messages: Message[];
  has_more: boolean;
  draft: string;
  turn_open: boolean | undefined;
  turn_offset: number | undefined;
  turn_segment_closed: boolean | undefined;
  live_turn: LiveTurnPage | undefined;
}> = (v) => {
  const o = asObject(v, "$.chat_get");
  return {
    chat: decodeChatHeader(o["chat"]),
    messages: decodeArray(o["messages"], decodeMessage, "$.chat_get.messages"),
    has_more: reqBool(o, "has_more", "$.chat_get"),
    // The in-flight turn, which the window structurally cannot carry: it is the one thing
    // in this response that is not in the chat file yet.
    live_turn: decodeLiveTurn(o["live_turn"]),
    // Every field below is optional-tolerant: an older server, or a proxy that strips
    // one, must not fail the whole chat load. `store.ts` turnLive is turn_open's one
    // reader; `turnBaseOf` is the window base's. UNDEFINED rather than false when absent,
    // for the base's reason below AND because the newest-page door's teardown arm turns on
    // the server having STATED the turn closed — a collapse to false hands it that
    // statement for a chat the answer said nothing about.
    turn_open: optBool(o, "turn_open", "$.chat_get"),
    // The window base is UNDEFINED rather than 0/false when absent, so the session
    // records "the server said nothing" instead of "the window starts the session" —
    // the same distinction `has_more`'s guess-versus-answer split turns on.
    turn_offset: optNum(o, "turn_offset", "$.chat_get"),
    turn_segment_closed: optBool(o, "turn_segment_closed", "$.chat_get"),
    draft: typeof o["draft"] === "string" ? o["draft"] : "",
  };
};

/** Record the server's statement about the window's LEFT EDGE, or forget the one the
 *  session held. A half-present answer is a stripped field rather than a partial fact,
 *  and forgetting beats keeping: `turnBaseOf`'s fallback numbers the window from 1,
 *  where a stale offset numbers it from a place nothing in the window corresponds to. */
function adoptTurnBase(
  session: Session,
  offset: number | undefined,
  closed: boolean | undefined,
): void {
  if (offset === undefined || closed === undefined) {
    delete session.turn_offset;
    delete session.turn_segment_closed;
    return;
  }
  session.turn_offset = offset;
  session.turn_segment_closed = closed;
}

/** Adopt the fetched in-flight turn, or refuse it as stale. The four calls are the ones the
 *  `turn_state` handler makes (`handlers/messages.ts`): the same content through a second
 *  channel has to land in the same four places, or the two channels leave the store in
 *  different shapes.
 *
 *  THE GATE is the whole guard against a stale answer — the response is a point-in-time read,
 *  and `store.ts` chunkWatermarks states what adopting an older copy costs. A refusal changes
 *  nothing: the live stream is already ahead. An absent local mark passes. */
function adoptLiveTurn(chatID: string, live: LiveTurnPage): void {
  if (live.message.id === "") {
    return;
  }
  const held = chunkWatermark(chatID, live.message.id);
  if (held !== undefined && live.chunk_seq < held) {
    return;
  }
  setChunkWatermark(chatID, live.message.id, live.chunk_seq);
  // The server holds this message in memory and nowhere else, so it is unpersisted by
  // construction — which is what a later refetch has to know before it may drop it.
  noteLiveTurnMessage(chatID, live.message.id);
  if (live.truncated) {
    noteTruncatedSnapshot(chatID, live.message.id);
  }
  upsertMessage(chatID, live.message);
}

/** Re-order re-adopted rows the way the live path puts them; `store.ts` insertIndexFor owns
 *  that rule. Inert when the live message is not among them — nothing to insert against. */
function reorderKept(kept: Message[], liveID: string | undefined): Message[] {
  const live = kept.find((m) => m.id === liveID);
  if (live === undefined) {
    return kept;
  }
  const before: Message[] = [];
  const after: Message[] = [];
  for (const m of kept) {
    if (m.id === liveID) {
      continue;
    }
    (m.role === "user" ? after : before).push(m);
  }
  return [...before, live, ...after];
}

/**
 * The BYTE bound on one transcript page: the hostile-input ceiling on what the wire
 * may carry, matching the server's own default. It is not a proxy for what one paint
 * can mount, which `block-window.ts` bounds itself, per turn, on arrival. Nothing
 * becomes unreachable: the server returns the newest turn whole however big it is,
 * and `has_more` plus `before_id` reach everything older.
 */
const PAGE_BUDGET_BYTES = 1 << 20;

/**
 * The server's cap on messages per page, and NOT this client's budget — it is a
 * bound on the answer's shape, not on its size. It binds only where messages are
 * small enough that 50 of them fit in the budget above, which on this workload is
 * never: the budget is what cuts every real page.
 */
const PAGE_MESSAGE_CAP = 50;

// --- Abort controllers ---
let listController: AbortController | null = null;
const msgControllers = new Map<string, AbortController>();

/** Whether `loadList` has ever succeeded. Read through `chatListLoaded`. */
let listLoaded = false;

/** What the last `loadList` attempt established about the SERVER, not about that request.
 *  An abort is a fact about a request only, so the abort path leaves this alone. */
type ListReach = "unknown" | "reachable" | "unreachable";
let listReach: ListReach = "unknown";

/** The ladder behind a list load that reached the network and failed: three attempts,
 *  1s doubling. Bounded because the next SSE `connected` refetches the list anyway, so
 *  what this covers is the window in between — a stream that stayed up while the list
 *  request died, which no other trigger revisits. */
const LIST_RETRY_LIMIT = 3;
const LIST_RETRY_BASE_MS = 1000;

let listRetryTimer: ReturnType<typeof setTimeout> | undefined;
let listRetryAttempts = 0;

/** Forget a ladder in flight, rungs and all: a successful `loadList` has nothing left to
 *  retry, and a new gap arms one of its own rather than stacking beside this. */
function cancelListRetry(): void {
  if (listRetryTimer !== undefined) {
    clearTimeout(listRetryTimer);
    listRetryTimer = undefined;
  }
  listRetryAttempts = 0;
}

/** Arm the next rung, or report the ladder exhausted. Private because it is the
 *  CONTINUATION: it keeps the attempt count that `scheduleListRetry` resets. */
function armListRetry(): void {
  if (listRetryAttempts >= LIST_RETRY_LIMIT) {
    console.warn(
      `[list] gave up after ${String(LIST_RETRY_LIMIT)} retries; the chat list stays as it is until the next reconnect`,
    );
    listRetryAttempts = 0;
    return;
  }
  if (listRetryTimer !== undefined) {
    // A fresh gap's own `loadList` can fail while a rung is already armed: `scheduleListRetry`
    // arms one, and the ABORTED load a rung is waiting on then settles false with no reach
    // verdict written, so its continuation reads the previous `unreachable` and arms again.
    // The newest failure owns the rung — without this the orphaned timer also fires and the
    // ladder fetches twice per rung instead of staying inside its three.
    clearTimeout(listRetryTimer);
  }
  const delay = LIST_RETRY_BASE_MS * 2 ** listRetryAttempts;
  listRetryAttempts++;
  listRetryTimer = setTimeout(() => {
    listRetryTimer = undefined;
    void loadList().then((ok) => {
      // The door's own gate, for the door's own reason: an ABORT also answers false and
      // writes no reach verdict, so it must not extend a ladder.
      if (!ok && listReach === "unreachable") {
        armListRetry();
      }
    });
  }, delay);
}

/** Retry a `loadList` that failed, bounded.
 *
 *  THE REACH GATE LIVES HERE rather than at the call site: `!ok` alone is not evidence
 *  about the server, because an aborted load returns it too and deliberately records no
 *  verdict, so a ladder armed on `!ok` would chase a load a newer one superseded.
 *  `serverMayAnswer` cannot stand in for the read — it folds `listLoaded` in, so it
 *  answers a different question. */
export function scheduleListRetry(): void {
  if (listReach !== "unreachable") {
    return;
  }
  cancelListRetry();
  armListRetry();
}

/** What the SERVER says about a chat id. `gone` is the only value that licenses a terminal
 *  claim; `unresolved` means nobody answered and the caller holds whatever it has. */
export type ChatVerdict = "exists" | "gone" | "unresolved";

/** The single-chat GET reduced to the one field this question needs. Its own
 *  decoder rather than `decodeChatGetResponseLocal`, because that one requires
 *  `messages` and `has_more` — fields a verdict does not read, and each an extra
 *  way for an answer that DID arrive to be discarded as undecodable. */
const decodeChatConfirmResponseLocal: Decoder<{ chat: ChatHeader }> = (v) => {
  const o = asObject(v, "$.chat_confirm");
  return { chat: decodeChatHeader(o["chat"]) };
};

/** Is this id SHAPED like a chat id? The only 400 this client can explain to itself.
 *
 *  It mirrors the server's `ids.ValidChatID` deliberately: the question is not whether the id
 *  is valid — the server answers that — but whether the 400 could have come from an id gate
 *  at all. KEEP IT AT LEAST AS PERMISSIVE as the server's, because accepting an id the server
 *  would refuse costs one non-terminal `unresolved`, while refusing one it would ACCEPT reads
 *  a request-level 400 as "no such chat". Length in UTF-16 units is exact here: every
 *  character this admits is ASCII, so a non-ASCII id fails the charset test first. */
function chatIDShaped(id: string): boolean {
  return id !== "" && id.length <= 128 && /^[A-Za-z0-9_-]+$/.test(id);
}

/** Does this status settle the question ABOUT THIS CHAT, rather than about the request?
 *  404 is the server reading its own store. A 400 counts only for an id this client can see
 *  is not a chat id, because a request-level 400 — a stale CSRF header, a host check, a body
 *  limit — is not evidence about a conversation, and reading one as "no such chat" is the
 *  false-terminal claim this whole path exists to remove. Measured against the route as it
 *  stands (`chatIDPattern`, `canonicalAPIPath`) every 400 source IS id-shaped, so the
 *  narrowing changes no verdict today; what it stops is a middleware added later making one. */
function saysTheChatIsGone(status: number, chatID: string): boolean {
  return status === 404 || (status === 400 && !chatIDShaped(chatID));
}

/** Ask the SERVER whether a chat exists, for an id the store holds no row for.
 *
 *  The store's own absence is not proof: a list that landed cleanly goes STALE, so a chat
 *  created on another device — or during an SSE outage — is missing from a store otherwise
 *  entitled to speak, and reading that as gone is the false-terminal claim. So the server
 *  decides: a 2xx adopts the header through `upsertHeader`, the same door the missed
 *  `chat_created` frame would have used, so no second Session-construction rule appears and
 *  the deep link opens. Everything `saysTheChatIsGone` does not settle is `unresolved`. */
export async function confirmChatExists(chatID: string): Promise<ChatVerdict> {
  // `limit=1` is the cheapest page the endpoint will serve (it clamps 0 and below back to
  // its 50 default), and the transcript is not what is being asked about. No abort
  // controller: two confirmations for one id are an idempotent read plus an idempotent
  // upsert, and the CALLER owns whether a late answer still matters to what is on screen.
  const r = await apiGetTypedOrError(
    `/api/chats/${encodeURIComponent(chatID)}?limit=1`,
    decodeChatConfirmResponseLocal,
  );
  if (r.ok && r.data !== null) {
    upsertHeader(r.data.chat);
    return "exists";
  }
  if (saysTheChatIsGone(r.status, chatID)) {
    return "gone";
  }
  console.warn("chat confirm: no answer", chatID, r.status, r.error);
  return "unresolved";
}

/** Whether the chat list has been read successfully at least once. An empty store has two
 *  meanings that want opposite answers: the server said there are no such chats, or the
 *  server could not be reached. Without this, a reload of any `/chat/<id>` against a
 *  restarting server rewrote the URL and claimed the conversation no longer exists, seconds
 *  after toasting that the chats could not be loaded — a terminal verdict derived from absent
 *  data. LATCHED rather than a snapshot of the last attempt: a later failed refetch does not
 *  un-know rows the store already holds, and `loadList` runs on every SSE `connected`, so a
 *  client whose boot fetch failed self-heals at its first reconnect. */
export function chatListLoaded(): boolean {
  return listLoaded;
}

/** Can asking the server about ONE chat id plausibly be answered? The gate on the
 *  confirmation round trip, narrowed to the only thing that makes asking pointless:
 *  EVIDENCE the server cannot answer. A list that landed, a list that was ABORTED (routine —
 *  see `loadList`'s first line) and a page that has not tried yet all answer true; only a
 *  load that reached the network and failed answers false. `listLoaded` is read too and is
 *  not redundant: it is LATCHED where the reach is not, so rows in the store outlive a server
 *  that has since gone down. One fold is conservative — `apiGetTyped` collapses an
 *  undecodable BODY onto a dead network, so such a list reads unreachable. */
export function serverMayAnswer(): boolean {
  return listLoaded || listReach !== "unreachable";
}

registerCleanup(() => {
  listController?.abort();
  cancelListRetry();
});
registerCleanup(() => {
  for (const c of msgControllers.values()) {
    c.abort();
  }
  msgControllers.clear();
});

// --- Load operations ---
export async function loadList(): Promise<boolean> {
  listController?.abort();
  const controller = new AbortController();
  listController = controller;

  const sessionIndex = new Map<string, Session>();
  for (const s of getSessions()) {
    sessionIndex.set(s.id, s);
  }
  const knownBefore = new Set(sessionIndex.keys());

  const d = await apiGetTyped("/api/chats", decodeChatListResponseLocal, controller.signal);
  if (controller.signal.aborted) {
    // `listReach` is deliberately NOT written here. This request was superseded —
    // by a `connected` refetch, or by the page unloading — and it never learned
    // anything about the server, so recording a verdict would put the abort's own
    // false return in front of every later reader. That is the conflation
    // `serverMayAnswer` exists to undo.
    listController = null;
    return false;
  }
  if (d?.chats === undefined) {
    // The request DID resolve and produced no usable list, so the server is the
    // best available explanation. See `serverMayAnswer` for the one case this
    // over-attributes (an undecodable body) and why the fold is safe.
    listReach = "unreachable";
    listController = null;
    return false;
  }
  const next: Session[] = [];
  for (const h of d.chats) {
    const existing = get(h.id);
    const session: Session = {
      id: h.id,
      name: h.name,
      model: h.model ?? "",
      acp_session_id: h.acp_session_id ?? "",
      current_mode_id: h.current_mode_id ?? "",
      supervised_mode: h.supervised_mode ?? false,
      effort: h.effort ?? "",
      // Keep the client's live effort catalog when the header carries none: this
      // list endpoint rebuilds a Session from a header, and blanking the tiers
      // would empty the effort control for every chat on a refresh.
      effort_levels: h.effort_levels ?? existing?.effort_levels ?? [],
      effort_active: h.effort_active ?? existing?.effort_active ?? "",
      usage: h.usage,
      message_count: h.message_count,
      messages: existing?.messages ?? [],
      // A header carries no window, so this is the DERIVATION and never an answer — one
      // rule, `store.ts` `derivedHasMore`, over the count the server just sent and whatever
      // window is carried over above. Never OR'd with the previous value: a sticky true can
      // only be wrong in the direction of a Load-older button with nothing behind it, and
      // this runs on boot, on login and on every `connected` handshake.
      has_more: derivedHasMore(h.message_count, existing?.messages.length ?? 0),
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
      ...(existing?.steers !== undefined && { steers: existing.steers }),
      // The promoted steers too, and this one is load-bearing rather than
      // symmetric: a mark's lifetime is the loaded TRANSCRIPT, not the turn, so
      // without it every reconnect would wipe the notes back out of turns the
      // reader can still see.
      ...(existing?.steer_marks !== undefined && { steer_marks: existing.steer_marks }),
      // The two outcome latches are SERVER-SUPPLIED, with the local one carried over on
      // top: `last_turn_outcome` rides the header, so a chat this client has never seen
      // live gets a real verdict instead of the hollow `idle` ring. A moved header wins, a
      // live `turn_ended` on this page is newer than one that has not moved, and a live
      // turn seeds nothing; `latchFieldsFor` owns all four rules.
      ...latchFieldsFor(existing, h),
      // Every OTHER client-only projection is a pure carry-over: the server sends none of
      // them, so rebuilding a Session from a header alone silently resets them — and
      // `boot.ts onTransportStatus` owns which connections call this, which is more of
      // them than a reconnect, so an ordinary network recovery dropped the agent's
      // declared status. The reconcile that IS entitled to drop them is `transport:gap`,
      // which clears them explicitly and runs first.
      ...(existing?.agent_status !== undefined && { agent_status: existing.agent_status }),
      ...(existing?.agent_status_text !== undefined && {
        agent_status_text: existing.agent_status_text,
      }),
      // Residency describes the carried-over `messages` window, so it travels
      // with it: dropping it here would make every reconnect read a loaded
      // chat as never-loaded (or an evicted one as fresh).
      ...(existing?.residency !== undefined && { residency: existing.residency }),
      // The window BASE describes that same window, and it travels TOGETHER or not
      // at all, matching `adoptTurnBase`'s half-present rule. A reconnect does not
      // bump `syncEpoch`, so nothing refetches to replace a dropped base and the
      // next repaint renumbers a paged chat from 1.
      ...(existing?.turn_offset !== undefined &&
        existing.turn_segment_closed !== undefined && {
          turn_offset: existing.turn_offset,
          turn_segment_closed: existing.turn_segment_closed,
        }),
      ...(h.compaction_watermark !== undefined && { compaction_watermark: h.compaction_watermark }),
      // The two SERVER facts the row used to drop on the floor. The row is rebuilt from the
      // header rather than spread from `existing`, so a conditional spread IS a replace here:
      // nothing carries over because nothing is there. `updated_at` needs no guard — it is
      // required on the wire.
      ...(h.last_turn_outcome !== undefined && { last_turn_outcome: h.last_turn_outcome }),
      updated_at: h.updated_at,
    };
    next.push(session);
  }
  // Preserve sessions added by SSE (upsertHeader) during the await — but NOT the
  // boot snapshot's provisional rows, which satisfy the same "unknown before,
  // unnamed by the server" test and mean the opposite thing: a hint for a chat the
  // server no longer holds, which would otherwise outlive the answer that omitted
  // it. See types.ts `Session.provisional`.
  const currentSessions = getSessions();
  const currentIndex = new Map(currentSessions.map((s) => [s.id, s]));
  const nextIds = new Set(next.map((s) => s.id));
  for (const [id, s] of currentIndex) {
    if (!knownBefore.has(id) && !nextIds.has(id) && s.provisional !== true) {
      next.push(s);
    }
  }
  setSessions(next);
  listController = null;
  // Latched HERE and nowhere else: this is the one point at which the store holds a
  // row for every chat the server named, which is exactly the claim
  // `chatListLoaded` makes. Every earlier return above is an abort or a failed
  // decode and leaves it as it was.
  listLoaded = true;
  // Not latched, unlike the line above: this one describes the LAST attempt, so a
  // later failure is entitled to overwrite it.
  listReach = "reachable";
  // The list landed, so any ladder still climbing toward it is answered.
  cancelListRetry();
  return true;
}

export async function loadMessages(chatID: string, beforeID?: string): Promise<boolean> {
  msgControllers.get(chatID)?.abort();
  const controller = new AbortController();
  msgControllers.set(chatID, controller);
  const params = new URLSearchParams({
    limit: String(PAGE_MESSAGE_CAP),
    max_bytes: String(PAGE_BUDGET_BYTES),
  });
  if (beforeID !== undefined) {
    params.set("before_id", beforeID);
  }
  // The ids present BEFORE the request goes out. The server computes its answer
  // from the chat file when the handler runs, so anything that arrives while the
  // request is in flight is NEWER than that answer and the answer is not entitled
  // to drop it — a plan or event message persisted and broadcast inside that
  // window would otherwise vanish from the transcript until the next fetch.
  const knownBefore = new Set((get(chatID)?.messages ?? []).map((m) => m.id));
  // The epoch too, and before rather than after for the mirror-image reason: a
  // transport gap landing while this request is in flight may have dropped
  // events the answer predates, so the window assembled from it must not claim
  // to have survived that gap.
  const epochAtStart = syncEpoch();
  const d = await apiGetTyped(
    `/api/chats/${encodeURIComponent(chatID)}?${params.toString()}`,
    decodeChatGetResponseLocal,
    controller.signal,
  );
  if (controller.signal.aborted) {
    msgControllers.delete(chatID);
    return false;
  }
  if (d === null) {
    msgControllers.delete(chatID);
    return false;
  }
  const session = get(chatID);
  if (session === undefined) {
    msgControllers.delete(chatID);
    return false;
  }
  // Whether the page this load applies STARTS the client's window — the subject the
  // server's `has_more` and its window base both describe (the OLDEST MESSAGE HELD).
  // A `before_id` page always does: it becomes the new oldest. A no-cursor page does
  // only when nothing older was re-adopted in front of it, which the branch below
  // decides.
  let pageStartsWindow = true;
  if (beforeID !== undefined) {
    // Prepend older-page messages, deduped by id. The cursor is a message ID and
    // the server treats it as exclusive, so a boundary message cannot come back
    // twice the way the old millisecond cursor allowed. The id filter STAYS
    // anyway: it costs one Set, and it is what makes a re-issued or overlapping
    // page harmless rather than a double render that also corrupts the msg index.
    const seen = new Set(session.messages.map((m) => m.id));
    const older = d.messages.filter((m) => !seen.has(m.id)).map(normalizeMessage);
    session.messages = [...older, ...session.messages];
  } else {
    // Normalize replayed messages so legacy transcripts (persisted before the
    // blocks field) get synthesized blocks — the renderer is block-only.
    const fetched = d.messages.map(normalizeMessage);
    // Then re-adopt what this page CANNOT know about, and NEVER by position: the agent
    // persists messages DURING a turn — a plan update, a compaction or safety event, the
    // cancel badge — each landing after the streaming reply locally while sitting inside
    // the page, so a boundary derived from the page's newest id steps past the reply and
    // the replace deletes it. Exactly two things qualify: the in-flight turn, which the
    // server holds in an in-memory buffer until turn_ended so only the store's own marker
    // names it, and a message that arrived while the request was in flight. Both go at the
    // END, where the server puts the finished turn too.
    const fetchedIDs = new Set(fetched.map((m) => m.id));
    const liveID = liveTurnMessage(chatID);
    // And the RESIDENT OLDER PAGES go back in front. The page is a CONTIGUOUS newest
    // window, so held messages older than its oldest one are pages this client already
    // fetched that the answer says nothing about; dropping them threw a paged-up reader's
    // history away — and their scroll position with it — on every no-cursor reload, which
    // is what the gap heal is. Anchored on that oldest id rather than on a count or a
    // timestamp: no overlap means the window moved out from under what is held, and then
    // the page replaces, which is the honest answer.
    const oldest = fetched[0]?.id;
    const anchor = oldest === undefined ? -1 : session.messages.findIndex((m) => m.id === oldest);
    const older =
      anchor > 0 ? session.messages.slice(0, anchor).filter((m) => !fetchedIDs.has(m.id)) : [];
    const kept = session.messages
      .slice(anchor > 0 ? anchor : 0)
      .filter((m) => !fetchedIDs.has(m.id) && (m.id === liveID || !knownBefore.has(m.id)));
    session.messages = [...older, ...fetched, ...reorderKept(kept, liveID)];
    // A card already on screen does not read the array this line just replaced: its DOM
    // has one refresh channel, the per-call signal, and the repaint below writes none. So
    // the page's own calls go through that channel — which is what makes a card built from
    // the boot snapshot's truncated copy show the server's output rather than keeping the
    // hint for the life of the document. Only the FETCHED rows: a prepended older page
    // mounts its cards fresh, and the live turn's calls arrive on their own signal.
    republishWindowToolCalls(chatID, fetched);
    // The answer describes what is older than the PAGE, which is only the same
    // question the session's flag and base answer when nothing older sits in front
    // of it.
    pageStartsWindow = older.length === 0;
  }
  // Before the two reads below, so both see the server's own count.
  session.message_count = d.chat.message_count;
  if (pageStartsWindow) {
    session.has_more = d.has_more;
    adoptTurnBase(session, d.turn_offset, d.turn_segment_closed);
  } else {
    // The page said nothing about this window's left edge, so `has_more` falls back to the
    // derivation rather than preserving the previous value: preserving is only right when
    // that value was an ANSWER, and for a header-built row it is the guess, which left a
    // button on a chat holding every message it has. The base IS left alone, for the mirror
    // reason — the edge did not move, so whatever was recorded still describes it.
    session.has_more = derivedHasMore(session.message_count, session.messages.length);
  }
  rebuildMsgIndex(chatID, session.messages);
  msgControllers.delete(chatID);
  // Park the server's draft on the session so the composer can adopt it. Only on
  // the newest page: an older page fetch is a scroll-up, not an open. This module
  // deliberately does not reach into the composer — chat.ts owns that call, right
  // where it already sequences the rest of the activation.
  if (beforeID === undefined) {
    session.draft = d.draft;
    // The server's liveness statement, newest page ONLY, for the draft's reason: an
    // older-page fetch is a scroll-up and asserts nothing about liveness. Written on the
    // session directly, because this block mutates it in place and `bumpMessages` below is
    // the one repaint. RECORDED in both directions, and FORGOTTEN when the answer carries no
    // statement — the window base's rule above, because a `true` left standing would keep
    // `turnLive` answering live off an answer nothing restates. The false is unambiguous:
    // `hasOpenTurn` counts an ADMITTED prompt as well as a minted turn record, so it no
    // longer reads false for the bridge-ready window between the two, which is what makes
    // the heal below admissible — no turn and no admitted prompt.
    if (d.turn_open === undefined) {
      delete session.turn_open;
    } else {
      session.turn_open = d.turn_open;
    }
    // The CONTENT behind that liveness statement. Newest page only, like the two above,
    // and AFTER the splice so the upsert sees the merged window. No duplicate is possible
    // either way: the merge is keyed by message id and the live turn is not in `messages`.
    if (d.live_turn !== undefined) {
      adoptLiveTurn(chatID, d.live_turn);
    }
    // A successful newest-page load is the ONE writer of `loaded`: the window
    // is now the server's answer, so an activation may trust it. An older-page
    // prepend extends an already-trusted window and asserts nothing new. The
    // epoch stamped is the one captured before the request, so a load that
    // raced a gap records a claim that already reads stale.
    session.residency = "loaded";
    noteLoaded("chat", chatID, epochAtStart);
  }
  // `load`, not `shape`: both branches above REPLACED or EXTENDED the window
  // with the server's own answer, so its rows are a replay and the paint must
  // not read them as messages that arrived here (messages.ts `appendNewIds`).
  // The array cannot say so on its own — a cold open paints before this fetch
  // resolves, so the paint it drives is not a chat switch and its predecessor
  // recorded no tail to append past.
  bumpMessages(chatID, "load");
  if (beforeID === undefined) {
    // The page carries the last turn's PERSISTED outcome, so the outcome latches — client
    // memory the gap door just dropped, or a fresh page never had — are re-derived from it.
    // Both arms sit after `bumpMessages`, so the repaint and the dot read one settled window.
    //
    // A stated `turn_open === false` covers the chat's WHOLE liveness, so it also retracts a
    // `thinking` this client is holding for a turn that is over — the one door licensed to
    // run the full teardown. Anything else only re-derives, because the page has asserted
    // nothing about liveness that would let it drop a live turn's markers.
    if (d.turn_open === false) {
      healSettledChat(chatID);
    } else {
      relatchTurnVerdict(chatID);
    }
  }
  return true;
}
