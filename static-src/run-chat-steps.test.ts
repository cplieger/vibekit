// ---------------------------------------------------------------------------
// The chat-route step transcript's render lifecycle.
//
// What this module owns is not what a step's body LOOKS like — the transcript's own
// dispatcher decides that, and `messages-blocks.test.ts` owns it — but four
// decisions about when to touch it: build once per node path, UPDATE when the blocks
// merely grew, dispose-and-rebuild when the shape moved, and seal exactly once.
// Getting any of them wrong costs the reader their scroll position or leaves a
// finished step under a streaming caret.
//
// So `messages-blocks.js` is mocked with counting spies and the assertions are on
// the ORDER and the IDENTITY of those calls — the `run-step-blocks.test.ts` pattern,
// and the same reason: linking the real dispatcher pulls the whole transcript stack
// in to observe four function calls.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Block } from "./types.js";
import type { RunStepSlice } from "./run-step-slice.js";
// TYPE-only, so it is erased and cannot defeat the `vi.mock` hoisting the value
// import below is deferred for.
import type { RunStepPaint, RunStepSource } from "./run-chat-steps.js";

interface Call {
  fn: "build" | "update" | "finalize" | "dispose";
  /** The render key the call was made under — a synthetic message id. */
  key: string;
  /** The `agent_subtask_id` the call paired that key with. */
  subtask: string;
  host?: HTMLElement;
  blockCount?: number;
  live?: boolean;
  /** The chat id the call was handed. "" for an on-demand read, which is what makes
   *  a delegate's page link correctly absent — the dispatcher withholds it on "". */
  chatID?: string;
}

const m = vi.hoisted(() => ({ calls: [] as unknown[] }));

vi.mock("./messages-blocks.js", () => ({
  buildDetachedBody: vi.fn(
    (host: HTMLElement, msg: { id: string; blocks?: Block[] }, chatID, subtask, live) => {
      m.calls.push({
        fn: "build",
        key: msg.id,
        subtask,
        chatID,
        host,
        blockCount: (msg.blocks ?? []).length,
        live,
      });
      // A real build fills the host, so the rebuild case can prove the host was
      // cleared first rather than appended to.
      host.appendChild(document.createElement("p"));
    },
  ),
  updateDetachedBody: vi.fn(
    (host: HTMLElement, msg: { id: string; blocks?: Block[] }, chatID, subtask, live) => {
      m.calls.push({
        fn: "update",
        key: msg.id,
        subtask,
        chatID,
        host,
        blockCount: (msg.blocks ?? []).length,
        live,
      });
    },
  ),
  finalizeDetachedBody: vi.fn((key: string, subtask: string) => {
    m.calls.push({ fn: "finalize", key, subtask });
  }),
  disposeDetachedBody: vi.fn((key: string, subtask: string) => {
    m.calls.push({ fn: "dispose", key, subtask });
  }),
}));

const { createRunChatStepStream } = await import("./run-chat-steps.js");

function slice(blocks: readonly string[], live = true): RunStepSlice {
  return {
    blocks: blocks.map((text) => ({ type: "text", text })),
    toolCalls: [],
    sourceKeys: blocks.map((_, i) => `m1:${String(i)}`),
    live,
  };
}

/** A slice whose block SHAPE differs from a text run of the same length: a
 *  `tool_use` at the head reorders the prefix, which is what a rewind or a refetch
 *  does and what an incremental update cannot represent. */
function reshaped(): RunStepSlice {
  return {
    blocks: [
      { type: "tool_use", tool_call_id: "t1" },
      { type: "text", text: "one" },
    ],
    toolCalls: [],
    sourceKeys: ["m1:0", "m1:1"],
    live: true,
  };
}

function harness(): {
  apply: (slices: Record<string, RunStepSlice>, live?: boolean) => void;
  applyFrom: (source: RunStepSource, slices: Record<string, RunStepSlice>) => void;
  /** The stream's own apply, for the one case that needs two sources in ONE call. */
  applyPaints: (paints: ReadonlyMap<string, RunStepPaint>) => void;
  dispose: () => void;
  host: (path: string) => HTMLElement;
  calls: Call[];
} {
  const hosts = new Map<string, HTMLElement>();
  const stream = createRunChatStepStream((path) => {
    let host = hosts.get(path);
    if (host === undefined) {
      host = document.createElement("div");
      hosts.set(path, host);
    }
    return host;
  }, "wf_1");
  const from = (source: RunStepSource, slices: Record<string, RunStepSlice>): void => {
    const paints = new Map<string, RunStepPaint>();
    for (const [path, sl] of Object.entries(slices)) {
      paints.set(path, { slice: sl, source });
    }
    stream.apply(paints);
  };
  return {
    // Liveness now rides the SLICE, which is what lets one apply carry a live
    // chat-sourced step beside a settled on-demand read. The old `live` parameter is
    // kept in the harness so the existing cases read unchanged.
    apply: (slices, live = true) => {
      const relive: Record<string, RunStepSlice> = {};
      for (const [path, sl] of Object.entries(slices)) {
        relive[path] = { ...sl, live };
      }
      from({ kind: "chat", chatID: "c-1" }, relive);
    },
    applyFrom: from,
    applyPaints: (paints) => {
      stream.apply(paints);
    },
    dispose: () => {
      stream.dispose();
    },
    host: (path) => {
      const host = hosts.get(path);
      if (host === undefined) {
        throw new Error(`no host for ${path}`);
      }
      return host;
    },
    calls: m.calls as Call[],
  };
}

