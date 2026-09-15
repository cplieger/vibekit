// ---------------------------------------------------------------------------
// The SSE adapter: vibekit's half of the wake path. The stream, the cursor, the
// hold-and-drain and the single-flight rule are the library's and are tested there;
// what THIS file owns is the seam — which frames reach the bus and when their
// stamps are observed, what the `revalidate` body does with a digest answer, and
// which loaders it drives with the run's signal.
//
// The loaders are mocked at the module boundary: each is a network read whose own
// observe-on-commit is store-load.test.ts's (and its siblings') subject, so the
// contract pinned here is the call and the signal it carries.
// ---------------------------------------------------------------------------

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RevalidateContext, TabRevalidateContext, WorkerHost } from "@cplieger/sse";

import {
  BUS_PAGE_RESUMED,
  BUS_RECONCILE,
  onBus,
  registerSSEDecoder,
  type SSEPayloads,
} from "./bus.js";
import {
  adoptPushSubscription,
  init,
  markHydrated,
  presentedTag,
  _resetForTest,
  _revalidateForTest,
  _spawnWorkerForTest,
  _tabRevalidateForTest,
} from "./sse-adapter.js";
import { PROFILE_TAG_KEY } from "./sse-tag.js";
import { createSSEHost } from "./sse-worker-host.js";
import { setSessions } from "./store.js";
import {
  hasSubject,
  observeStamp,
  versionMap,
  _resetForTest as resetVersions,
} from "./subject-versions.js";
import type { ServerEvent, Session } from "./types.js";
import {
  EPOCH_A,
  EPOCH_B,
  createScriptedFetch,
  json,
  type ScriptedFetch,
  until,
} from "./__test-helpers__/sse-fetch.js";

const {
  mockLoadList,
  mockLoadMessages,
  mockListTabs,
  mockRebuildLiveRuns,
  mockInvalidate,
  mockFetchCatalog,
} = vi.hoisted(() => ({
  mockLoadList: vi.fn((_signal?: AbortSignal) => Promise.resolve(true)),
  mockLoadMessages: vi.fn((_id: string, _before?: string, _signal?: AbortSignal) =>
    Promise.resolve(true),
  ),
  mockListTabs: vi.fn((_signal?: AbortSignal) => Promise.resolve(true)),
  mockRebuildLiveRuns: vi.fn((_cause?: string, _signal?: AbortSignal) => Promise.resolve()),
  mockInvalidate: vi.fn(),
  mockFetchCatalog: vi.fn((_opts?: { signal?: AbortSignal }) => Promise.resolve()),
}));
vi.mock("./store-load.js", () => ({ loadList: mockLoadList, loadMessages: mockLoadMessages }));
vi.mock("./tabs-sync.js", () => ({ listTabs: mockListTabs }));
vi.mock("./run-store.js", () => ({
  rebuildLiveRuns: mockRebuildLiveRuns,
  invalidateCachedRuns: mockInvalidate,
}));
vi.mock("./session-catalog.js", () => ({ fetchCatalog: mockFetchCatalog }));
vi.mock("./send-state.js", () => ({ setSSEStatus: vi.fn() }));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...over,
  };
}

function ctx(over: Partial<RevalidateContext> = {}): RevalidateContext {
  return {
    cause: "hello",
    epoch: versionMap().epoch(),
    generation: 1,
    full: false,
    signal: new AbortController().signal,
    ...over,
  };
}

/** The context a host-routed run hands this tab: the same fields plus the host's verdict. */
function tabCtx(over: Partial<TabRevalidateContext> = {}): TabRevalidateContext {
  return { ...ctx(), ...over };
}

/** A digest answer for the subjects the request named, `changed` and `removed` by key. */
function digestAnswer(
  changed: { kind: string; ref: string; version: string }[],
  removed: { kind: string; ref: string; reason: "gone" | "forbidden" }[] = [],
) {
  return (req: { body: string | null }) => {
    const body = JSON.parse(req.body ?? "{}") as { subjects: unknown[] };
    return json({
      epoch: EPOCH_A,
      head: "9",
      must_refetch: false,
      checked: body.subjects.length,
      changed,
      removed,
    });
  };
}

let scripted: ScriptedFetch;
let seen: ServerEvent[];
/** The in-page worker host a hosted case attached this tab to, closed after it. */
let hosted: WorkerHost | null = null;

