// ---------------------------------------------------------------------------
// What the reader is told when a retry lands.
//
// The action used to be built by `runControl`, which answers `{ok:true}` for every
// verb: no success notification, no refetch, and a reply whose outcome was thrown
// away. So a retry that reset five nodes and one that reset none produced the same
// silence, and the second is exactly what "I pressed Retry and nothing happened"
// looks like from the outside. These cases pin all three halves of the fix — the
// outcome is decoded, reported through the channel it deserves, and followed by a
// refetch.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

const m = vi.hoisted(() => ({
  /** The next `POST /api/runs/{id}/retry` outcome, or an error to throw. */
  result: { current: undefined as unknown, err: undefined as unknown },
  invalidated: [] as string[],
  controlsInvalidated: [] as string[],
}));

// The action itself is NOT mocked: its decode / notify / onSuccess wiring is the
// subject, so it runs for real over a canned HTTP response installed through
// `configureApi`'s fetch seam. Mocking the action would test nothing.

vi.mock("../toast.js", () => ({
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}));

vi.mock("../run-store.js", () => ({
  invalidateRun: vi.fn((id: string) => {
    m.invalidated.push(id);
  }),
  invalidateRunControls: vi.fn((id: string) => {
    m.controlsInvalidated.push(id);
  }),
}));

import { configure, configureApi } from "@cplieger/actions";
import { retryRun } from "./runs.js";
import { error as toastError, success as toastSuccess } from "../toast.js";

/** Answer the next fetch with a 2xx body, or with a JSON error envelope. */
function stubFetch(): void {
  configureApi({
    fetchFn: () => {
      const err = m.result.err as { status: number; body: unknown } | undefined;
      if (err !== undefined) {
        return Promise.resolve(
          new Response(JSON.stringify(err.body), {
            status: err.status,
            headers: { "content-type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify(m.result.current), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    },
  });
}

beforeEach(() => {
  m.result.current = undefined;
  m.result.err = undefined;
  m.invalidated.length = 0;
  m.controlsInvalidated.length = 0;
  // Re-armed per test: configureApi REPLACES the library's private fetch instance,
  // and no reset hook is exported, so each case installs its own answer.
  stubFetch();
  // The notifier the app wires at boot, so a REFUSAL's sentence is observable
  // rather than silently dropped by the library's headless default — which is how
  // the "…: internal error" prefix went unnoticed in the first place.
  configure({
    success: (msg) => {
      toastSuccess(msg);
    },
    error: (msg) => {
      toastError(msg);
    },
  });
});

describe("retrying a run", () => {
  it("states how many steps a real retry reset", async () => {
    m.result.current = { status: "running", retried_node_ids: ["phase-c", "phase-d"] };
    const res = await retryRun.dispatch("wf_1");

    expect(res).not.toBeNull();
    expect(vi.mocked(toastSuccess)).toHaveBeenCalledWith("Retrying 2 steps");
    expect(vi.mocked(toastError)).not.toHaveBeenCalled();
  });

  // THE DEFECT. A zero-node retry is a first-class outcome upstream and a no-op from
  // the reader's seat, and it used to be reported exactly like a five-node one:
  // silently. Reporting it as a success would be the same defect wearing a toast.
  it("does not report a retry that reset NOTHING as a success", async () => {
    m.result.current = { status: "aborted", retried_node_ids: [] };
    await retryRun.dispatch("wf_1");

    expect(vi.mocked(toastSuccess)).not.toHaveBeenCalled();
    const said = vi.mocked(toastError).mock.calls[0]?.[0] ?? "";
    expect(said).toContain("Nothing to retry");
  });

  // The success path used to trigger no refetch, deliberately: the row was left to
  // be repainted by a `run_progress` frame. A no-op retry produces no such frame, so
  // on exactly the outcome the reader most needs to see, the screen never moved.
  it("refetches the run AND its affordance, even after a no-op", async () => {
    for (const nodes of [["phase-c"], []]) {
      m.invalidated.length = 0;
      m.controlsInvalidated.length = 0;
      m.result.current = { status: "running", retried_node_ids: nodes };
      await retryRun.dispatch("wf_1");
      expect(m.invalidated).toEqual(["wf_1"]);
      expect(m.controlsInvalidated).toEqual(["wf_1"]);
    }
  });

  // The refusal reads as the SERVER's own sentence, `answerRunInput`'s rule. A
  // static `error` string does not replace the server's message, it PREFIXES it —
  // so before this the reader saw "Couldn't retry the run: internal error", which
  // asserts a failure and then explains nothing.
  it("shows a refusal as the server's sentence alone", async () => {
    const sentence = "Workflow wf_1 is not registered. Load or create it first.";
    m.result.err = { status: 409, body: { error: sentence } };
    const res = await retryRun.dispatch("wf_1");

    expect(res).toBeNull();
    expect(m.invalidated).toEqual([]);
    // Exactly the sentence, with nothing in front of it. A static `error` string
    // does not replace the server's message, it PREFIXES it — measured, and the
    // opposite of what it looks like — so "Couldn't retry the run: <sentence>"
    // asserts a failure and then explains that the reader must do something else.
    expect(vi.mocked(toastError)).toHaveBeenCalledWith(sentence);
  });

  // A transport failure has no server sentence, so the fallback is what the reader
  // gets. It must still name the verb rather than leaking the library's own
  // placeholder.
  it("falls back to naming the verb when there is no server sentence", async () => {
    m.result.err = { status: 500, body: {} };
    await retryRun.dispatch("wf_1");

    expect(vi.mocked(toastError)).toHaveBeenCalledWith("Couldn't retry the run");
  });

  // A 2xx whose body is not an outcome must fail at the boundary rather than
  // reaching the notification as `undefined` and reporting "Retrying NaN steps".
  it("rejects a 2xx body that is not an outcome, and still says something", async () => {
    m.result.current = { ok: true };
    const res = await retryRun.dispatch("wf_1");

    expect(res).toBeNull();
    expect(vi.mocked(toastSuccess)).not.toHaveBeenCalled();
    expect(m.invalidated).toEqual([]);
    // The negatives alone would leave this path indistinguishable from the second
    // unacceptable failure mode — a refusal the reader never sees — on the one path
    // where the HTTP reply is well-formed and its CONTENT is not. A decode failure
    // carries no server sentence, so the fallback has to name the verb.
    expect(vi.mocked(toastError)).toHaveBeenCalledWith("Couldn't retry the run");
  });
});
