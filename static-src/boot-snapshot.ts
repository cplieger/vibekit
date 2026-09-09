// ---------------------------------------------------------------------------
// The boot snapshot: a bounded projection of what THIS SCREEN was showing, held
// in IndexedDB so a resume paints before the network answers. A PAINT-TIME HINT
// with the standing the theme's localStorage cache has: it advances no tab-set
// version, and `boot.ts` paints it only while its own chat read has yet to ANSWER,
// so the server's answer always wins.
// ---------------------------------------------------------------------------

import { effect } from "@cplieger/reactive";
import {
  derivedHasMore,
  get,
  getActiveId,
  getSessions,
  messagesVersionOf,
  normalizeMessage,
  setSessions,
  turnBaseOf,
  watchActiveId,
} from "./store.js";
import { openTabSubjects, paintProvisionalTabs, tabSetVersion } from "./tabs.js";
import { projectTurns, type Turn, type TurnWindowBase } from "./turns.js";
import type { Message, Session, TabSubject, Usage } from "./types.js";
import { asObject, decodeArray, reqBool, reqNum, reqStr } from "./validators.js";
import { decodeMessage, decodeTabSubject, decodeUsage } from "./wire/decoders.gen.js";

/** How many of the active chat's newest TURNS are carried. Turns because half a
 *  turn renders as a card with no header. */
const SNAPSHOT_TURNS = 3;

/** Hard cap on the messages those turns may contribute: one turn can hold hundreds
 *  of tool rows, so the turn bound alone is not a bound. Bytes need no third rule —
 *  this is a subset of the server's byte-bounded window (store-load.ts). */
const SNAPSHOT_MAX_MESSAGES = 40;

/** How long the projection must sit still before it is written. Every write is a
 *  whole-record replace, so a streaming turn would otherwise write per frame. */
const SNAPSHOT_DEBOUNCE_MS = 1_000;

const DB_NAME = "vibekit-boot";
const DB_VERSION = 1;
const STORE_NAME = "snapshot";
const RECORD_KEY = "current";

/** One chat row, bounded to what the strip and the context bar paint from it.
 *
 *  Not a `ChatHeader`: that wire type requires `created_at`/`updated_at`, which a
 *  `Session` does not carry, so reusing it would mean inventing two timestamps for
 *  fields nothing here reads. */
interface SnapshotChat {
  readonly id: string;
  readonly name: string;
  readonly model: string;
  readonly current_mode_id: string;
  readonly message_count: number;
  readonly usage: Usage;
}

/** The transcript window a resume paints, and the segmentation state it is numbered
 *  from. ONE value because the two are only meaningful together: an offset with no
 *  messages numbers a window that is not there, and messages with no offset number
 *  themselves from 1. */
interface SnapshotWindow {
  readonly chat_id: string;
  readonly messages: readonly Message[];
  readonly base: TurnWindowBase;
}

/** What one screen was showing. No active tab: `device-view.ts` persists that per
 *  screen already, and `tabs.ts`'s reset reads it. */
export interface BootSnapshot {
  readonly tabs: readonly TabSubject[];
  readonly chats: readonly SnapshotChat[];
  /** `null` is the explicit "no chat was active", which is why there is no
   *  empty-string sentinel to test for. */
  readonly window: SnapshotWindow | null;
}

/** Read the persisted snapshot. Resolves `null` for every failure — an absent
 *  record, a browser with no IndexedDB, a corrupt or foreign payload — because a
 *  paint-time hint has no failure a caller could act on. */
export async function readBootSnapshot(): Promise<BootSnapshot | null> {
  return decodeSnapshot(await readRecord());
}

/** Forget what this screen was showing, and stop capturing.
 *
 *  Both sign-out doors reach it and `boot.ts` owns both. It does NOT un-paint the
 *  current frame — those rows are the ones this device already had on screen — so
 *  what it buys is that the NEXT boot paints nothing.
 *
 *  Every writer stops, the `pagehide` listener included: a page transition after
 *  this would otherwise re-write the record it just deleted. */
export async function clearBootSnapshot(): Promise<void> {
  clearTimeout(pending);
  pending = undefined;
  disposeCapture?.();
  disposeCapture = undefined;
  captureAbort?.abort();
  captureAbort = undefined;
  await deleteRecord();
}

