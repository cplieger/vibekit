// ---------------------------------------------------------------------------
// The boot snapshot: a bounded projection of what THIS SCREEN was showing, held
// in IndexedDB so a resume paints before the network answers. A PAINT-TIME HINT
// with the standing the theme's localStorage cache has: it advances no tab-set
// version, and `boot.ts` paints it only while its own chat read has yet to ANSWER,
// so the server's answer always wins.
// ---------------------------------------------------------------------------

import { effect } from "@cplieger/reactive";
import {
  get,
  getActiveId,
  getSessions,
  messagesVersionOf,
  setSessions,
  upsertMessage,
  watchActiveId,
} from "./store.js";
import { openTabSubjects, paintProvisionalTabs, tabSetVersion } from "./tabs.js";
import { projectTurns } from "./turns.js";
import type { Message, Session, TabSubject, Usage } from "./types.js";
import { asObject, decodeArray, reqNum, reqStr } from "./validators.js";
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

/** What one screen was showing. No active tab: `device-view.ts` persists that per
 *  screen already, and `tabs.ts`'s reset reads it. */
export interface BootSnapshot {
  readonly tabs: readonly TabSubject[];
  readonly chats: readonly SnapshotChat[];
  /** The chat whose turns `messages` holds, empty when no chat was active. */
  readonly transcript_chat_id: string;
  readonly messages: readonly Message[];
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
  setSessions(snap.chats.map(toProvisionalSession));
  paintProvisionalTabs(snap.tabs);
  for (const m of snap.messages) {
    upsertMessage(snap.transcript_chat_id, m);
  }
  return true;
}

/** A chat row with no transcript claim, marked as the hint it is.
 *
 *  `residency` is left unset deliberately: `transcriptStale` then reads true, so the
 *  activation this paint enables refetches the window rather than trusting a hint —
 *  which is what makes the server's answer overwrite this and not the reverse.
 *  `provisional` covers the rows that answer does not name; its rule is at `types.ts`
 *  `Session.provisional`. */
function toProvisionalSession(c: SnapshotChat): Session {
  return {
    id: c.id,
    name: c.name,
    model: c.model,
    acp_session_id: "",
    current_mode_id: c.current_mode_id,
    usage: c.usage,
    messages: [],
    message_count: c.message_count,
    has_more: c.message_count > 0,
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
    transcript_chat_id: active,
    messages: newestTurns(active),
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

/** The active chat's newest turns, flattened back to messages and capped.
 *
 *  `projectTurns` is the app's own segmentation, so this cuts on the boundary the
 *  transcript renders and there is no second turn rule to drift from it. BOTH bounds
 *  cut there: walking newest-first is what makes the message cap drop whole turns
 *  instead of slicing the flattened tail, which would keep a body whose trigger is
 *  gone — the headerless card SNAPSHOT_TURNS exists to prevent. */
function newestTurns(chatID: string): Message[] {
  if (chatID === "") {
    return [];
  }
  const messages = get(chatID)?.messages ?? [];
  const turns = projectTurns(messages, false).slice(-SNAPSHOT_TURNS);
  const out: Message[] = [];
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i];
    if (t === undefined) {
      continue;
    }
    const trigger = t.trigger;
    const flat = trigger === undefined ? t.body : [trigger, ...t.body];
    if (out.length + flat.length <= SNAPSHOT_MAX_MESSAGES) {
      out.unshift(...flat);
      continue;
    }
    if (out.length === 0) {
      // The newest turn alone over budget is the case the cap exists for, and dropping
      // it would resume with no transcript. Trimmed from its OLD end, trigger first.
      const head = trigger === undefined ? [] : [trigger];
      out.push(...head, ...t.body.slice(-(SNAPSHOT_MAX_MESSAGES - head.length)));
    }
    break;
  }
  return out;
}

/** Narrow a persisted record: `unknown` in, every ELEMENT validated through the
 *  generated wire decoders the network path already runs. A rejection is the whole
 *  record, because a half-valid hint is not a hint. */
function decodeSnapshot(v: unknown): BootSnapshot | null {
  try {
    const o = asObject(v, "$.boot_snapshot");
    return {
      tabs: decodeArray(o["tabs"], decodeTabSubject, "$.boot_snapshot.tabs"),
      chats: decodeArray(o["chats"], decodeSnapshotChat, "$.boot_snapshot.chats"),
      transcript_chat_id: reqStr(o, "transcript_chat_id", "$.boot_snapshot"),
      messages: decodeArray(o["messages"], decodeMessage, "$.boot_snapshot.messages"),
    };
  } catch {
    return null;
  }
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
