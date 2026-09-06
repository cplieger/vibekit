// HTTP load operations for the session store: hydrates the state store.ts owns.

import type { Session, ChatHeader, Message } from "./types.js";
import { apiGetTyped, apiGetTypedOrError } from "./api-client.js";
import { asObject, decodeArray, reqBool, type Decoder } from "./validators.js";
import { decodeChatHeader, decodeMessage } from "./wire/decoders.gen.js";
import { registerCleanup } from "./actions/index.js";
import {
  setSessions,
  get,
  getSessions,
  rebuildMsgIndex,
  bumpMessages,
  normalizeMessage,
  liveTurnMessage,
  relatchTurnVerdict,
  latchFieldsFor,
  syncEpoch,
  upsertHeader,
} from "./store.js";

// --- Inline decoders ---
const decodeChatListResponseLocal: Decoder<{ chats?: ChatHeader[] }> = (v) => {
  const o = asObject(v, "$.chat_list");
  const out: { chats?: ChatHeader[] } = {};
  if (o["chats"] !== undefined) {
    out.chats = decodeArray(o["chats"], decodeChatHeader, "$.chat_list.chats");
  }
  return out;
};

const decodeChatGetResponseLocal: Decoder<{
  chat: ChatHeader;
  messages: Message[];
  has_more: boolean;
  draft: string;
  turn_open: boolean;
}> = (v) => {
  const o = asObject(v, "$.chat_get");
  return {
    chat: decodeChatHeader(o["chat"]),
    messages: decodeArray(o["messages"], decodeMessage, "$.chat_get.messages"),
    has_more: reqBool(o, "has_more", "$.chat_get"),
    // Both fields are optional-tolerant: an older server, or a proxy that strips one,
    // must not fail the whole chat load. `store.ts` turnLive is turn_open's one reader.
    turn_open: o["turn_open"] === true,
    draft: typeof o["draft"] === "string" ? o["draft"] : "",
  };
};

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

// --- Abort controllers ---
let listController: AbortController | null = null;
const msgControllers = new Map<string, AbortController>();

/** Whether `loadList` has ever succeeded. Read through `chatListLoaded`. */
let listLoaded = false;

/** What the last `loadList` attempt established about the SERVER, not about that request.
 *  An abort is a fact about a request only, so the abort path leaves this alone. */
type ListReach = "unknown" | "reachable" | "unreachable";
let listReach: ListReach = "unknown";

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

/** Whether an id is SHAPED like a chat id, which is the only 400 this client can
 *  explain to itself.
 *
 *  It mirrors the server's own gate (`ids.ValidChatID`: non-empty, at most 128
 *  bytes, and nothing outside `[A-Za-z0-9_-]`), and the mirror is deliberate rather
 *  than duplicated knowledge escaping: the question being asked is not "is this id
 *  valid" — the server answers that — but "could the 400 I just received have come
 *  from an id gate at all". Only a rule this client holds can answer that.
 *
 *  THE DRIFT DIRECTION IS THE WHOLE SAFETY ARGUMENT, so keep this predicate at
 *  least as PERMISSIVE as the server's. Accepting an id the server would refuse
 *  costs an `unresolved` — a held URL and a retry, non-terminal. Refusing one the
 *  server would ACCEPT is what re-opens the false-terminal claim, because a
 *  request-level 400 from some later middleware would then be read as "no such
 *  chat". A server that loosens its charset must be followed here; a server that
 *  tightens it needs no change.
 *
 *  Length in UTF-16 code units rather than bytes is exact for the accepting set:
 *  every character it admits is ASCII, and a non-ASCII id fails the charset test
 *  first. */
function chatIDShaped(id: string): boolean {
  return id !== "" && id.length <= 128 && /^[A-Za-z0-9_-]+$/.test(id);
}