beforeEach(() => {
  scripted = createScriptedFetch();
  vi.stubGlobal("fetch", scripted.fetch);
  seen = [];
  setSessions([]);
  resetVersions();
  // Bound to the epoch the scripted hello presents, so a stamp a case observes before
  // connecting survives the bind; the one case about the bind itself rebinds first.
  versionMap().bind(EPOCH_A);
  localStorage.removeItem(PROFILE_TAG_KEY);
  mockLoadList.mockResolvedValue(true);
  mockLoadMessages.mockResolvedValue(true);
  mockListTabs.mockResolvedValue(true);
  mockRebuildLiveRuns.mockResolvedValue(undefined);
  mockFetchCatalog.mockResolvedValue(undefined);
});

afterEach(() => {
  _resetForTest();
  hosted?.close();
  hosted = null;
});

/** Boot the adapter against a scripted stream and answer its first hello. */
async function connect(): Promise<ReturnType<ScriptedFetch["connections"]["at"]>> {
  init(
    (evt) => {
      seen.push(evt);
    },
    () => undefined,
  );
  await until(() => scripted.connections.length === 1);
  const conn = scripted.connections[0];
  conn?.hello();
  return conn;
}

function syncs(): number {
  return scripted.requests.filter((r) => r.url === "/api/sync").length;
}

function acks(): ScriptedFetch["requests"] {
  return scripted.requests.filter((r) => r.url === "/api/events/alive");
}

/** Connect and let the fresh hello's own digest settle on an empty answer, so a case
 *  driving `revalidate` by hand is the only revalidation in flight. */
async function connectSettled(): Promise<ReturnType<ScriptedFetch["connections"]["at"]>> {
  scripted.respond("/api/sync", digestAnswer([]));
  const conn = await connect();
  markHydrated();
  await until(() => syncs() === 1);
  return conn;
}

describe("the connect", () => {
  it("presents the persisted profile tag as SSE-Client, minting one when none is held", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    const conn = await connect();
    expect(conn?.headers.get("SSE-Client")).toBe("held-tag_0123456789AB");
    _resetForTest();

    localStorage.removeItem(PROFILE_TAG_KEY);
    scripted = createScriptedFetch();
    vi.stubGlobal("fetch", scripted.fetch);
    const fresh = await connect();
    const minted = fresh?.headers.get("SSE-Client") ?? "";
    expect(minted).toMatch(/^[A-Za-z0-9_-]{22}$/);
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe(minted);
  });

  it("adopts the subscription's derived tag with ONE reconnect, and none when it already matches", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    await connect();
    expect(presentedTag()).toBe("held-tag_0123456789AB");

    // The golden endpoint from internal/push/testdata/tag_golden.json.
    const endpoint =
      "https://fcm.googleapis.com/fcm/send/dQw4w9WgXcQ:APA91bGolden-Fixture-Endpoint-0001";
    await adoptPushSubscription({ endpoint });
    await until(() => scripted.connections.length === 2);
    expect(scripted.connections[1]?.headers.get("SSE-Client")).toBe("amxAEqwvwjG23476CxNmK6");
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe("amxAEqwvwjG23476CxNmK6");

    // The worker reports the same subscription again (a resubscribe that kept the
    // endpoint): nothing moves.
    await adoptPushSubscription({ endpoint });
    await new Promise((r) => setTimeout(r, 20));
    expect(scripted.connections).toHaveLength(2);
    expect(presentedTag()).toBe("amxAEqwvwjG23476CxNmK6");
  });

  it("acknowledges each keepalive with a POST carrying SSE-Client, and stops when the stream ends", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    scripted.respond("/api/events/alive", () => new Response(null, { status: 204 }));
    const conn = await connect();
    conn?.named("heartbeat", "{}");
    await until(() => acks().length === 1);
    const ack = acks()[0];
    expect(ack?.method).toBe("POST");
    expect(ack?.headers.get("SSE-Client")).toBe("held-tag_0123456789AB");
    conn?.named("heartbeat", "{}");
    await until(() => acks().length === 2);

    _resetForTest();
    conn?.named("heartbeat", "{}");
    await new Promise((r) => setTimeout(r, 20));
    expect(acks()).toHaveLength(2);
  });

  it("binds the version map to the hello's epoch, dropping what a previous epoch held", async () => {
    versionMap().bind(EPOCH_B);
    observeStamp({ kind: "chats", ref: "", version: "9" });
    await connect();
    await until(() => versionMap().epoch() === EPOCH_A);
    expect(versionMap().snapshot().held).toEqual([]);
  });
});