/** The build calls only. A build is preceded by an unconditional dispose (the
 *  rebuild arm's clear), which is noise in an assertion about what was built. */
function builds(h: { calls: Call[] }): Call[] {
  return h.calls.filter((c) => c.fn === "build");
}

beforeEach(() => {
  m.calls.length = 0;
});

describe("run chat step stream", () => {
  // ONE build per node path, and the two ids the four detached calls have to agree
  // on: `detachedID` composes `${messageID}#${subtask}`, so a mismatch orphans the
  // render — the build registers under one id and the dispose clears another.
  it("builds once per path, pairing the render key with the step's own wf: id", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]), "a/verify": slice(["two"]) });
    expect(builds(h).map((c) => [c.key, c.subtask])).toEqual([
      ["runstep:c-1:a/coder", "wf:wf_1:a/coder"],
      ["runstep:c-1:a/verify", "wf:wf_1:a/verify"],
    ]);
    // The render key is NOT the real message id: `messages-blocks.ts` holds one
    // render map keyed by message id and the transcript already holds an entry for
    // those messages, so a shared key would have the two surfaces clobber each
    // other's state and dispose the wrong one.
    expect(builds(h)[0]?.key).not.toBe("m1");
  });

  it("hands each path the host the consumer supplied for it", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]) });
    expect(builds(h)[0]?.host).toBe(h.host("a/coder"));
  });

  // The dispatcher's incremental update appends past a watermark, so it is correct
  // only while the prefix it mounted is unchanged. A rebuild here would throw away
  // the reader's place on every streamed chunk.
  it("updates rather than rebuilds when the blocks merely grew", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]) });
    m.calls.length = 0;
    h.apply({ "a/coder": slice(["one", "two"]) });
    expect(h.calls.map((c) => c.fn)).toEqual(["update"]);
    expect(h.calls[0]?.blockCount).toBe(2);
  });

  // ...and the inverse. A shortened or reordered shape is a rewind, a refetch or a
  // turn restructured underneath, which the append-past-a-watermark update cannot
  // represent: it would leave the old prefix on screen under the new tail.
  it("disposes, clears the host and rebuilds when the shape moved", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one", "two"]) });
    const host = h.host("a/coder");
    expect(host.childElementCount).toBe(1);
    m.calls.length = 0;
    h.apply({ "a/coder": reshaped() });
    expect(h.calls.map((c) => c.fn)).toEqual(["dispose", "build"]);
    // The rebuild started from an empty host: exactly one child, the new build's.
    expect(host.childElementCount).toBe(1);
  });

  // The FIRST apply cannot update, whatever the shape says: nothing is mounted for
  // the watermark to extend, and an empty prefix trivially extends anything. The
  // dispose ahead of it is the rebuild arm's unconditional clear — a no-op for a key
  // nothing is registered under, and the one call that guarantees no render from an
  // earlier page instance is left holding this key.
  it("builds on the first apply even though an empty shape extends everything", () => {
    const h = harness();
    h.apply({ "a/coder": slice([]) });
    expect(h.calls.map((c) => c.fn)).toEqual(["dispose", "build"]);
  });

  // Every later repaint of a finished run lands here too, so the latch is what
  // keeps one seal from becoming dozens — each of which re-flushes the markdown
  // streams and re-collapses the reasoning traces.
  it("finalizes exactly once across repeated settled applies", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]) }, false);
    h.apply({ "a/coder": slice(["one", "two"]) }, false);
    h.apply({ "a/coder": slice(["one", "two"]) }, false);
    expect(h.calls.filter((c) => c.fn === "finalize")).toHaveLength(1);
  });

  it("does not finalize while the launching chat is still live", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]) }, true);
    expect(h.calls.some((c) => c.fn === "finalize")).toBe(false);
  });

  // A rebuild resets the latch: the render it sealed is gone, so the new one has to
  // be sealed too or a settled step sits under a caret forever.
  it("re-seals after a rebuild", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one", "two"]) }, false);
    m.calls.length = 0;
    h.apply({ "a/coder": reshaped() }, false);
    expect(h.calls.map((c) => c.fn)).toEqual(["dispose", "build", "finalize"]);
  });

  it("passes the liveness through to the dispatcher", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]) }, false);
    expect(builds(h)[0]?.live).toBe(false);
  });

  // The tab's retarget: every render this stream registered has to be released, or
  // the previous run's disposers stay alive under the next run's page.
  it("disposes every path it holds", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]), "a/verify": slice(["two"]) });
    m.calls.length = 0;
    h.dispose();
    expect(h.calls.map((c) => [c.fn, c.key, c.subtask])).toEqual([
      ["dispose", "runstep:c-1:a/coder", "wf:wf_1:a/coder"],
      ["dispose", "runstep:c-1:a/verify", "wf:wf_1:a/verify"],
    ]);
  });

  it("disposes nothing twice", () => {
    const h = harness();
    h.apply({ "a/coder": slice(["one"]) });
    h.dispose();
    m.calls.length = 0;
    h.dispose();
    expect(h.calls).toEqual([]);
  });

  // The SOURCE FLIP. A chat's window can be evicted while the run tab is open, which
  // empties the slice and hands the path to the on-demand read; reopening the chat
  // reverses it. The render key carries the source, so the previous render has to be
  // disposed and rebuilt — an update would leave it registered under a key nothing
  // ever disposes, holding its store subscriptions alive under the new one.
  it("rebuilds when a path changes source, and releases the previous render", () => {
    const h = harness();
    h.applyFrom({ kind: "chat", chatID: "c-1" }, { "a/coder": slice(["one"]) });
    m.calls.length = 0;

    h.applyFrom({ kind: "kas" }, { "a/coder": slice(["one"], false) });

    // The OLD key is disposed and the NEW key is built. An update would show one
    // "update" call under the old key and no dispose at all.
    expect(h.calls.map((c) => [c.fn, c.key])).toEqual([
      ["dispose", "runstep:c-1:a/coder"],
      ["dispose", "runstep:kas:a/coder"],
      ["build", "runstep:kas:a/coder"],
      ["finalize", "runstep:kas:a/coder"],
    ]);
  });

  // The other direction, and the one that proves the flip is not one-way: a chat
  // reopening must take the path back off the read.
  it("rebuilds when a path returns to the chat route", () => {
    const h = harness();
    h.applyFrom({ kind: "kas" }, { "a/coder": slice(["one"], false) });
    m.calls.length = 0;

    h.applyFrom({ kind: "chat", chatID: "c-1" }, { "a/coder": slice(["one"]) });

    expect(h.calls.filter((c) => c.fn === "build").map((c) => c.key)).toEqual([
      "runstep:c-1:a/coder",
    ]);
    expect(h.calls.some((c) => c.fn === "dispose" && c.key === "runstep:kas:a/coder")).toBe(true);
  });

  // Growth on ONE source still updates rather than rebuilding, or the flip rule would
  // have thrown away the incremental path it was added beside.
  it("still updates on growth within one source", () => {
    const h = harness();
    h.applyFrom({ kind: "kas" }, { "a/coder": slice(["one"], false) });
    m.calls.length = 0;
    h.applyFrom({ kind: "kas" }, { "a/coder": slice(["one", "two"], false) });
    expect(h.calls.map((c) => c.fn)).toEqual(["update"]);
  });

  // The on-demand read has no chat behind it, and the dispatcher uses that id for
  // real: it keys tool-call signals by it and builds a delegate's page link from it.
  // An empty id is what makes that link correctly absent.
  it("hands the dispatcher no chat id for an on-demand read", () => {
    const h = harness();
    h.applyFrom({ kind: "kas" }, { "a/coder": slice(["one"], false) });
    expect(builds(h)[0]?.chatID).toBe("");
  });

  it("hands the dispatcher the launching chat for a chat-sourced slice", () => {
    const h = harness();
    h.applyFrom({ kind: "chat", chatID: "c-7" }, { "a/coder": slice(["one"]) });
    expect(builds(h)[0]?.chatID).toBe("c-7");
  });

  // Liveness is per ENTRY now, which is the property a page holding both routes
  // needs: a chat-sourced step can still be streaming while an on-demand read beside
  // it is a settled answer.
  it("carries liveness per entry, so one apply can hold both", () => {
    const h = harness();
    const paints = new Map<string, RunStepPaint>([
      [
        "a/live",
        { slice: slice(["streaming"]), source: { kind: "chat", chatID: "c-1" } as RunStepSource },
      ],
      ["a/done", { slice: slice(["settled"], false), source: { kind: "kas" } as RunStepSource }],
    ]);
    // Applied through the stream's own door, because the harness's helpers set one
    // source per call and the property under test is two sources in ONE apply.
    h.applyPaints(paints);
    const built = h.calls.filter((c) => c.fn === "build");
    expect(built.find((c) => c.key.endsWith("a/live"))?.live).toBe(true);
    expect(built.find((c) => c.key.endsWith("a/done"))?.live).toBe(false);
    // And only the settled one is sealed.
    expect(h.calls.filter((c) => c.fn === "finalize").map((c) => c.key)).toEqual([
      "runstep:kas:a/done",
    ]);
  });
});
