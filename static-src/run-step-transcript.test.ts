// The on-demand step-transcript reader: which URL it asks for, when it declines to
// ask, how it GRADES a failure, and what it hands the render lifecycle.
//
// `api-client.js` is mocked and everything else is real, including the GENERATED
// decoder — which is the point of asking for a typed read: a reply the decoder
// rejects must reach the caller as the transient verdict rather than as content.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Block, Message, RunStepTranscript } from "./types.js";

const apiGetTypedOrError = vi.fn<(url: string, decode: unknown) => Promise<unknown>>();

vi.mock("./api-client.js", () => ({
  apiGetTypedOrError: (url: string, decode: unknown) => apiGetTypedOrError(url, decode),
  // Present-but-inert so real-ESM linking succeeds whatever else this graph reaches.
  // No case here calls them.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPost: vi.fn(),
  apiDelete: vi.fn(),
}));

const {
  clearStepTranscripts,
  requestStepTranscript,
  stepRead,
  stepSliceFor,
  stepTranscriptVersion,
} = await import("./run-step-transcript.js");

/** The envelope `apiGetTypedOrError` answers with, structurally — `ApiResult` is
 *  private to `api-client.ts` and the consumer destructures rather than naming it. */
interface Envelope {
  readonly ok: boolean;
  readonly status: number;
  readonly data: RunStepTranscript | null;
  readonly error: string;
}

/** A 2xx envelope carrying a decoded reply in the shape the endpoint answers with. */
function ok(over: Partial<RunStepTranscript> = {}): Envelope {
  const body: RunStepTranscript = {
    messages: [],
    workflow_id: "wf_1",
    node_path: "wf_1/a",
    state: "ready",
    ...over,
  };
  return { ok: true, status: 200, data: body, error: "" };
}

/** A FAILURE envelope at `status`. `data` is null by construction on this side:
 *  `toApiResult` collapses it, because a caller handed a status has no business
 *  reading a body the transport rejected. */
function fail(status: number): Envelope {
  return { ok: false, status, data: null, error: "refused" };
}

/** One assistant message carrying `blocks`. */
function assistant(blocks: Block[], id = "m1"): Message {
  return { id, role: "assistant", ts: 0, content: "", blocks };
}

/** Ask, then wait for the answer that was queued for this call. */
async function ask(workflowID: string, nodePath: string, answer: unknown): Promise<void> {
  apiGetTypedOrError.mockResolvedValueOnce(answer);
  requestStepTranscript(workflowID, nodePath);
  // Three turns: the fetch's own await, the `finally`, and the caller's read.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

/** Ask, with the fetch queued to THROW rather than answer. */
async function reject(workflowID: string, nodePath: string): Promise<void> {
  apiGetTypedOrError.mockRejectedValueOnce(new Error("boom"));
  requestStepTranscript(workflowID, nodePath);
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  clearStepTranscripts();
  apiGetTypedOrError.mockReset();
  stepTranscriptVersion.value = 0;
});

describe("run-step-transcript: the URL", () => {
  it("keeps the separators raw and encodes each segment", async () => {
    await ask("wf_1", "wf_1/loop/iter-0/build", ok());
    expect(apiGetTypedOrError.mock.calls[0]?.[0]).toBe(
      "/api/runs/wf_1/steps/wf_1/loop/iter-0/build",
    );
  });

  // The one encoding rule this route has. A raw `#` would truncate the path at the
  // fragment; an encoded "/" would be refused by the server's canonical-path gate,
  // which compares the DECODED path against what its router would match.
  it("encodes a segment's own metacharacters without encoding the separators", async () => {
    await ask("wf_1", "wf_1/a b#0/build", ok());
    expect(apiGetTypedOrError.mock.calls[0]?.[0]).toBe("/api/runs/wf_1/steps/wf_1/a%20b%230/build");
  });
});