/** Paint a snapshot, and report whether it painted anything.
 *
 *  ORDERED: the chat rows go in first because a chat tab's label is read from the
 *  store while its row is built (`tab-materialize.ts` `chatName`).
 *
 *  `setSessions` REPLACES the store, so this may only run BEFORE the boot's own chat
 *  list lands; `boot.ts`'s `restoreWorkspace` owns that ordering. The transport holds
 *  every SSE frame until `markHydrated`, so there is no other writer to lose to. */
export function paintBootSnapshot(snap: BootSnapshot | null): boolean {
  if (snap === null || snap.tabs.length === 0) {
    return false;
  }
  const win = snap.window;
  setSessions(
    snap.chats.map((c) => toProvisionalSession(c, win?.chat_id === c.id ? win : undefined)),
  );
  paintProvisionalTabs(snap.tabs);
  return true;
}

/** A chat row and, for the chat the snapshot carried one for, its window — the ONE
 *  door onto a provisional session's messages, so a row holding them holds a base too.
 *
 *  `residency` is left unset deliberately: `transcriptStale` then reads true, so the
 *  activation this paint enables refetches the window rather than trusting a hint —
 *  which is what makes the server's answer overwrite this and not the reverse.
 *  `provisional` covers the rows that answer does not name; its rule is at `types.ts`
 *  `Session.provisional`. */
function toProvisionalSession(c: SnapshotChat, win: SnapshotWindow | undefined): Session {
  const messages = win === undefined ? [] : win.messages.map(normalizeMessage);
  return {
    id: c.id,
    name: c.name,
    model: c.model,
    acp_session_id: "",
    current_mode_id: c.current_mode_id,
    usage: c.usage,
    messages,
    message_count: c.message_count,
    // Through the shared derivation, so one rule answers this for every row built
    // without a window — and the count it reads is the REAL resident count, because
    // the row and its window are built by this one call.
    has_more: derivedHasMore(c.message_count, messages.length),
    // TOGETHER or not at all, matching `store-load.ts` `adoptTurnBase`: a row with no
    // window states no base, so `turnBaseOf` numbers nothing from an edge that is not
    // there.
    ...(win !== undefined && {
      turn_offset: win.base.offset,
      turn_segment_closed: win.base.closed,
    }),
    thinking: false,
    working_label: "Thinking",
    provisional: true,
  };
}

let pending: ReturnType<typeof setTimeout> | undefined;
/** The capture's effect, or undefined while nothing is capturing. Production
 *  disposes it on a sign-out; a test that drives two boots does too. */
let disposeCapture: (() => void) | undefined;
/** Lifetime of the capture's DOM listener, so `clearBootSnapshot` can revoke it. */
let captureAbort: AbortController | undefined;

/** Start persisting the projection. Called once, from the post-auth door: there
 *  is nothing worth remembering about a login screen. */
export function startBootSnapshot(): void {
  if (disposeCapture !== undefined) {
    return;
  }
  captureAbort = new AbortController();
  disposeCapture = effect(() => {
    // The three reads that move the projection: the tab set (which a rename also
    // bumps, via `renameTab`), which chat is active, and that chat's transcript.
    tabSetVersion();
    const active = watchActiveId();
    if (active !== "") {
      // eslint-disable-next-line @typescript-eslint/no-unused-expressions
      messagesVersionOf(active).value;
    }
    schedule();
  });
  // Best-effort, on the last event a backgrounded PWA reliably gets: the debounce is
  // usually still pending, and the write may not land.
  addEventListener(
    "pagehide",
    () => {
      flush();
    },
    { signal: captureAbort.signal },
  );
}

function schedule(): void {
  clearTimeout(pending);
  pending = setTimeout(flush, SNAPSHOT_DEBOUNCE_MS);
}

function flush(): void {
  clearTimeout(pending);
  pending = undefined;
  void writeRecord(captureBootSnapshot());
}

/** Project the live state into a snapshot. Exported for the capture test, which
 *  is the only reader outside `flush`. */
export function captureBootSnapshot(): BootSnapshot {
  const active = getActiveId();
  const tabs = openTabSubjects();
  // Only the chats a tab names: the store also holds closed ones, not what was on screen.
  const open = new Set(tabs.filter((t) => t.kind === "chat").map((t) => t.ref));
  return {
    tabs,
    chats: getSessions()
      .filter((s) => open.has(s.id))
      .map(projectChat),
    window: newestWindow(active),
  };
}

