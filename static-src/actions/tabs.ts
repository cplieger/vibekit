// ---------------------------------------------------------------------------
// The four tab mutations. Every change to the open-tab set goes through one of
// these; nothing else may send a tab command.
//
// A RESPONSE IS NOT A RENDER. Each of these returns what the server COMMITTED —
// an id, a `created` flag, a version — and the strip is painted by the
// `tabs_changed` frame that follows. That split is the whole design: the frame is
// idempotent by id, so the device that asked and the device that did not take the
// same path, and no call site can paint a row the server has not persisted.
//
// THE VERSION IN EVERY REPLY IS DIAGNOSTIC AND MUST NOT BE ADOPTED. Only an event
// advances the local watermark (tabs-sync.ts, mechanism 1). A response-adopted
// v+2 makes another device's in-flight v+1 read as stale, which destroys the gap
// check permanently — so these replies deliberately do not carry the version out
// of this module.
//
// WHERE `dedupe` IS AND IS NOT USED, which is a per-command decision rather than
// a house style:
//
//   - `open_tab` DEDUPES on (kind, ref), with a key FUNCTION. Two taps on one
//     door are one open. The default key is `safeStringify(args)`, which would
//     include the unique `op_id` and therefore collapse nothing at all, so the
//     function is not a refinement — it is the difference between the option
//     working and being decorative. `dedupe` collapses only IN-FLIGHT dispatches
//     (the framework evicts the slot in `result.finally`); outside that window the
//     server's own (kind, ref) uniqueness answers a late second tap by returning
//     the tab already open, which is why nothing further is needed.
//   - `close_tab` DEDUPES on the id, for the same reason and with the same
//     safety: closing an id that is not open is not an error, so a collapse and a
//     second round trip have the same outcome.
//   - `pin_tab` and `reorder_tabs` DEDUPE ON NOTHING, deliberately. A repeat pin
//     and a drag back to where it started are real gestures that must reach the
//     server: pin → unpin → pin has to end pinned, and A → B → A has to end at A.
//     Any key that collapsed the third gesture onto the first would leave the
//     collection at the second, silently. This is also why none of these four
//     carries an argument-composite idempotency key — inside the 5-minute cache a
//     repeated mutation would replay a cached success and never run, which is the
//     same defect from the other side and one this fleet has already shipped once
//     (`files.rename`).
//
// `op_id` IS A DISPATCH ARGUMENT, never minted inside `run()`. The framework
// re-invokes `run()` per retry attempt and hoists only the idempotency key, so an
// op minted there would be fresh on every attempt and correlate nothing — and
// correlation is what tells this device's own echo from another device's, which is
// what stops a teardown running twice.
// ---------------------------------------------------------------------------

import { join as joinKey } from "@cplieger/keyenc";

import { defineAction, retryNetwork, RETRY_STANDARD, IDEMPOTENCY_COMMAND_FIELD } from "./index.js";
import { send as transportSend, type SendResult } from "../transport.js";
import type { TabKind, TabSubject } from "../types.js";
import { decodeTabSubject } from "../wire/decoders.gen.js";

/** What `open_tab` needs about the thing being opened. `ref` is empty for a
 *  singleton, which is the one kind whose identity is its kind. */
export interface OpenTabArgs {
  kind: TabKind;
  ref: string;
  /** An already-open tab to nest under. Empty for top level. A parent that is not
   *  open promotes the new tab to top level rather than refusing it — the server's
   *  rule, matching what the strip does with an orphan. */
  parent: string;
  /** Whether closing this tab tears down what it shows. */
  owns: boolean;
  opID: string;
}

/** What the server committed for an open.
 *
 *  `created: false` is load-bearing, not informational: an already-open
 *  (kind, ref) commits nothing, so it bumps no version and emits NO event. A
 *  caller that waited only for the frame would wait forever, which is exactly the
 *  silent no-op this design exists to remove. */
export interface OpenTabReply {
  subject: TabSubject;
  created: boolean;
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
      // Absent is treated as NOT created, which is the conservative reading: it
      // makes the caller wait for a frame rather than assume the row is already
      // there, and the wait is bounded.
      created: body["created"] === true,
    };
  },
  // At the product limit the refusal has a remedy, and saying so is the whole
  // difference between a control that looks broken and one that is bounded.
  error: (_args, err) =>
    err.status === 409
      ? "Close a tab first — this workspace has too many open."
      : "Couldn't open that tab",
});

export interface CloseTabArgs {
  id: string;
  opID: string;
}

export const closeTabCommand = defineAction<CloseTabArgs, string[] | null>({
  name: "tabs.close",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
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
    // A LIST, because a parent and its children go as one mutation. Empty is a
    // normal answer rather than a failure: two devices can close one tab.
    const body = asObject(r.body);
    const closed = body?.["closed"];
    return Array.isArray(closed) ? closed.filter((v): v is string => typeof v === "string") : [];
  },
  error: "Couldn't close that tab",
});

export interface ReorderTabsArgs {
  order: readonly string[];
  opID: string;
}

/** The exact-set refusal. A 409 means the set moved under the drag, so the
 *  arrangement the gesture committed describes a collection that no longer exists
 *  — re-list, never re-send. Distinguished here rather than at the call site so
 *  there is one reading of the status. */
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
        // NOT an error to the reader: nothing is broken and nothing is lost, the
        // strip simply reflects a set this device had not caught up with. The
        // caller re-lists and the drag snaps back, which is the honest surface.
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
      // A 404 here rather than the empty answer a close gets, and the asymmetry is
      // the server's: a pin is a statement ABOUT a tab, so naming one that is not
      // open is a mistake rather than a race.
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

/** Turn a transport failure into the shape the framework's error surface reads.
 *
 *  `ActionError` is not imported: the framework normalizes a thrown Error and
 *  reads `status` off it when present, and hand-building one here would put a
 *  second error vocabulary beside the one every other action uses. */
function sendFailure(r: SendResult, what: string): Error & { status?: number } {
  const err: Error & { status?: number } = new Error(r.error ?? `Couldn't ${what}`);
  err.status = r.status;
  return err;
}