describe("frames", () => {
  it("dispatches an applied frame and THEN observes its stamp", async () => {
    setSessions([makeSession("c1", { residency: "loaded" })]);
    let heldAtDispatch: boolean | undefined;
    init(
      (evt) => {
        heldAtDispatch = hasSubject("chat", "c1");
        seen.push(evt);
      },
      () => undefined,
    );
    await until(() => scripted.connections.length === 1);
    const conn = scripted.connections[0];
    conn?.hello();
    markHydrated();
    conn?.frame(
      {
        type: "message_appended",
        chat_id: "c1",
        payload: { id: "m1", role: "user", ts: 1, content: "hi" },
        subject: { kind: "chat", ref: "c1", version: "4" },
      },
      `${EPOCH_A}:1`,
    );
    await until(() => seen.length === 1);
    expect(seen[0]?.type).toBe("message_appended");
    // Not yet held while the handlers ran: the version certifies APPLIED state.
    expect(heldAtDispatch).toBe(false);
    expect(versionMap().snapshot().held).toEqual([{ kind: "chat", ref: "c1", version: "4" }]);
  });

  it("observes a workspace-wide stamp whatever the store holds", async () => {
    const conn = await connect();
    markHydrated();
    conn?.frame({
      type: "chat_updated",
      chat_id: "c9",
      payload: { id: "c9", name: "n", message_count: 0, updated_at: 1 },
      subject: { kind: "chats", ref: "", version: "12" },
    });
    await until(() => seen.length === 1);
    expect(hasSubject("chats", "")).toBe(true);
  });

  it("does not observe a chat stamp for a chat whose window is not resident", async () => {
    // The frame was applied to nothing, so recording its version would make the digest
    // name — and the wake refetch — a transcript nobody holds.
    setSessions([makeSession("c1", { residency: "evicted" })]);
    const conn = await connect();
    markHydrated();
    conn?.frame({
      type: "message_appended",
      chat_id: "c1",
      payload: { id: "m1", role: "user", ts: 1, content: "hi" },
      subject: { kind: "chat", ref: "c1", version: "4" },
    });
    await until(() => seen.length === 1);
    expect(hasSubject("chat", "c1")).toBe(false);
  });

  it("observes nothing when the decoder rejects the frame, and asks the digest instead", async () => {
    registerSSEDecoder("working_label", () => {
      throw new TypeError("refused");
    });
    // Something held, so the rejection's `revalidate("hello")` has a question to ask.
    observeStamp({ kind: "chats", ref: "", version: "1" });
    scripted.respond("/api/sync", digestAnswer([]));
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const conn = await connect();
    markHydrated();
    // The connect's own fresh-hello digest lands first; the rejection earns a second.
    await until(() => scripted.requests.filter((r) => r.url === "/api/sync").length === 1);
    conn?.frame({
      type: "working_label",
      chat_id: "c1",
      payload: { label: 1 },
      subject: { kind: "chats", ref: "", version: "2" },
    });
    await until(() => scripted.requests.filter((r) => r.url === "/api/sync").length === 2);
    expect(seen).toEqual([]);
    expect(versionMap().snapshot().held).toEqual([{ kind: "chats", ref: "", version: "1" }]);
    expect(errors).toHaveBeenCalled();
    registerSSEDecoder("working_label", (v) => v as SSEPayloads["working_label"]);
  });

  it("holds frames until markHydrated and releases them in arrival order", async () => {
    const conn = await connect();
    conn?.frame({ type: "connected", payload: { busy_stated: false, live_runs_stated: false } });
    conn?.frame({ type: "permission_needed", chat_id: "c1", payload: { request_id: 1 } });
    // Let the bytes cross the stream: nothing may reach the bus yet.
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 20);
    });
    expect(seen).toEqual([]);
    markHydrated();
    expect(seen.map((e) => e.type)).toEqual(["connected", "permission_needed"]);
  });

  it("runs the subject's refetch on subject_changed and observes nothing", async () => {
    await connect();
    markHydrated();
    scripted.connections[0]?.frame({
      type: "subject_changed",
      chat_id: "c1",
      subject: { kind: "chat", ref: "c1", version: "6" },
    });
    await until(() => mockLoadMessages.mock.calls.length === 1);
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, undefined);
    expect(seen).toEqual([]);
    expect(hasSubject("chat", "c1")).toBe(false);
  });

  it("refetches a loaded, fresh, EMPTY window on subject_changed, and keeps its old version until the page lands", async () => {
    // The window a `session/load` swap has to reach: `loaded`, holding a version, and
    // EMPTY — what a chat whose first newest-page load answered zero messages holds.
    // Nothing else heals it: the header frame ahead of the swap writes neither the
    // residency nor the version, and the activation gate reads the window as fresh.
    setSessions([makeSession("c1", { message_count: 2, messages: [], residency: "loaded" })]);
    observeStamp({ kind: "chat", ref: "c1", version: "5" });
    // Something is held, so the fresh hello digests; let that settle first, or the
    // frame below sits behind a revalidation nobody answers.
    const conn = await connectSettled();
    conn?.frame({
      type: "subject_changed",
      chat_id: "c1",
      subject: { kind: "chat", ref: "c1", version: "6" },
    });
    await until(() => mockLoadMessages.mock.calls.length === 1);
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, undefined);
    // The refetch's own commit is what moves the version; the frame's stamp is not it.
    expect(versionMap().snapshot().held).toEqual([{ kind: "chat", ref: "c1", version: "5" }]);
  });
});