function projectChat(s: Session): SnapshotChat {
  return {
    id: s.id,
    name: s.name,
    model: s.model,
    current_mode_id: s.current_mode_id,
    message_count: s.message_count,
    usage: s.usage,
  };
}

/** The active chat's newest turns, flattened back to messages and capped, WITH the
 *  base they are numbered from.
 *
 *  `projectTurns` is the app's own segmentation, so this cuts on the boundary the
 *  transcript renders and there is no second turn rule to drift from it. BOTH bounds
 *  cut there: walking newest-first is what makes the message cap drop whole turns
 *  instead of slicing the flattened tail, which would keep a body whose trigger is
 *  gone — the headerless card SNAPSHOT_TURNS exists to prevent. */
function newestWindow(chatID: string): SnapshotWindow | null {
  const s = get(chatID);
  if (s === undefined) {
    return null;
  }
  // The window's OWN base, and both halves of this module need it for different
  // reasons. The WRITE path below reads a turn's messages and never its `n`, so the
  // cut would be identical without one. The READ path renders it: `paintBootSnapshot`
  // hands this window to `toProvisionalSession`, whose session the transcript projects
  // through `turnBaseOf` — so an absent base numbers a partial tail from #1 and every
  // card's number, and its `turnAnchorID`, moves when the activation refetch lands.
  const turns = projectTurns(s.messages, false, turnBaseOf(s)).slice(-SNAPSHOT_TURNS);
  const out: Message[] = [];
  let first: Turn | undefined;
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i];
    if (t === undefined) {
      continue;
    }
    const trigger = t.trigger;
    const flat = trigger === undefined ? t.body : [trigger, ...t.body];
    if (out.length + flat.length <= SNAPSHOT_MAX_MESSAGES) {
      out.unshift(...flat);
      first = t;
      continue;
    }
    if (out.length === 0) {
      // The newest turn alone over budget is the case the cap exists for, and dropping
      // it would resume with no transcript. Trimmed from its OLD end, trigger first.
      const head = trigger === undefined ? [] : [trigger];
      out.push(...head, ...t.body.slice(-(SNAPSHOT_MAX_MESSAGES - head.length)));
      first = t;
    }
    break;
  }
  if (first === undefined) {
    return null;
  }
  return {
    chat_id: chatID,
    messages: out,
    // `closed: false` is forced, not chosen: every cut above is at a turn boundary, so
    // the first carried message force-opens through `projectTurns`' `open === undefined`
    // and both arms of its `closed` update then compute what the parent scan computed.
    // `true` would let a step or event row inside the over-budget fragment open a turn
    // the parent never opened, since `opensHeaderlessTurn` is monotone in `prevClosed`.
    base: { offset: first.n - 1, closed: false },
  };
}

/** Narrow a persisted record: `unknown` in, every ELEMENT validated through the
 *  generated wire decoders the network path already runs. A rejection is the whole
 *  record, because a half-valid hint is not a hint. */
function decodeSnapshot(v: unknown): BootSnapshot | null {
  try {
    const o = asObject(v, "$.boot_snapshot");
    const win = o["window"];
    return {
      tabs: decodeArray(o["tabs"], decodeTabSubject, "$.boot_snapshot.tabs"),
      chats: decodeArray(o["chats"], decodeSnapshotChat, "$.boot_snapshot.chats"),
      // `null` is the value; ABSENT is a foreign record and rejects like any other
      // missing required field, so the first boot after this shape ships paints
      // nothing and the capture rewrites the record within the debounce. Invariant 5
      // (no migration code), and `DB_VERSION` stays put: the object store is
      // unchanged and only the record's own shape moved.
      window: win === null ? null : decodeSnapshotWindow(win),
    };
  } catch {
    return null;
  }
}

