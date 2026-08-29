// ---------------------------------------------------------------------------
// A fake tab server, for every suite that drives the tab PROJECTION.
//
// The tab set is server-owned, so a test cannot put a row on the strip by
// calling a store mutator any more: it dispatches a mutation and the
// `tabs_changed` frame that follows is what paints. This module is the other end
// of that round trip — it holds the collection, mints the opaque ids, bumps the
// version and emits the frames, so a suite exercises the REAL `tabs.ts`,
// `tabs-sync.ts` and `actions/tabs.ts` rather than a stub of any of them.
//
// WHY A SHARED HELPER RATHER THAN ONE PER SUITE: five suites need it (the store,
// the dot, the attention fold, the editor's close, the projection's own cases),
// and the part they must agree on is the INTERLEAVING — whether the frame lands
// before or after the command's response resolves. A per-suite copy of that
// would be five chances to test one ordering and believe both were covered.
//
// `mode` is the whole of that:
//
//   - "event-first" (the default): the frame is emitted BEFORE the response
//     resolves, so the response's adoption finds the row already there and
//     upserts idempotently. The cheapest seeding path, and a real interleaving.
//   - "response-first": the response resolves first and the frame is emitted one
//     macrotask later, which is the COMMON case in production — the adoption
//     paints the row and the late frame must change nothing.
//   - "manual": nothing is emitted until a test calls `flushFrames()`. For the
//     cases whose subject IS the gap between the two.
// ---------------------------------------------------------------------------

import { vi } from "vitest";

import type { TabKind, TabList, TabSubject, TabsChangedPayload } from "../types.js";

/** The two entry points of the sync layer this harness drives.
 *
 *  Handed in by the test rather than imported here, and that is not a style
 *  choice: the `api-client.js` mock's factory imports THIS module, so a static
 *  `../tabs-sync.js` import would make resolving that mock require loading the
 *  module the mock replaces. The mocker deadlocks on it, with no error — the run
 *  simply never starts. */
export interface SyncSeam {
  ingest: (frame: TabsChangedPayload) => void;
  list: () => Promise<void>;
}

let sync: SyncSeam | null = null;

/** Wire the harness to the real sync layer. Called once per suite, at module
 *  scope, after the mocks are declared. */
export function bindTabsSync(seam: SyncSeam): void {
  sync = seam;
}

function ingest(frame: TabsChangedPayload): void {
  if (sync === null) {
    throw new Error("tabs-server: bindTabsSync was never called");
  }
  sync.ingest(frame);
}

export type Interleaving = "event-first" | "response-first" | "manual";

interface Sent {
  type: string;
  payload: Record<string, unknown>;
}

interface State {
  subjects: TabSubject[];
  version: number;
  nextID: number;
  mode: Interleaving;
  /** Frames the harness has committed but not yet handed to the sync layer. */
  pending: TabsChangedPayload[];
  /** Every command the projection dispatched, in order. */
  sent: Sent[];
  /** Command types the next dispatch of which fails, with the status to use. */
  failures: { type: string; status: number; error: string }[];
  /** Answers `GET /api/tabs` gives, queued. Falls through to the live set. */
  listAnswers: (TabList | null)[];
  listCalls: number;
  /** Releasers for responses being HELD, or null when responses resolve at once. */
  held: (() => void)[] | null;
}

const state: State = {
  subjects: [],
  version: 0,
  nextID: 0,
  mode: "event-first",
  pending: [],
  sent: [],
  failures: [],
  listAnswers: [],
  listCalls: 0,
  held: null,
};

// --- What a test reads and writes ---