describe("revalidate", () => {
  it("emits BUS_PAGE_RESUMED first on a visible cause, then one digest", async () => {
    observeStamp({ kind: "chats", ref: "", version: "1" });
    const order: string[] = [];
    const unsub = onBus(BUS_PAGE_RESUMED, () => {
      order.push("resumed");
    });
    scripted.respond("/api/sync", (req) => {
      order.push("digest");
      return digestAnswer([])(req);
    });
    try {
      await _revalidateForTest(ctx({ cause: "visible" }));
    } finally {
      unsub();
    }
    expect(order).toEqual(["resumed", "digest"]);
    expect(scripted.requests.filter((r) => r.url === "/api/sync")).toHaveLength(1);
  });

  it("asks nothing when the map holds nothing", async () => {
    await _revalidateForTest(ctx());
    expect(scripted.requests).toEqual([]);
  });

  it("refetches exactly the chat the digest named, with the run's signal", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "chat", ref: "Y", version: "3" });
    scripted.respond("/api/sync", digestAnswer([{ kind: "chat", ref: "X", version: "4" }]));
    const controller = new AbortController();

    await _revalidateForTest(ctx({ signal: controller.signal }));

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, controller.signal);
    // Never copied from the answer: the loader observes its own response stamp.
    expect(
      versionMap()
        .snapshot()
        .held.find((h) => h.ref === "X")?.version,
    ).toBe("3");
  });

  it("leaves the map at the old version when the GET fails", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    scripted.respond("/api/sync", digestAnswer([{ kind: "chat", ref: "X", version: "4" }]));
    mockLoadMessages.mockResolvedValue(false);

    await _revalidateForTest(ctx());

    expect(versionMap().snapshot().held).toEqual([{ kind: "chat", ref: "X", version: "3" }]);
  });

  it("sends the digest the held snapshot and its epoch", async () => {
    versionMap().bind(EPOCH_A);
    observeStamp({ kind: "chats", ref: "", version: "2" });
    scripted.respond("/api/sync", digestAnswer([]));

    await _revalidateForTest(ctx());

    const body = JSON.parse(scripted.requests[0]?.body ?? "{}") as Record<string, unknown>;
    expect(body).toEqual({ epoch: EPOCH_A, subjects: [{ kind: "chats", ref: "", version: "2" }] });
  });

  it("runs the action column: chats, tabs, runs and catalog each reach their loader", async () => {
    observeStamp({ kind: "chats", ref: "", version: "1" });
    observeStamp({ kind: "tabs", ref: "", version: "1" });
    observeStamp({ kind: "runs", ref: "", version: "1" });
    observeStamp({ kind: "catalog", ref: "", version: "1" });
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chats", ref: "", version: "2" },
        { kind: "tabs", ref: "", version: "2" },
        { kind: "runs", ref: "", version: "2" },
        { kind: "catalog", ref: "", version: "2" },
      ]),
    );
    const controller = new AbortController();

    await _revalidateForTest(ctx({ signal: controller.signal }));

    expect(mockLoadList).toHaveBeenCalledWith(controller.signal);
    expect(mockListTabs).toHaveBeenCalledWith(controller.signal);
    expect(mockRebuildLiveRuns).toHaveBeenCalledWith(expect.any(String), controller.signal);
    expect(mockInvalidate).toHaveBeenCalledWith(mockRebuildLiveRuns.mock.calls[0]?.[0]);
    expect(mockFetchCatalog).toHaveBeenCalledWith({ signal: controller.signal });
  });

  it("issues one GET for a chat named by both its chat and live_turn subjects", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "live_turn", ref: "X", version: "7:2" });
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chat", ref: "X", version: "4" },
        { kind: "live_turn", ref: "X", version: "7:3" },
      ]),
    );

    await _revalidateForTest(ctx());

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
  });

  it("forgets a removed chat and runs its local drop through the frame door", async () => {
    observeStamp({ kind: "chat", ref: "gone", version: "3" });
    await connectSettled();
    scripted.respond(
      "/api/sync",
      digestAnswer([], [{ kind: "chat", ref: "gone", reason: "gone" }]),
    );

    await _revalidateForTest(ctx());

    expect(hasSubject("chat", "gone")).toBe(false);
    expect(seen).toEqual([{ type: "chat_deleted", chat_id: "", payload: { id: "gone" } }]);
    expect(mockLoadMessages).not.toHaveBeenCalled();
  });

  it("refetches a chat whose live turn is gone, so the GET settles its live markers", async () => {
    observeStamp({ kind: "live_turn", ref: "X", version: "7:2" });
    scripted.respond(
      "/api/sync",
      digestAnswer([], [{ kind: "live_turn", ref: "X", reason: "gone" }]),
    );

    await _revalidateForTest(ctx());

    expect(hasSubject("live_turn", "X")).toBe(false);
    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, expect.any(AbortSignal));
  });

  it("completes every GET, then reconnects once when pending moved", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "pending", ref: "", version: "1" });
    const first = await connectSettled();
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chat", ref: "X", version: "4" },
        { kind: "pending", ref: "", version: "2" },
      ]),
    );
    let getSettled = false;
    let connectionsAtGet = 0;
    mockLoadMessages.mockImplementation(async () => {
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 5);
      });
      connectionsAtGet = scripted.connections.length;
      getSettled = true;
      return true;
    });

    await _revalidateForTest(ctx({ cause: "visible" }));

    expect(getSettled).toBe(true);
    // The reconnect came AFTER the GET settled: one connection existed while it ran.
    expect(connectionsAtGet).toBe(1);
    await until(() => scripted.connections.length === 2);
    expect(first?.aborted()).toBe(true);
    // A FRESH hello: the cursor was dropped, so the second connect presents none.
    expect(scripted.connections[1]?.headers.has("Last-Event-ID")).toBe(false);
  });

  it("the fresh hello after a pending reconnect digests once and never reconnects again, though status still reads moved", async () => {
    observeStamp({ kind: "status", ref: "", version: "1" });
    await connectSettled();
    // The realistic answer, left in place: the map still holds status@1 while the
    // hello's own status_snapshot@2 sits held behind the run, so the server keeps
    // naming it until that frame drains.
    scripted.respond("/api/sync", digestAnswer([{ kind: "status", ref: "", version: "2" }]));

    await _revalidateForTest(ctx({ cause: "visible" }));
    await until(() => scripted.connections.length === 2);
    const syncsBefore = syncs();
    scripted.connections[1]?.hello();
    await until(() => syncs() > syncsBefore);
    // Settle: nothing else is queued behind it, and no third connection was opened.
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 50);
    });
    expect(syncs()).toBe(syncsBefore + 1);
    expect(scripted.connections).toHaveLength(2);
  });

  it("binds the epoch BEFORE the reconcile on must_refetch", async () => {
    versionMap().bind(EPOCH_A);
    observeStamp({ kind: "chats", ref: "", version: "2" });
    scripted.respond("/api/sync", () => json({ epoch: EPOCH_B, head: "0", must_refetch: true }));
    let epochAtReconcile: string | null = "unset";
    let heldAtReconcile = -1;
    const unsub = onBus(BUS_RECONCILE, () => {
      epochAtReconcile = versionMap().epoch();
      heldAtReconcile = versionMap().snapshot().held.length;
    });
    try {
      await _revalidateForTest(ctx({ epoch: EPOCH_A }));
    } finally {
      unsub();
    }
    expect(epochAtReconcile).toBe(EPOCH_B);
    expect(heldAtReconcile).toBe(0);
  });

  it("runs the reconcile body with no digest when the map was just cleared", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    const causes: string[] = [];
    let signalSeen: AbortSignal | undefined;
    const unsub = onBus(BUS_RECONCILE, ({ cause, signal }) => {
      causes.push(cause);
      signalSeen = signal;
    });
    const controller = new AbortController();
    try {
      await _revalidateForTest(ctx({ cause: "hello", full: true, signal: controller.signal }));
    } finally {
      unsub();
    }
    expect(causes).toEqual(["full:hello"]);
    expect(signalSeen).toBe(controller.signal);
    expect(scripted.requests).toEqual([]);
  });
});