function decodeSnapshotWindow(v: unknown): SnapshotWindow {
  const o = asObject(v, "$.boot_snapshot.window");
  const base = asObject(o["base"], "$.boot_snapshot.window.base");
  return {
    chat_id: reqStr(o, "chat_id", "$.boot_snapshot.window"),
    messages: decodeArray(o["messages"], decodeMessage, "$.boot_snapshot.window.messages"),
    // REQUIRED, both halves: `store-load.ts` `adoptTurnBase` has to FORGET a
    // half-present base, and there is nothing here yet to forget — so a window whose
    // base is half-present is a half-valid hint, and the record goes.
    base: {
      offset: reqNum(base, "offset", "$.boot_snapshot.window.base"),
      closed: reqBool(base, "closed", "$.boot_snapshot.window.base"),
    },
  };
}

function decodeSnapshotChat(v: unknown): SnapshotChat {
  const o = asObject(v, "$.boot_snapshot.chat");
  return {
    id: reqStr(o, "id", "$.boot_snapshot.chat"),
    name: reqStr(o, "name", "$.boot_snapshot.chat"),
    model: reqStr(o, "model", "$.boot_snapshot.chat"),
    current_mode_id: reqStr(o, "current_mode_id", "$.boot_snapshot.chat"),
    message_count: reqNum(o, "message_count", "$.boot_snapshot.chat"),
    usage: decodeUsage(o["usage"]),
  };
}

/** The one connection, opened lazily and kept, because the capture writes
 *  repeatedly. Resolves `null` for good once an open has failed. */
let dbHandle: Promise<IDBDatabase | null> | undefined;

function db(): Promise<IDBDatabase | null> {
  dbHandle ??= openDB();
  return dbHandle;
}

function openDB(): Promise<IDBDatabase | null> {
  // The capability off the object, not a `typeof` test: a runtime without
  // IndexedDB and one whose open throws (private browsing) are both this branch.
  const factory = (globalThis as { readonly indexedDB?: IDBFactory }).indexedDB;
  if (factory === undefined) {
    return Promise.resolve(null);
  }
  return new Promise((resolve) => {
    let req: IDBOpenDBRequest;
    try {
      req = factory.open(DB_NAME, DB_VERSION);
    } catch {
      resolve(null);
      return;
    }
    req.onupgradeneeded = () => {
      req.result.createObjectStore(STORE_NAME);
    };
    req.onsuccess = () => {
      resolve(req.result);
    };
    req.onerror = () => {
      resolve(null);
    };
    req.onblocked = () => {
      resolve(null);
    };
  });
}

async function readRecord(): Promise<unknown> {
  const conn = await db();
  if (conn === null) {
    return null;
  }
  return new Promise<unknown>((resolve) => {
    try {
      const req = conn.transaction(STORE_NAME, "readonly").objectStore(STORE_NAME).get(RECORD_KEY);
      req.onsuccess = () => {
        resolve(req.result as unknown);
      };
      req.onerror = () => {
        resolve(null);
      };
    } catch {
      resolve(null);
    }
  });
}

async function deleteRecord(): Promise<void> {
  const conn = await db();
  if (conn === null) {
    return;
  }
  return new Promise<void>((resolve) => {
    try {
      const tx = conn.transaction(STORE_NAME, "readwrite");
      tx.objectStore(STORE_NAME).delete(RECORD_KEY);
      tx.oncomplete = () => {
        resolve();
      };
      tx.onerror = () => {
        resolve();
      };
      tx.onabort = () => {
        resolve();
      };
    } catch {
      resolve();
    }
  });
}

async function writeRecord(snap: BootSnapshot): Promise<void> {
  const conn = await db();
  if (conn === null) {
    return;
  }
  return new Promise<void>((resolve) => {
    try {
      const tx = conn.transaction(STORE_NAME, "readwrite");
      // Structured-cloneable by construction: every field is JSON data off the wire.
      tx.objectStore(STORE_NAME).put(snap, RECORD_KEY);
      tx.oncomplete = () => {
        resolve();
      };
      tx.onerror = () => {
        resolve();
      };
      tx.onabort = () => {
        resolve();
      };
    } catch {
      resolve();
    }
  });
}

/** Forget the pending write, the capture effect and the connection — each is
 *  established once per page, so a test driving two boots needs them dropped.
 *
 *  The connection is DROPPED, not closed: a second one is harmless because the
 *  version never changes, and closing would make this reset async. */
export function _resetForTest(): void {
  clearTimeout(pending);
  pending = undefined;
  disposeCapture?.();
  disposeCapture = undefined;
  captureAbort?.abort();
  captureAbort = undefined;
  dbHandle = undefined;
}