/** Ask the SERVER whether a chat exists, for an id the store holds no row for.
 *
 *  `chatListLoaded` below answers "has this client ever read a list", which is not
 *  the claim a reader needs before saying a conversation is gone: a list that
 *  landed cleanly goes STALE, so a chat created on another device — or during an
 *  SSE outage, or while this client's stream lagged — is missing from a store that
 *  is otherwise entitled to speak. Reading that absence as proof is the same
 *  false-terminal-claim class one population narrower.
 *
 *  So the server decides, and the mapping is what makes the verdict honest:
 *
 *   - 2xx: the chat EXISTS, and the header is adopted through `upsertHeader` — the
 *     same door the `chat_created` frame this client missed would have used, so no
 *     second Session-construction rule is introduced. The deep link then opens
 *     rather than dead-ending, which is the whole point of asking.
 *   - 404: the server read its own store and there is no such chat. Terminal.
 *   - 400 FOR AN ID THIS CLIENT CAN SEE IS NOT A CHAT ID: the server refused the
 *     id itself, and no retry makes a malformed id a chat. Terminal. Narrowed to
 *     the id-shape case on purpose — measured against the route as it stands the
 *     only 400 sources ARE id-shaped (`chatIDPattern`, and `canonicalAPIPath`,
 *     which a well-shaped id cannot trip because `encodeURIComponent` is the
 *     identity over that charset), so this changes no verdict today. What it stops
 *     is a MIDDLEWARE answering 400 for a request-level reason later — a stale
 *     CSRF header, a host check, a body limit — being read as a terminal claim
 *     about the conversation. That is the exact class this whole path exists to
 *     eliminate, and a status is not evidence about a chat unless something ties
 *     it to the chat.
 *   - anything else (5xx, a 400 for a well-shaped id, a dead network, a timeout,
 *     an abort, an undecodable body — every one of which `@cplieger/fetch` reports
 *     with a status the tests above do not match): NOBODY ANSWERED about this
 *     chat. `unresolved`, and the caller holds what it has.
 *
 *  No abort controller, deliberately: two confirmations for one id are an
 *  idempotent read plus an idempotent upsert, and the caller — not this module —
 *  owns whether a late answer is still relevant to the location on screen. */
export async function confirmChatExists(chatID: string): Promise<ChatVerdict> {
  // `limit=1` is the cheapest page the endpoint will serve (it clamps 0 and below
  // back to its 50 default), and the transcript is not what is being asked about.
  const r = await apiGetTypedOrError(
    `/api/chats/${encodeURIComponent(chatID)}?limit=1`,
    decodeChatConfirmResponseLocal,
  );
  if (r.ok && r.data !== null) {
    upsertHeader(r.data.chat);
    return "exists";
  }
  if (r.status === 404 || (r.status === 400 && !chatIDShaped(chatID))) {
    return "gone";
  }
  console.warn("chat confirm: no answer", chatID, r.status, r.error);
  return "unresolved";
}

/** Whether the chat list has been read successfully at least once.
 *
 *  An empty store has two meanings and they want opposite answers: the server said
 *  there are no such chats, or the server could not be reached. `app.ts` handles a
 *  failed boot fetch by toasting and creating a fresh chat, then applies the URL's
 *  route anyway — so without this predicate a reload of any `/chat/<id>` against a
 *  restarting server rewrote the URL and claimed the conversation no longer exists,
 *  seconds after saying the chats could not be loaded. That is the same defect the
 *  `turn_open` field removes one surface over: a terminal verdict derived from
 *  absent data.
 *
 *  Latched rather than a snapshot of the last attempt: once a list has landed the
 *  store holds a row per chat, and a LATER failed refetch does not un-know them.
 *  It also self-heals — `loadList` runs on every SSE `connected`, so a client whose
 *  boot fetch failed starts answering true at its first successful reconnect.
 *
 *  What it is NOT is a licence to make the terminal claim: it says a list landed
 *  ONCE, and that claim weakens with every second the store goes stale. The claim
 *  itself now comes from `confirmChatExists` above, so this predicate survives as
 *  the cheap short-circuit for the case whose answer is already known — a client
 *  whose list never loaded has been told the server is unreachable, and boot's own
 *  Reload action is that reader's retry, so spending a round trip to be told the
 *  same thing buys nothing. */
export function chatListLoaded(): boolean {
  return listLoaded;
}