// The body the worker host routes to this tab. No worker runs here: the host's frames,
// hellos and runs are the library's (sse-worker-host.node.test.ts drives the real host);
// what this pins is what THIS tab does with a run it did not own the cause of.
describe("the body the host routes to this tab", () => {
  it("applies the run's verdict with no digest of its own: the named chat reaches its loader with the run's signal", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    const controller = new AbortController();

    await _tabRevalidateForTest(
      tabCtx({
        signal: controller.signal,
        changed: [{ kind: "chat", ref: "X", version: "4" }],
        removed: [],
      }),
    );

    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, controller.signal);
    // Nothing POSTed: the host performed the profile's one digest.
    expect(scripted.requests).toEqual([]);
  });

  it("completes every GET and asks for no hello of its own when pending moved: that decision is the profile's", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "pending", ref: "", version: "1" });
    await connectSettled();
    let getSettled = false;
    mockLoadMessages.mockImplementation(async () => {
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 5);
      });
      getSettled = true;
      return true;
    });

    await expect(
      _tabRevalidateForTest(
        tabCtx({
          cause: "visible",
          changed: [
            { kind: "chat", ref: "X", version: "4" },
            { kind: "pending", ref: "", version: "2" },
          ],
          removed: [],
        }),
      ),
    ).resolves.toBeUndefined();

    expect(getSettled).toBe(true);
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 20);
    });
    // The host reconnects the profile's stream once; N tabs asking would open N.
    expect(scripted.connections).toHaveLength(1);
  });

  it("a changed subject this tab does not hold reaches no loader", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });

    await _tabRevalidateForTest(
      tabCtx({
        changed: [
          { kind: "chat", ref: "X", version: "4" },
          { kind: "chat", ref: "Y", version: "7" },
        ],
        removed: [],
      }),
    );

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, expect.any(AbortSignal));
  });

  it("a removed live turn this tab does not hold runs no GET, while a removed chat is dropped whatever this tab holds", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    await connectSettled();

    await _tabRevalidateForTest(
      tabCtx({
        changed: [],
        removed: [
          { kind: "live_turn", ref: "Z", reason: "gone" },
          { kind: "chat", ref: "Y", reason: "gone" },
        ],
      }),
    );

    expect(mockLoadMessages).not.toHaveBeenCalled();
    expect(seen).toEqual([{ type: "chat_deleted", chat_id: "", payload: { id: "Y" } }]);
  });

  it("a run at an epoch this tab does not hold clears the map and reconciles in full, once", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    const causes: string[] = [];
    const unsub = onBus(BUS_RECONCILE, ({ cause }) => {
      causes.push(cause);
    });
    try {
      await _tabRevalidateForTest(tabCtx({ cause: "hello", epoch: EPOCH_B, full: false }));
      expect(versionMap().epoch()).toBe(EPOCH_B);
      expect(versionMap().snapshot().held).toEqual([]);
      // The same epoch again: nothing dropped, an empty verdict, nothing to reconcile.
      await _tabRevalidateForTest(tabCtx({ cause: "hello", epoch: EPOCH_B, full: false }));
    } finally {
      unsub();
    }
    expect(causes).toEqual(["full:hello"]);
    expect(scripted.requests).toEqual([]);
  });

  it("a run at this tab's own epoch keeps the map and applies an empty verdict as nothing", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    const causes: string[] = [];
    const unsub = onBus(BUS_RECONCILE, ({ cause }) => {
      causes.push(cause);
    });
    try {
      await _tabRevalidateForTest(tabCtx({ cause: "hello", epoch: EPOCH_A }));
    } finally {
      unsub();
    }
    expect(causes).toEqual([]);
    expect(versionMap().snapshot().held).toEqual([{ kind: "chats", ref: "", version: "2" }]);
    expect(scripted.requests).toEqual([]);
    expect(mockLoadList).not.toHaveBeenCalled();
  });

  it("emits BUS_PAGE_RESUMED on a profile wake only in a VISIBLE tab", async () => {
    let resumed = 0;
    const unsub = onBus(BUS_PAGE_RESUMED, () => {
      resumed++;
    });
    try {
      const hidden = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
      await _tabRevalidateForTest(tabCtx({ cause: "visible" }));
      expect(resumed).toBe(0);
      hidden.mockRestore();

      await _tabRevalidateForTest(tabCtx({ cause: "visible" }));
      expect(resumed).toBe(1);
      await _tabRevalidateForTest(tabCtx({ cause: "hello" }));
      expect(resumed).toBe(1);
    } finally {
      unsub();
    }
  });
});

