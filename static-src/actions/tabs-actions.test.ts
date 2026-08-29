// ---------------------------------------------------------------------------
// The four tab mutations, and the two opposite properties their coalescing has
// to hold at once.
//
// A DOUBLE GESTURE IS ONE MUTATION: two taps on one door open one tab, which is
// what `dedupe` with a key function on (kind, ref) buys. A REPEATED GESTURE IS
// SEVERAL MUTATIONS: pin → unpin → pin has to end pinned and a drag A → B → A has
// to end at A, so nothing may collapse the third onto the first. Those pull in
// opposite directions, and both have shipped broken in this fleet before — once as
// an arg-composite idempotency key replaying a cached success (`files.rename`), and
// once as a dedupe default whose key included a unique id and therefore collapsed
// nothing at all.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({
  send: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  newOpID: vi.fn(() => "op-test"),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { error as toastError } from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import {
  openTabCommand,
  closeTabCommand,
  pinTabCommand,
  reorderTabsCommand,
  REORDER_STALE,
} from "./tabs.js";

const mockSend = vi.mocked(transportSend);
const mockToastError = vi.mocked(toastError);

/** One sent envelope, as the assertions read it. */
interface Sent {
  type: string;
  payload: Record<string, unknown>;
}

function sent(): Sent[] {
  return mockSend.mock.calls.map((c) => c[0] as unknown as Sent);
}

/** The per-dispatch idempotency keys, in send order. */
function idempotencyKeys(): (string | undefined)[] {
  return mockSend.mock.calls.map((c) => {
    const v = (c[0] as unknown as Record<string, unknown>)["idempotency_key"];
    return typeof v === "string" ? v : undefined;
  });
}

function okWith(body: unknown): { ok: true; status: 200; body: unknown } {
  return { ok: true, status: 200, body };
}

function subject(id: string, over: Record<string, unknown> = {}): Record<string, unknown> {
  return { id, kind: "chat", ref: "c-1", parent: "", pinned: false, owns: true, ...over };
}

/** A reply that does not resolve until released, so a second dispatch is
 *  genuinely IN FLIGHT alongside the first — which is the only window `dedupe`
 *  covers. */
function heldReply(body: unknown): { release: () => void } {
  let release = (): void => {
    /* replaced by the promise below */
  };
  const held = new Promise<void>((r) => {
    release = r;
  });
  mockSend.mockImplementationOnce(async () => {
    await held;
    return okWith(body) as never;
  });
  return { release };
}

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockSend.mockResolvedValue(okWith({ ok: true }) as never);
});

// --- open_tab ---