/** Whether asking the server about ONE chat id can plausibly be answered.
 *
 *  This is the gate on the confirmation round trip, and it replaced
 *  `chatListLoaded()` in that role because that predicate answers a different
 *  question and the difference cost a population. The short-circuit's argument was
 *  that a client whose list never loaded would be told `unresolved` one request
 *  later, so the trip buys nothing — TRUE when the server is down, and FALSE when
 *  the boot load was ABORTED, which `loadList`'s own first line makes routine. In
 *  that population the chat exists, the server would answer 200, and the deep link
 *  dead-ended anyway.
 *
 *  So the question is narrowed to the only thing that makes asking pointless:
 *  EVIDENCE the server cannot answer. A list that landed, a list that was aborted,
 *  and a page that has not tried yet all answer true; only a load that reached the
 *  network and failed answers false.
 *
 *  `listLoaded` is read as well as `listReach`, and it is not redundant — it is
 *  LATCHED where the reach is not. A boot that loaded fine followed by a refetch
 *  against a server that has since gone down leaves rows in the store and a reader
 *  who has been told nothing (only boot toasts), so a deep link there is worth one
 *  request and a retry affordance rather than silence.
 *
 *  ONE case is folded conservatively and it is worth stating: `apiGetTyped` collapses
 *  an undecodable body and a dead network to the same null, so a list whose BODY
 *  failed to decode is recorded `unreachable` even though the server answered. That
 *  fold keeps round 3's behaviour for the population it was written for, and it
 *  costs nothing a reader can act on — a client that cannot decode the chat list
 *  cannot render a chat either, and boot has already toasted. */
export function serverMayAnswer(): boolean {
  return listLoaded || listReach !== "unreachable";
}