describe("run-step-transcript: when it asks", () => {
  it("records `loading` before the answer arrives", () => {
    apiGetTypedOrError.mockResolvedValueOnce(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("loading");
  });

  it("never refetches a settled answer", async () => {
    for (const state of ["ready", "gone"] as const) {
      clearStepTranscripts();
      apiGetTypedOrError.mockReset();
      await ask("wf_1", "wf_1/a", ok({ state }));
      expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
      requestStepTranscript("wf_1", "wf_1/a");
      requestStepTranscript("wf_1", "wf_1/a");
      expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
    }
  });

  // `unavailable` is the one verdict that means "could not be completed", which is
  // transient by definition — so it is the one a later ask retries. What bounds that
  // retry is the CALLER arming a read once per shown (node, state), not a backoff
  // here.
  it("retries an unavailable answer", async () => {
    await ask("wf_1", "wf_1/a", ok({ state: "unavailable" }));
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
    await ask("wf_1", "wf_1/a", ok({ state: "ready" }));
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("ready");
  });

  // A repaint during a fetch must not start a second one, or a page that repaints on
  // every store invalidation would issue a request per frame.
  it("issues one request while one is outstanding", () => {
    apiGetTypedOrError.mockResolvedValue(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    requestStepTranscript("wf_1", "wf_1/a");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
  });

  it("asks nothing for an empty run or an empty path", () => {
    requestStepTranscript("", "wf_1/a");
    requestStepTranscript("wf_1", "");
    expect(apiGetTypedOrError).not.toHaveBeenCalled();
  });

  it("keys reads per step, so one step's answer is not another's", async () => {
    await ask("wf_1", "wf_1/a", ok({ messages: [assistant([{ type: "text", text: "A" }])] }));
    await ask("wf_1", "wf_1/b", ok({ messages: [assistant([{ type: "text", text: "B" }])] }));
    expect(stepSliceFor("wf_1", "wf_1/a")?.blocks[0]).toMatchObject({ text: "A" });
    expect(stepSliceFor("wf_1", "wf_1/b")?.blocks[0]).toMatchObject({ text: "B" });
  });

  // A 5xx is the server failing to answer, which is transient by definition, so it
  // keeps the retryable verdict AND a later ask retries it.
  it("records a 5xx as unavailable and retries it", async () => {
    await ask("wf_1", "wf_1/a", fail(500));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
    await ask("wf_1", "wf_1/a", ok());
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  // A 2xx the DECODER rejected arrives on the failure side carrying its real 2xx
  // status, so it must not be graded as the caller's mistake.
  it("records an undecodable 2xx as unavailable", async () => {
    await ask("wf_1", "wf_1/a", fail(200));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
  });

  // A 4xx is the SERVER refusing the address: 404 is `errStepUnknown` (this run has
  // no step at that path) and 400 is the first-segment assertion. Asking again fails
  // identically, so the verdict is settled and the reader is offered no retry — which
  // is the whole point of the state. The second half of each case is the one that
  // matters: `settled()` must swallow the next ask.
  it.each([404, 400])("records a %i as the settled unaddressable verdict", async (status) => {
    await ask("wf_1", "wf_1/a", fail(status));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unaddressable");
    requestStepTranscript("wf_1", "wf_1/a");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
  });

  // A throw never reached the server, so it grades as status 0 — transient, like the
  // 5xx above and unlike a 4xx. It must also be CAUGHT, because the caller `void`s
  // the fetch by design, so an escaping rejection is an unhandled one; this suite
  // fails the file on one, which is the other half of the assertion.
  it("records a thrown fetch as unavailable and retries it", async () => {
    await reject("wf_1", "wf_1/a");
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  // A status-0 envelope is the same fact reported through the RESULT rather than a
  // throw (a dead network, an aborted request), so it takes the same verdict.
  it("records a status-0 transport failure as unavailable", async () => {
    await ask("wf_1", "wf_1/a", fail(0));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  // Left at `loading` with no request behind it, that step could never be asked
  // about again for the life of the tab.
  it("does not wedge a step when the fetch rejects", async () => {
    await reject("wf_1", "wf_1/a");
    await ask("wf_1", "wf_1/a", ok());
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("ready");
  });
});

describe("run-step-transcript: the version signal", () => {
  it("bumps once per resolved fetch, not per request", async () => {
    await ask("wf_1", "wf_1/a", ok());
    expect(stepTranscriptVersion.peek()).toBe(1);
    // A declined request resolves nothing, so it bumps nothing.
    requestStepTranscript("wf_1", "wf_1/a");
    await Promise.resolve();
    expect(stepTranscriptVersion.peek()).toBe(1);
    await ask("wf_1", "wf_1/b", ok());
    expect(stepTranscriptVersion.peek()).toBe(2);
  });

  it("bumps on a failed read too, so the note can say so", async () => {
    await ask("wf_1", "wf_1/a", fail(500));
    expect(stepTranscriptVersion.peek()).toBe(1);
  });
});

describe("run-step-transcript: the projection", () => {
  // The non-assistant rows carry real blocks and a real tool call, or this case
  // asserts nothing: with empty ones it passes whether or not `role` is read.
  it("keeps only assistant rows", async () => {
    await ask(
      "wf_1",
      "wf_1/a",
      ok({
        messages: [
          {
            id: "u1",
            role: "user",
            ts: 0,
            content: "the instruction",
            blocks: [{ type: "text", text: "the instruction" }],
          },
          assistant([{ type: "text", text: "the answer" }]),
          {
            id: "e1",
            role: "event",
            ts: 0,
            content: "",
            blocks: [{ type: "text", text: "cancelled" }],
            tool_calls: [{ id: "t9", title: "Read", kind: "read", status: "completed", ts: 0 }],
          },
        ],
      }),
    );
    const slice = stepSliceFor("wf_1", "wf_1/a");
    expect(slice?.blocks).toEqual([{ type: "text", text: "the answer" }]);
    expect(slice?.toolCalls).toEqual([]);
  });

  it("flattens several assistant messages in order", async () => {
    await ask(
      "wf_1",
      "wf_1/a",
      ok({
        messages: [
          assistant([{ type: "text", text: "one" }], "m1"),
          assistant([{ type: "text", text: "two" }], "m2"),
        ],
      }),
    );
    expect(stepSliceFor("wf_1", "wf_1/a")?.blocks.map((b) => b.text)).toEqual(["one", "two"]);
  });

  it("carries the tool calls, or a tool_use block renders blank", async () => {
    const msg = assistant([{ type: "tool_use", tool_call_id: "t1" }]);
    await ask(
      "wf_1",
      "wf_1/a",
      ok({
        messages: [
          {
            ...msg,
            tool_calls: [{ id: "t1", title: "Read", kind: "read", status: "completed", ts: 0 }],
          },
        ],
      }),
    );
    expect(stepSliceFor("wf_1", "wf_1/a")?.toolCalls.map((t) => t.id)).toEqual(["t1"]);
  });

  // THE rule that differs from `run-step-slice.ts`, which clears every id. Here the
  // blocks come from the step's OWN session, which can hold a DELEGATE too:
  // `containerFor` routes by this field, so clearing a delegate's uuid would collapse
  // its box into the step's prose while leaving a `wf:` id would build a nested run
  // card inside the run page.
  it("clears a wf: id and KEEPS a delegate's uuid", async () => {
    await ask(
      "wf_1",
      "wf_1/a",
      ok({
        messages: [
          assistant([
            { type: "text", text: "the step's own prose", agent_subtask_id: "wf:wf_1:wf_1/a" },
            {
              type: "text",
              text: "a delegate's prose",
              agent_subtask_id: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
            },
          ]),
        ],
      }),
    );
    const blocks = stepSliceFor("wf_1", "wf_1/a")?.blocks ?? [];
    expect(blocks[0]).not.toHaveProperty("agent_subtask_id");
    expect(blocks[1]?.agent_subtask_id).toBe("0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0");
  });

  // A malformed `wf:` id parses to null and is KEPT, which is the same fall-through
  // the transcript takes: the renderer draws it as a delegate box rather than losing
  // the block.
  it("keeps a malformed wf: id rather than losing the block", async () => {
    await ask(
      "wf_1",
      "wf_1/a",
      ok({
        messages: [
          assistant([{ type: "text", text: "x", agent_subtask_id: "wf:no-second-colon" }]),
        ],
      }),
    );
    const blocks = stepSliceFor("wf_1", "wf_1/a")?.blocks ?? [];
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.agent_subtask_id).toBe("wf:no-second-colon");
  });

  it("does not mutate the blocks it was handed", async () => {
    const block: Block = { type: "text", text: "x", agent_subtask_id: "wf:wf_1:wf_1/a" };
    await ask("wf_1", "wf_1/a", ok({ messages: [assistant([block])] }));
    expect(block.agent_subtask_id).toBe("wf:wf_1:wf_1/a");
  });

  // `sourceKeys` empty and `live` false are facts rather than gaps: these blocks are
  // not in the message store, so they have no per-block streaming signals, and the
  // answer is a settled read rather than a stream — a caret over it would claim
  // content is still arriving.
  it("reports no source keys and no liveness", async () => {
    await ask("wf_1", "wf_1/a", ok({ messages: [assistant([{ type: "text", text: "x" }])] }));
    const slice = stepSliceFor("wf_1", "wf_1/a");
    expect(slice?.sourceKeys).toEqual([]);
    expect(slice?.live).toBe(false);
  });
});

describe("run-step-transcript: what stepSliceFor withholds", () => {
  it("withholds a step nobody asked about", () => {
    expect(stepSliceFor("wf_1", "wf_1/a")).toBeUndefined();
  });

  it("withholds a loading, gone or unavailable read", async () => {
    for (const state of ["gone", "unavailable"] as const) {
      clearStepTranscripts();
      await ask("wf_1", "wf_1/a", ok({ state }));
      expect(stepSliceFor("wf_1", "wf_1/a")).toBeUndefined();
    }
    clearStepTranscripts();
    apiGetTypedOrError.mockResolvedValueOnce(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    expect(stepSliceFor("wf_1", "wf_1/a")).toBeUndefined();
  });

  // A `ready` read with no blocks is its own fact — the step ran and wrote nothing —
  // and the NOTE says so. Handing the render lifecycle an empty slice would build an
  // empty body and hide that note behind it.
  it("withholds a ready read that carries no blocks", async () => {
    await ask("wf_1", "wf_1/a", ok({ state: "ready", messages: [] }));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("ready");
    expect(stepSliceFor("wf_1", "wf_1/a")).toBeUndefined();
  });
});

describe("run-step-transcript: the bound", () => {
  it("clearStepTranscripts empties the cache", async () => {
    await ask("wf_1", "wf_1/a", ok({ messages: [assistant([{ type: "text", text: "x" }])] }));
    expect(stepRead("wf_1", "wf_1/a")).toBeDefined();
    clearStepTranscripts();
    expect(stepRead("wf_1", "wf_1/a")).toBeUndefined();
    expect(stepSliceFor("wf_1", "wf_1/a")).toBeUndefined();
  });

  // The in-flight set goes with it, or a read outstanding across a retarget would
  // block the new page from ever asking for that step.
  it("clears the in-flight set too", async () => {
    apiGetTypedOrError.mockResolvedValueOnce(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    clearStepTranscripts();
    await ask("wf_1", "wf_1/a", ok());
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });
});