describe("open_tab", () => {
  it("sends kind, ref, parent, owns and the op id", async () => {
    mockSend.mockResolvedValue(
      okWith({ subject: subject("t1"), created: true, version: 4 }) as never,
    );
    await openTabCommand.dispatch({
      kind: "editor",
      ref: "/workspace/a.ts",
      parent: "p1",
      owns: true,
      opID: "op-1",
    });
    expect(sent()[0]?.type).toBe("open_tab");
    expect(sent()[0]?.payload).toEqual({
      kind: "editor",
      op_id: "op-1",
      ref: "/workspace/a.ts",
      parent: "p1",
      owns: true,
    });
  });

  it("carries a FRESH idempotency key per dispatch, never one derived from the args", async () => {
    // An arg-composite key is the same defect as a collapsing dedupe seen from the
    // other side: inside the 5-minute cache a repeated mutation replays a cached
    // success and never runs. The key is per-dispatch; `op_id` is what correlates.
    mockSend.mockResolvedValue(okWith({ subject: subject("t1"), created: true }) as never);
    const args = { kind: "chat", ref: "c-1", parent: "", owns: true } as const;
    await openTabCommand.dispatch({ ...args, opID: "op-1" });
    await openTabCommand.dispatch({ ...args, opID: "op-2" });
    const keys = idempotencyKeys();
    expect(keys[0]).toBeTypeOf("string");
    expect(keys[0]).not.toBe(keys[1]);
  });

  it("omits ref, parent and owns rather than sending empty values", async () => {
    mockSend.mockResolvedValue(
      okWith({ subject: subject("t1", { kind: "settings", ref: "" }), created: true }) as never,
    );
    await openTabCommand.dispatch({
      kind: "settings",
      ref: "",
      parent: "",
      owns: false,
      opID: "op-2",
    });
    expect(sent()[0]?.payload).toEqual({ kind: "settings", op_id: "op-2" });
  });

  it("reports created:false for a tab that is already open", async () => {
    // The idempotent open. Nothing was committed, so NO frame will follow — a
    // caller that waited only for one would wait forever, which is the silent
    // no-op this flag exists to remove.
    mockSend.mockResolvedValue(
      okWith({ subject: subject("t1"), created: false, version: 9 }) as never,
    );
    const reply = await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-3",
    });
    expect(reply?.created).toBe(false);
    expect(reply?.subject.id).toBe("t1");
    // The widened reply: the collection version rides to the pending-op machine.
    expect(reply?.version).toBe(9);
  });

  it("collapses two dispatches 0ms apart into ONE round trip", async () => {
    const { release } = heldReply({ subject: subject("t1"), created: true });
    const first = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-a",
    });
    // Same (kind, ref), a different op id — which is the whole trap: the default
    // key is safeStringify(args), so the unique op would make every gesture its
    // own key and the option would collapse nothing.
    const second = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-b",
    });
    release();
    const [a, b] = await Promise.all([first, second]);
    expect(mockSend).toHaveBeenCalledTimes(1);
    // Both callers get the SAME answer, which is what lets each run its own
    // activation against one open.
    expect(a?.subject.id).toBe("t1");
    expect(b?.subject.id).toBe("t1");
  });

  it("does NOT collapse a different (kind, ref)", async () => {
    const { release } = heldReply({ subject: subject("t1"), created: true });
    const first = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-a",
    });
    mockSend.mockResolvedValueOnce(okWith({ subject: subject("t2"), created: true }) as never);
    const second = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-2",
      parent: "",
      owns: true,
      opID: "op-b",
    });
    release();
    await Promise.all([first, second]);
    expect(mockSend).toHaveBeenCalledTimes(2);
  });

  it("reaches the server again once the first dispatch has resolved", async () => {
    // `dedupe` covers the IN-FLIGHT window only — the framework evicts the slot in
    // result.finally. Outside it the server's own (kind, ref) uniqueness answers a
    // late second tap by returning the tab already open, which is why nothing
    // further is needed here.
    mockSend.mockResolvedValue(okWith({ subject: subject("t1"), created: true }) as never);
    await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-a",
    });
    mockSend.mockResolvedValue(okWith({ subject: subject("t1"), created: false }) as never);
    const again = await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-b",
    });
    expect(mockSend).toHaveBeenCalledTimes(2);
    expect(again?.created).toBe(false);
  });

  it("names the remedy when the workspace is at its tab limit", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "too many tabs" } as never);
    await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-4",
    });
    // The refusal has a remedy, and a control that says only "failed" reads as
    // broken rather than as bounded.
    expect(mockToastError.mock.calls[0]?.[0]).toContain("Close a tab first");
  });

  it("surfaces a failure and returns nothing to paint from", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" } as never);
    const out = await openTabCommand
      .dispatch({ kind: "chat", ref: "c-1", parent: "", owns: true, opID: "op-5" })
      .catch(() => null);
    expect(out).toBeNull();
    expect(mockToastError).toHaveBeenCalled();
  });
});

// --- close_tab ---