export const tabServer = {
  /** Drop every trace of a previous case. Call FIRST in a beforeEach, before
   *  `_resetForTest()` re-registers the projection. */
  reset(mode: Interleaving = "event-first"): void {
    state.subjects = [];
    state.version = 0;
    state.nextID = 0;
    state.mode = mode;
    state.pending = [];
    state.sent = [];
    state.failures = [];
    state.listAnswers = [];
    state.listCalls = 0;
    state.held = null;
  },

  /** Switch the interleaving mid-case. */
  setMode(mode: Interleaving): void {
    state.mode = mode;
  },

  /** Hand every committed-but-unemitted frame to the sync layer, oldest first. */
  flushFrames(): void {
    const frames = state.pending;
    state.pending = [];
    for (const frame of frames) {
      ingest(frame);
    }
  },

  /** How many committed frames have not reached the sync layer yet. Zero after an
   *  event-first open means the row landed BEFORE the response resolved, which is
   *  the whole difference between the two interleavings. */
  pendingCount(): number {
    return state.pending.length;
  },

  /** Hold every response until `releaseResponses`, so two dispatches can be IN
   *  FLIGHT at once — the only window the framework's `dedupe` covers, since it
   *  evicts its slot in the result's `finally`. */
  holdResponses(): void {
    state.held = [];
  },

  /** Resolve every held response, oldest first. */
  releaseResponses(): void {
    const held = state.held ?? [];
    state.held = null;
    for (const release of held) {
      release();
    }
  },

  /** Feed ONE frame the harness never committed: another device's mutation, a
   *  frame at a version the collection never reached, an order that omits a tab.
   *  Carries no `op_id`, so the projection reads it as remote. */
  emitRaw(frame: TabsChangedPayload): void {
    ingest(frame);
  },

  /** The collection as the server holds it. */
  subjects(): readonly TabSubject[] {
    return state.subjects;
  },

  version(): number {
    return state.version;
  },

  /** The id the server minted for a subject, or "" — the harness's own view,
   *  independent of what the projection believes. */
  idFor(kind: TabKind, ref = ""): string {
    return state.subjects.find((s) => s.kind === kind && s.ref === ref)?.id ?? "";
  },

  /** Every command the projection dispatched. */
  sent(): readonly Sent[] {
    return state.sent;
  },

  sentOfType(type: string): readonly Sent[] {
    return state.sent.filter((c) => c.type === type);
  },

  /** Make the next dispatch of `type` fail. Consumed by that dispatch. */
  failNext(type: string, status = 500, error = "nope"): void {
    state.failures.push({ type, status, error });
  },

  /** Queue an answer for the next `GET /api/tabs`. `null` is the unreachable
   *  case. Unqueued reads answer with the live collection. */
  queueList(answer: TabList | null): void {
    state.listAnswers.push(answer);
  },

  listCalls(): number {
    return state.listCalls;
  },

  /** Put a subject in the collection WITHOUT a mutation, then adopt the whole
   *  set through a real `GET /api/tabs`. The boot read, and the shortest way to
   *  arrange a strip a case wants to start from. */
  async seed(...specs: readonly SeedSpec[]): Promise<void> {
    for (const spec of specs) {
      commitOpen(spec);
    }
    state.pending = [];
    if (sync === null) {
      throw new Error("tabs-server: bindTabsSync was never called");
    }
    await sync.list();
  },

  /** Open a tab the way ANOTHER DEVICE does, and DO NOT deliver its frame.
   *
   *  The collection moves ahead of the projection, which is the state a `reorder`
   *  refuses on: the arrangement a drag committed describes a set that no longer
   *  exists, so the exact-set check answers 409 and the caller re-lists. Call
   *  `flushFrames` to let the projection catch up the ordinary way instead. */
  openElsewhere(spec: SeedSpec): TabSubject {
    return commitOpen(spec);
  },

  /** Close a tab the way ANOTHER device does: commit it and emit a frame with no
   *  `op_id`, so the projection runs its local cleanup and dispatches nothing. */
  closeRemotely(id: string): void {
    const removed = descendants(id);
    if (removed.length === 0) {
      return;
    }
    state.subjects = state.subjects.filter((s) => !removed.includes(s.id));
    state.version++;
    ingest({ removed_ids: removed, version: state.version });
  },
};

export interface SeedSpec {
  kind: TabKind;
  ref?: string;
  parent?: string;
  owns?: boolean;
  pinned?: boolean;
}

// --- The collection's own rules, as the server states them ---

function mint(): string {
  state.nextID++;
  // Opaque on purpose: nothing about a tab's identity is recoverable from its
  // id, so a case that wants one has to read it back through `tabIdFor`.
  return `tb_${String(state.nextID).padStart(3, "0")}`;
}

function subjectFor(kind: TabKind, ref: string): TabSubject | undefined {
  return state.subjects.find((s) => s.kind === kind && s.ref === ref);
}

/** Append a subject at its canonical position: a child after its parent's
 *  existing children, a top-level tab at the end. Mirrors the store's Open. */