// This tab attached to the REAL host (sse-worker-host.ts) over a MessageChannel in
// place of a SharedWorker, with the host's stream and digest on the same scripted
// fetch: what crosses the port is what a profile's tabs and worker exchange.
describe("attached to a worker host", () => {
  /** Boot the adapter onto a host this test holds and answer the host's first hello. */
  async function connectHosted(): Promise<ReturnType<ScriptedFetch["connections"]["at"]>> {
    const host = createSSEHost();
    hosted = host;
    _spawnWorkerForTest(() => {
      const channel = new MessageChannel();
      host.attach(channel.port2);
      return { port: channel.port1, addEventListener: () => undefined };
    });
    init(
      (evt) => {
        seen.push(evt);
      },
      () => undefined,
    );
    await until(() => scripted.connections.length === 1);
    const conn = scripted.connections[0];
    conn?.hello();
    await until(() => host.stream().state().kind === "open");
    return conn;
  }

  /** Hide and show the page, which the tab reports and the host's stream wakes on. */
  async function wakeProfile(): Promise<void> {
    const hidden = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 10);
    });
    hidden.mockRestore();
    document.dispatchEvent(new Event("visibilitychange"));
  }

  it("a stamp this tab records reaches the host's map, whose ONE digest names it, and the verdict comes back to this tab's loader", async () => {
    setSessions([makeSession("c1", { residency: "loaded" })]);
    scripted.respond("/api/sync", digestAnswer([{ kind: "chat", ref: "c1", version: "5" }]));
    const conn = await connectHosted();
    markHydrated();
    conn?.frame(
      {
        type: "message_appended",
        chat_id: "c1",
        payload: { id: "m1", role: "user", ts: 1, content: "hi" },
        subject: { kind: "chat", ref: "c1", version: "4" },
      },
      `${EPOCH_A}:1`,
    );
    await until(() => seen.length === 1);

    await wakeProfile();
    await until(() => mockLoadMessages.mock.calls.length === 1);

    expect(syncs()).toBe(1);
    const body = JSON.parse(scripted.requests[0]?.body ?? "{}") as Record<string, unknown>;
    expect(body).toEqual({ epoch: EPOCH_A, subjects: [{ kind: "chat", ref: "c1", version: "4" }] });
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, expect.any(AbortSignal));
  });

  it("adopting the subscription's tag moves SSE-Client on the PROFILE's stream with one resumed reconnect", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    const first = await connectHosted();
    expect(first?.headers.get("SSE-Client")).toBe("held-tag_0123456789AB");

    const endpoint =
      "https://fcm.googleapis.com/fcm/send/dQw4w9WgXcQ:APA91bGolden-Fixture-Endpoint-0001";
    await adoptPushSubscription({ endpoint });
    await until(() => scripted.connections.length === 2);
    expect(first?.aborted()).toBe(true);
    expect(scripted.connections[1]?.headers.get("SSE-Client")).toBe("amxAEqwvwjG23476CxNmK6");
    // The cursor is kept: the tag moved, the profile lost no frame.
    expect(scripted.connections[1]?.headers.get("Last-Event-ID")).not.toBeNull();
    expect(presentedTag()).toBe("amxAEqwvwjG23476CxNmK6");

    await adoptPushSubscription({ endpoint });
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 20);
    });
    expect(scripted.connections).toHaveLength(2);
  });
});