describe("close_tab", () => {
  it("returns every id the mutation closed, with the committed version", async () => {
    // A parent and its children go as ONE mutation, so this is a list — and the
    // version rides beside it for the pending-op machine.
    mockSend.mockResolvedValue(okWith({ closed: ["p", "c1", "c2"], version: 3 }) as never);
    const reply = await closeTabCommand.dispatch({ id: "p", opID: "op-1" });
    expect(reply?.closed).toEqual(["p", "c1", "c2"]);
    expect(reply?.version).toBe(3);
  });

  it("treats an empty list as a normal answer, not a failure", async () => {
    // Two devices can close one tab. Closing an id that is not open commits
    // nothing and is not an error — the empty list is the machine's semantic
    // confirmation of absence.
    mockSend.mockResolvedValue(okWith({ closed: [], version: 3 }) as never);
    const reply = await closeTabCommand.dispatch({ id: "gone", opID: "op-2" });
    expect(reply?.closed).toEqual([]);
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("collapses two closes of the same tab", async () => {
    const { release } = heldReply({ closed: ["t1"] });
    const a = closeTabCommand.dispatch({ id: "t1", opID: "op-a" });
    const b = closeTabCommand.dispatch({ id: "t1", opID: "op-b" });
    release();
    await Promise.all([a, b]);
    expect(mockSend).toHaveBeenCalledTimes(1);
  });
});

// --- pin_tab, and the repeat that must NOT collapse ---

describe("pin_tab", () => {
  it("executes a repeated pin -> unpin -> pin, all three", async () => {
    // The third gesture repeats the first. Any coalescing keyed on (id, pinned)
    // would collapse it onto the first while it was still in flight and leave the
    // tab UNPINNED — silently, which is the failure mode that makes this worth a
    // test rather than a comment.
    await pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-1" });
    await pinTabCommand.dispatch({ id: "t1", pinned: false, opID: "op-2" });
    await pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-3" });
    expect(sent().map((s) => s.payload["pinned"])).toEqual([true, false, true]);
    // And each carries its OWN idempotency key. An arg-composite key would make
    // the third gesture identical to the first, so a real server would replay the
    // first's cached success and the tab would end UNPINNED — the same silent
    // outcome a collapsing dedupe produces, reached through the other mechanism.
    expect(idempotencyKeys()[0]).not.toBe(idempotencyKeys()[2]);
  });

  it("executes three concurrent pin gestures rather than collapsing them", async () => {
    const held: (() => void)[] = [];
    mockSend.mockImplementation(async () => {
      await new Promise<void>((r) => held.push(r));
      return okWith({ version: 1 }) as never;
    });
    const all = [
      pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-1" }),
      pinTabCommand.dispatch({ id: "t1", pinned: false, opID: "op-2" }),
      pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-3" }),
    ];
    expect(mockSend).toHaveBeenCalledTimes(3);
    for (const release of held) {
      release();
    }
    await Promise.all(all);
    expect(sent().map((s) => s.payload["pinned"])).toEqual([true, false, true]);
  });
});

// --- reorder_tabs ---

describe("reorder_tabs", () => {
  it("executes a drag A -> B -> A, all three", async () => {
    await reorderTabsCommand.dispatch({ order: ["a", "b"], opID: "op-1" });
    await reorderTabsCommand.dispatch({ order: ["b", "a"], opID: "op-2" });
    await reorderTabsCommand.dispatch({ order: ["a", "b"], opID: "op-3" });
    expect(sent().map((s) => s.payload["order"])).toEqual([
      ["a", "b"],
      ["b", "a"],
      ["a", "b"],
    ]);
    // Same reasoning as the repeated pin: the third order equals the first, so an
    // arg-composite idempotency key would leave the collection at B.
    expect(idempotencyKeys()[0]).not.toBe(idempotencyKeys()[2]);
  });

  it("reports a 409 as stale rather than as a failure", async () => {
    // The exact-set check refused the order because the SET moved under the drag.
    // Nothing is broken and nothing is lost, so this must not reach the error
    // notification: the caller re-lists and the drag snaps back.
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "order mismatch" } as never);
    expect(await reorderTabsCommand.dispatch({ order: ["a"], opID: "op-1" })).toBe(REORDER_STALE);
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("surfaces a real failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" } as never);
    await reorderTabsCommand.dispatch({ order: ["a"], opID: "op-1" }).catch(() => null);
    expect(mockToastError).toHaveBeenCalled();
  });

  it("sends the order as a plain array the caller cannot mutate afterwards", async () => {
    const order = ["a", "b"];
    await reorderTabsCommand.dispatch({ order, opID: "op-1" });
    order.push("c");
    expect(sent()[0]?.payload["order"]).toEqual(["a", "b"]);
  });
});