function commitOpen(spec: SeedSpec): TabSubject {
  const ref = spec.ref ?? "";
  const parent = spec.parent ?? "";
  const subject: TabSubject = {
    id: mint(),
    kind: spec.kind,
    ref,
    // A parent that is not open promotes the tab to top level, which is the
    // server's rule and the strip's.
    parent: parent !== "" && state.subjects.some((s) => s.id === parent) ? parent : "",
    pinned: spec.pinned ?? false,
    owns: spec.owns ?? true,
  };
  if (subject.parent === "") {
    state.subjects.push(subject);
  } else {
    let at = state.subjects.findIndex((s) => s.id === subject.parent) + 1;
    while (at < state.subjects.length && state.subjects[at]?.parent === subject.parent) {
      at++;
    }
    state.subjects.splice(at, 0, subject);
  }
  state.version++;
  state.pending.push({ changed: subject, version: state.version });
  return subject;
}

/** An id and every tab beneath it, deepest first, which is the order a close
 *  commits them in. */
function descendants(id: string): string[] {
  if (!state.subjects.some((s) => s.id === id)) {
    return [];
  }
  const out: string[] = [];
  const walk = (parent: string): void => {
    for (const child of state.subjects.filter((s) => s.parent === parent)) {
      walk(child.id);
    }
    out.push(parent);
  };
  walk(id);
  return out;
}

// --- The two module mocks ---

interface SendResultLike {
  ok: boolean;
  status: number;
  error?: string;
  body?: unknown;
}

/** Answer one command against the collection, and schedule its frame per the
 *  current interleaving. */
function handle(type: string, payload: Record<string, unknown>): SendResultLike {
  const failAt = state.failures.findIndex((f) => f.type === type);
  if (failAt >= 0) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
    const failure = state.failures[failAt]!;
    state.failures.splice(failAt, 1);
    return { ok: false, status: failure.status, error: failure.error };
  }
  switch (type) {
    case "open_tab": {
      const kind = payload["kind"] as TabKind;
      const ref = (payload["ref"] as string | undefined) ?? "";
      const existing = subjectFor(kind, ref);
      if (existing !== undefined) {
        // Commits nothing, so it emits NOTHING. `created: false` is the only
        // signal the caller gets, which is why it is load-bearing. The version
        // is the collection's CURRENT one, exactly as the real handler answers.
        return {
          ok: true,
          status: 200,
          body: { subject: existing, created: false, version: state.version },
        };
      }
      const subject = commitOpen({
        kind,
        ref,
        parent: (payload["parent"] as string | undefined) ?? "",
        owns: payload["owns"] === true,
      });
      stamp(payload);
      return {
        ok: true,
        status: 200,
        body: { subject, created: true, version: state.version },
      };
    }
    case "close_tab": {
      const removed = descendants(payload["id"] as string);
      if (removed.length === 0) {
        // Closing an id that is not open is not an error: two devices can close
        // one tab. The EMPTY list is the client's semantic confirmation of
        // absence, so the shape matters more than usual here.
        return { ok: true, status: 200, body: { closed: [], version: state.version } };
      }
      state.subjects = state.subjects.filter((s) => !removed.includes(s.id));
      state.version++;
      state.pending.push({ removed_ids: removed, version: state.version });
      stamp(payload);
      return { ok: true, status: 200, body: { closed: removed, version: state.version } };
    }
    case "pin_tab": {
      const id = payload["id"] as string;
      const pinned = payload["pinned"] === true;
      const at = state.subjects.findIndex((s) => s.id === id);
      if (at < 0) {
        // A pin is a statement ABOUT a tab, so naming one that is not open is a
        // mistake rather than a race.
        return { ok: false, status: 404, error: "no such tab" };
      }
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
      const before = state.subjects[at]!;
      if (before.pinned === pinned) {
        return { ok: true, status: 200, body: { version: state.version } };
      }
      const after: TabSubject = { ...before, pinned };
      state.subjects[at] = after;
      state.version++;
      state.pending.push({ changed: after, version: state.version });
      stamp(payload);
      return { ok: true, status: 200, body: { version: state.version } };
    }
    case "reorder_tabs": {
      const order = payload["order"] as string[];
      const held = state.subjects.map((s) => s.id);
      const exact =
        order.length === held.length &&
        new Set(order).size === order.length &&
        order.every((id) => held.includes(id));
      if (!exact) {
        // The exact-set check IS the whole precondition, so a mismatch is 409 and
        // means re-list, never re-send.
        return { ok: false, status: 409, error: "set moved" };
      }
      state.subjects = order.map(
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- exactness checked above
        (id) => state.subjects.find((s) => s.id === id)!,
      );
      state.version++;
      state.pending.push({ order: [...order], version: state.version });
      stamp(payload);
      return { ok: true, status: 200, body: { version: state.version } };
    }
    default:
      return { ok: true, status: 200, body: { ok: true } };
  }
}