registerCleanup(() => listController?.abort());
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
      available_modes: h.available_modes ?? [],
      available_models: h.available_models ?? [],
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
      has_more:
        existing !== undefined
          ? existing.has_more || h.message_count > existing.messages.length
          : h.message_count > 0,
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
      ...(existing?.steers !== undefined && { steers: existing.steers }),
      // The promoted steers too, and this one is load-bearing rather than
      // symmetric: a mark's lifetime is the loaded TRANSCRIPT, not the turn, so
      // without it every reconnect would wipe the notes back out of turns the
      // reader can still see.
      ...(existing?.steer_marks !== undefined && { steer_marks: existing.steer_marks }),
      // The two outcome latches are SERVER-SUPPLIED now, with the local one
      // carried over on top. They used to be a pure carry-over, and the comment
      // here called this list "the header endpoint's blind spot" — that is the
      // half that is fixed: `last_turn_outcome` rides the header, so a chat this
      // client has never seen live gets a real verdict instead of the hollow
      // `idle` ring. The carry-over still wins where it exists (a live
      // `turn_ended` on this page is newer than any header read) and a live turn
      // seeds nothing; `latchFieldsFor` owns all three rules.
      ...latchFieldsFor(existing, h),
      // The rest of the client-only projections remain a pure carry-over: the
      // server sends none of them, so rebuilding a Session from a header alone
      // silently resets them — and `loadList` runs on EVERY `connected`,
      // reconnects included, so an ordinary network recovery dropped the agent's
      // declared status. The reconcile that IS entitled to drop them is
      // `transport:gap`, which clears them explicitly and runs first, so there is
      // nothing left here to preserve after a real replay gap.
      ...(existing?.agent_status !== undefined && { agent_status: existing.agent_status }),
      ...(existing?.agent_status_text !== undefined && {
        agent_status_text: existing.agent_status_text,
      }),
      // Residency describes the carried-over `messages` window, so it travels
      // with it: dropping it here would make every reconnect read a loaded
      // chat as never-loaded (or an evicted one as fresh). `loadedEpoch` is the
      // other half of the same claim and travels for the same reason.
      ...(existing?.residency !== undefined && { residency: existing.residency }),
      ...(existing?.loadedEpoch !== undefined && { loadedEpoch: existing.loadedEpoch }),
      ...(h.compaction_watermark !== undefined && { compaction_watermark: h.compaction_watermark }),
    };
    next.push(session);
  }
  // Preserve sessions added by SSE (upsertHeader) during the await.
  const currentSessions = getSessions();
  const currentIndex = new Map(currentSessions.map((s) => [s.id, s]));
  const nextIds = new Set(next.map((s) => s.id));
  for (const [id, s] of currentIndex) {
    if (!knownBefore.has(id) && !nextIds.has(id)) {
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
  return true;
}

export async function loadMessages(
  chatID: string,
  beforeID?: string,
  limit = 50,
): Promise<boolean> {
  msgControllers.get(chatID)?.abort();
  const controller = new AbortController();
  msgControllers.set(chatID, controller);
  const params = new URLSearchParams({ limit: String(limit) });
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
    // Then re-adopt what this page CANNOT know about. A blind whole-array
    // replace deleted the reply the reader was watching on every mid-turn
    // refetch: a tab switch, the boot activation after a refresh, and the gap
    // handler's own heal.
    //
    // Exactly two things qualify, and neither is decided by position:
    //
    //  - The in-flight turn. The server accumulates it in an in-memory buffer
    //    and appends it to the chat file once, at turn_ended, so the page cannot
    //    carry it and the store's own marker is what names it. The rule this
    //    replaces kept "everything after the newest id the page carries", and
    //    that boundary is wrong because the agent persists messages DURING a
    //    turn — every plan update, a compaction or safety event, the cancel
    //    badge — each landing after the streaming reply locally while sitting
    //    inside the page. One plan update stepped the boundary past the reply,
    //    and the replace deleted it; the reader saw their own prompt with an
    //    empty turn body until a reload, by which time the buffer had flushed.
    //
    //  - A message that arrived while the request was in flight, which is newer
    //    than the answer being applied.
    //
    // Both go at the END, which is where the server puts the finished turn too:
    // persistTurn appends it after anything persisted during it. Everything else
    // the page omits is the page's own business — older history it deliberately
    // left out, which re-appending would reorder.
    const fetchedIDs = new Set(fetched.map((m) => m.id));
    const liveID = liveTurnMessage(chatID);
    const kept = session.messages.filter(
      (m) => !fetchedIDs.has(m.id) && (m.id === liveID || !knownBefore.has(m.id)),
    );
    session.messages = kept.length === 0 ? fetched : [...fetched, ...reorderKept(kept, liveID)];
  }
  session.message_count = d.chat.message_count;
  session.has_more = d.has_more;
  rebuildMsgIndex(chatID, session.messages);
  msgControllers.delete(chatID);
  // Park the server's draft on the session so the composer can adopt it. Only on
  // the newest page: an older page fetch is a scroll-up, not an open. This module
  // deliberately does not reach into the composer — chat.ts owns that call, right
  // where it already sequences the rest of the activation.
  if (beforeID === undefined) {
    session.draft = d.draft;
    // The server's liveness statement, newest page ONLY, for the draft's reason: an
    // older-page fetch is a scroll-up and asserts nothing about whether a turn is
    // running now. Written on the session object directly rather than through
    // `setTurnOpen`, because this whole block is mutating the session in place and
    // `bumpMessages` below is the one repaint.
    session.turn_open = d.turn_open;
    // A successful newest-page load is the ONE writer of `loaded`: the window
    // is now the server's answer, so an activation may trust it. An older-page
    // prepend extends an already-trusted window and asserts nothing new. The
    // epoch stamped is the one captured before the request, so a load that
    // raced a gap records a claim that already reads stale.
    session.residency = "loaded";
    session.loadedEpoch = epochAtStart;
  }
  // `load`, not `shape`: both branches above REPLACED or EXTENDED the window
  // with the server's own answer, so its rows are a replay and the paint must
  // not read them as messages that arrived here (messages.ts `appendNewIds`).
  // The array cannot say so on its own — a cold open paints before this fetch
  // resolves, so the paint it drives is not a chat switch and its predecessor
  // recorded no tail to append past.
  bumpMessages(chatID, "load");
  if (beforeID === undefined) {
    // The page carries the last turn's PERSISTED outcome, so the outcome
    // latches — client memory the gap door just dropped, or a fresh page
    // never had — are re-derived from it. After bumpMessages, so the repaint
    // and the dot read one settled window.
    relatchTurnVerdict(chatID);
  }
  return true;
}
