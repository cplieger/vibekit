// ---------------------------------------------------------------------------
// The four tab mutations. Every change to the open-tab set goes through one
// of these; nothing else may send a tab command.
//
// A response is what the server committed (a subject, a `closed` list, a
// version), reconciled by the pending-op machine against the `tabs_changed`
// frame that follows, idempotent by id.
//
// The version in every reply is for the machine, never the watermark: only
// an EVENT advances the watermark (tabs-sync.ts). A response-adopted v+2
// would make another device's in-flight v+1 read as stale, destroying the
// gap check.
//
// `dedupe` is a per-command decision. `open_tab` dedupes on (kind, ref) via
// a key function — the default key would include the unique `op_id` and
// collapse nothing. `close_tab` dedupes on the id, same reasoning. `pin_tab`
// and `reorder_tabs` dedupe on nothing: pin -> unpin -> pin must end pinned,
// and A -> B -> A must end at A, so collapsing the repeat would silently
// leave the collection one step behind. None of these four carries an
// argument-composite idempotency key for the same reason — inside the TTL a
// repeated mutation would replay a cached success and never run
// (`files.rename` shipped this once).
//
// `op_id` is a dispatch ARGUMENT, never minted inside `run()`: the framework
// re-invokes `run()` per retry and hoists only the idempotency key, so an
// op minted there would be fresh on every attempt and correlate nothing.
// ---------------------------------------------------------------------------

import { join as joinKey } from "@cplieger/keyenc";

import { defineAction, retryNetwork, RETRY_STANDARD, IDEMPOTENCY_COMMAND_FIELD } from "./index.js";
import { send as transportSend, type SendResult } from "../transport.js";
import type { TabKind, TabSubject } from "../types.js";
import { decodeTabSubject } from "../wire/decoders.gen.js";

/** What `open_tab` needs about the thing being opened. `ref` is empty for a
 *  singleton, whose identity is its kind. */
export interface OpenTabArgs {
  kind: TabKind;
  ref: string;
  /** An already-open tab to nest under. Empty for top level. A parent that
   *  is not open promotes the new tab to top level rather than refusing it. */
  parent: string;
  /** Whether closing this tab tears down what it shows. */
  owns: boolean;
  opID: string;
}

/** What the server committed for an open. `created: false` is load-bearing:
 *  an already-open (kind, ref) commits nothing, bumps no version, and emits
 *  no event, so the pending-op machine retires such an op on the spot. */
export interface OpenTabReply {
  subject: TabSubject;
  created: boolean;
  version: number;
}

export const openTabCommand = defineAction<OpenTabArgs, OpenTabReply | null>({
  name: "tabs.open",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (args) => joinKey("tabs.open", args.kind, args.ref),
  run: async ({ kind, ref, parent, owns, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "open_tab",
        payload: {
          kind,
          op_id: opID,
          ...(ref === "" ? {} : { ref }),
          ...(parent === "" ? {} : { parent }),
          ...(owns ? { owns: true } : {}),
        },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw sendFailure(r, "open that tab");
    }
    const body = asObject(r.body);
    if (body === null || !("subject" in body)) {
      throw sendFailure(r, "open that tab");
    }
    return {
      subject: decodeTabSubject(body["subject"]),
      // Absent treated as not created — the conservative reading.
      created: body["created"] === true,
      version: numberField(body, "version"),
    };
  },
  // At the product limit the refusal has a remedy.
  error: (_args, err) =>
    err.status === 409
      ? "Close a tab first — this workspace has too many open."
      : "Couldn't open that tab",
});

export interface CloseTabArgs {
  id: string;
  opID: string;
}

/** How long a close dispatch may stay unanswered before the pending-op
 *  machine VERIFIES instead: the removal stays applied, nothing restores,
 *  and a re-list settles it on authoritative evidence. */
export const CLOSE_CONFIRM_MS = 5000;

/** What the server committed for a close. `closed` is a list (a parent and
 *  its children close as one mutation), and empty is a normal answer —
 *  two devices can close one tab. */
export interface CloseTabReply {
  closed: string[];
  version: number;
}

export const closeTabCommand = defineAction<CloseTabArgs, CloseTabReply | null>({
  name: "tabs.close",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  timeout: CLOSE_CONFIRM_MS,
  dedupe: (args) => joinKey("tabs.close", args.id),
  run: async ({ id, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "close_tab",
        payload: { id, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw sendFailure(r, "close that tab");
    }
    const body = asObject(r.body);
    const closed = body?.["closed"];
    return {
      closed: Array.isArray(closed) ? closed.filter((v): v is string => typeof v === "string") : [],
      version: body === null ? 0 : numberField(body, "version"),
    };
  },
  // No framework toast: a TIMEOUT is inconclusive (the close may have
  // committed, so the machine verifies), while a DEFINITIVE refusal is the
  // close gesture's own to report alongside its rollback.
  error: false,
});

export interface ReorderTabsArgs {
  order: readonly string[];
  opID: string;
}

/** The exact-set refusal: a 409 means the set moved under the drag, so the
 *  gesture's arrangement describes a collection that no longer exists —
 *  re-list, never re-send. */
export const REORDER_STALE = "stale" as const;

export const reorderTabsCommand = defineAction<ReorderTabsArgs, "ok" | typeof REORDER_STALE | null>(
  {
    name: "tabs.reorder",
    networkMode: "always",
    idempotencyKey: true,
    retryable: retryNetwork,
    retry: RETRY_STANDARD,
    run: async ({ order, opID }, signal, ctx) => {
      const r = await transportSend(
        {
          type: "reorder_tabs",
          payload: { order: [...order], op_id: opID },
          ...(ctx?.idempotencyKey === undefined
            ? {}
            : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
        },
        { signal, reportSendState: false },
      );
      if (r.status === 409) {
        // Not an error to the reader: the strip reflects a set this device
        // had not caught up with. Caller re-lists and the drag snaps back.
        return REORDER_STALE;
      }
      if (!r.ok) {
        throw sendFailure(r, "reorder the tabs");
      }
      return "ok";
    },
    error: "Couldn't reorder the tabs",
  },
);

export interface PinTabArgs {
  id: string;
  pinned: boolean;
  opID: string;
}

export const pinTabCommand = defineAction<PinTabArgs, boolean>({
  name: "tabs.pin",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ id, pinned, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "pin_tab",
        payload: { id, pinned, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      // 404 rather than the empty answer a close gets: a pin is a statement
      // ABOUT a tab, so naming one that is not open is a mistake, not a race.
      throw sendFailure(r, "pin that tab");
    }
    return true;
  },
  error: "Couldn't pin that tab",
});

// --- Shared reply handling ---

function asObject(body: unknown): Record<string, unknown> | null {
  return typeof body === "object" && body !== null ? (body as Record<string, unknown>) : null;
}

/** The committed version out of a reply body, 0 when absent or malformed —
 *  below every real version, so the machine treats it as already covered. */
function numberField(body: Record<string, unknown>, key: string): number {
  const v = body[key];
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

/** Turns a transport failure into the shape the framework's error surface
 *  reads. `ActionError` is not imported: the framework normalizes a thrown
 *  Error and reads `status` off it when present. */
function sendFailure(r: SendResult, what: string): Error & { status?: number } {
  const err: Error & { status?: number } = new Error(r.error ?? `Couldn't ${what}`);
  err.status = r.status;
  return err;
}