/** Put the dispatching device's `op_id` on the frame this command committed, so
 *  the projection can tell its own echo from another device's. */
function stamp(payload: Record<string, unknown>): void {
  const opID = payload["op_id"];
  const frame = state.pending[state.pending.length - 1];
  if (frame !== undefined && typeof opID === "string") {
    frame.op_id = opID;
  }
}

/** The `transport.js` mock: `send` answers tab commands off the collection, and
 *  `newOpID` mints the correlation id the projection stamps its dispatches with.
 *
 *  Every other command answers a bare success rather than throwing, because a
 *  suite mocking transport for the tab set usually has one or two other
 *  commands in its graph and none of them is the subject. */
export function tabTransportMock(): {
  send: (cmd: { type: string; payload?: unknown }) => Promise<SendResultLike>;
  newOpID: () => string;
  newMessageID: () => string;
  newRequestID: () => string;
  markHydrated: () => void;
  init: () => void;
} {
  let ops = 0;
  return {
    send: vi.fn((cmd: { type: string; payload?: unknown }) => {
      const payload = (cmd.payload ?? {}) as Record<string, unknown>;
      state.sent.push({ type: cmd.type, payload });
      const before = state.pending.length;
      const result = handle(cmd.type, payload);
      const committed = state.pending.slice(before);
      const deliver = (): void => {
        state.pending = state.pending.filter((f) => !committed.includes(f));
        for (const frame of committed) {
          ingest(frame);
        }
      };
      if (committed.length > 0 && state.mode === "event-first") {
        // The frame lands BEFORE the response resolves, so the adoption finds
        // the row already there and upserts idempotently.
        deliver();
      } else if (committed.length > 0 && state.mode === "response-first") {
        // The response resolves first — the common production order — so the
        // ADOPTION is what paints, and the frame lands on a row that exists.
        setTimeout(deliver, 0);
      }
      const answer = answerWith(result);
      return answer;
    }),
    newOpID: vi.fn(() => {
      ops++;
      return `op-${String(ops)}`;
    }),
    newMessageID: vi.fn(() => "m-test"),
    newRequestID: vi.fn(() => "r-test"),
    markHydrated: vi.fn(),
    init: vi.fn(),
  };
}

/** The `api-client.js` mock's `apiGetTyped`: `GET /api/tabs` answers a queued
 *  list when a case arranged one (a stale snapshot, an unreachable endpoint),
 *  otherwise the live collection at its current version. */
export function tabListRead(): (path: string) => Promise<TabList | null> {
  return vi.fn((path: string) => {
    if (path !== "/api/tabs") {
      return Promise.resolve(null);
    }
    state.listCalls++;
    const queued = state.listAnswers.shift();
    if (queued !== undefined) {
      return Promise.resolve(queued);
    }
    return Promise.resolve({ tabs: state.subjects.map((s) => ({ ...s })), version: state.version });
  });
}

/** Resolve a response now, or park it until `releaseResponses` when a case is
 *  arranging two concurrent dispatches. */
function answerWith(result: SendResultLike): Promise<SendResultLike> {
  const held = state.held;
  if (held === null) {
    return Promise.resolve(result);
  }
  return new Promise<SendResultLike>((resolve) => {
    held.push(() => {
      resolve(result);
    });
  });
}

/** A subject built by hand, for the frames a test feeds through `emitRaw`. */
export function fakeSubject(id: string, over: Partial<TabSubject> = {}): TabSubject {
  return { id, kind: "chat", ref: id, parent: "", pinned: false, owns: true, ...over };
}

/** Let every microtask and the response-first `setTimeout(0)` run. */
export function settleTabs(): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
}
