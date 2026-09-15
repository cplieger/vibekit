// The bulk cache's contract: collapse duplicate requests, hold nothing a reveal
// does not read, and stay under a stated byte ceiling.
//
// `api-client` is the unmanaged dependency and is the only thing faked; the
// generated decoder is exercised by its own suite, and this file's subject is
// what the module does with an answer once it has one.

import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  paths: [] as string[],
  /** Tool call id -> how to answer it. A thunk so a case can defer a resolution. */
  answers: new Map<string, () => Promise<unknown>>(),
}));

vi.mock("./api-client.js", () => ({
  apiGetTyped: (path: string) => {
    api.paths.push(path);
    const answer = api.answers.get(path.slice(path.lastIndexOf("/") + 1));
    return answer === undefined ? Promise.resolve(null) : answer();
  },
}));

const { toolCallBulk, MAX_RETAINED_BYTES, _resetToolBulkForTest } = await import("./tool-bulk.js");

const CHAT = "c-deadbeef";

/** Half the budget, in code units: two of these fit exactly, a third evicts. */
const HALF_BUDGET_CHARS = MAX_RETAINED_BYTES / 4;

function requests(id: string): number {
  return api.paths.filter((p) => p.endsWith(`/${id}`)).length;
}

/** Answer `id` with an output of `chars` code units, so its charge is `chars * 2`. */
function serve(id: string, chars: number): void {
  api.answers.set(id, () => Promise.resolve({ id, output: "x".repeat(chars) }));
}

beforeEach(() => {
  _resetToolBulkForTest();
  api.paths.length = 0;
  api.answers.clear();
});

describe("toolCallBulk", () => {
  it("collapses concurrent readers onto one request", async () => {
    const gate = Promise.withResolvers<unknown>();
    api.answers.set("t1", () => gate.promise);

    const first = toolCallBulk(CHAT, "t1");
    const second = toolCallBulk(CHAT, "t1");
    expect(second).toBe(first);
    expect(requests("t1")).toBe(1);

    gate.resolve({ id: "t1", output: "done" });
    await first;
  });

  it("answers a later reader from the held bulk", async () => {
    serve("t1", 4);
    await toolCallBulk(CHAT, "t1");
    const again = await toolCallBulk(CHAT, "t1");

    expect(again?.output).toBe("xxxx");
    expect(requests("t1")).toBe(1);
  });

  it("holds only what a reveal renders, never the wire's unread input", async () => {
    api.answers.set("t1", () =>
      Promise.resolve({ id: "t1", output: "out", input: { content: "a written file" } }),
    );

    const bulk = await toolCallBulk(CHAT, "t1");

    expect(bulk).toEqual({ output: "out", outputSpans: [], diffs: [] });
    expect(Object.keys(bulk ?? {})).not.toContain("input");
  });

  it("does not hold a failed fetch, so a re-open retries", async () => {
    api.answers.set("t1", () => Promise.resolve(null));

    expect(await toolCallBulk(CHAT, "t1")).toBeNull();
    expect(await toolCallBulk(CHAT, "t1")).toBeNull();
    expect(requests("t1")).toBe(2);
  });

  it("evicts the least recently read bulk once the budget is exceeded", async () => {
    for (const id of ["t1", "t2", "t3"]) {
      serve(id, HALF_BUDGET_CHARS);
      await toolCallBulk(CHAT, id);
    }

    // t1 and t2 filled the budget exactly; t3 pushed it over and took t1 with it.
    await toolCallBulk(CHAT, "t3");
    await toolCallBulk(CHAT, "t2");
    expect(requests("t3")).toBe(1);
    expect(requests("t2")).toBe(1);

    await toolCallBulk(CHAT, "t1");
    expect(requests("t1")).toBe(2);
  });

  it("renews an entry that is read again, so eviction is by read and not by age", async () => {
    serve("t1", HALF_BUDGET_CHARS);
    serve("t2", HALF_BUDGET_CHARS);
    serve("t3", HALF_BUDGET_CHARS);
    await toolCallBulk(CHAT, "t1");
    await toolCallBulk(CHAT, "t2");

    // Reading t1 makes t2 the oldest, so t3 evicts t2 rather than t1.
    await toolCallBulk(CHAT, "t1");
    await toolCallBulk(CHAT, "t3");

    await toolCallBulk(CHAT, "t1");
    expect(requests("t1")).toBe(1);
    await toolCallBulk(CHAT, "t2");
    expect(requests("t2")).toBe(2);
  });

  it("never evicts a request still in flight", async () => {
    const gate = Promise.withResolvers<unknown>();
    api.answers.set("t1", () => gate.promise);
    const inFlight = toolCallBulk(CHAT, "t1");

    for (const id of ["t2", "t3", "t4"]) {
      serve(id, HALF_BUDGET_CHARS);
      await toolCallBulk(CHAT, id);
    }

    expect(toolCallBulk(CHAT, "t1")).toBe(inFlight);
    expect(requests("t1")).toBe(1);

    gate.resolve({ id: "t1", output: "done" });
    await inFlight;
  });

  it("answers a bulk bigger than the whole budget without holding it or the rest", async () => {
    serve("small", 4);
    await toolCallBulk(CHAT, "small");
    const oversizedChars = MAX_RETAINED_BYTES / 2 + 1;
    serve("huge", oversizedChars);

    const bulk = await toolCallBulk(CHAT, "huge");
    expect(bulk?.output.length).toBe(oversizedChars);

    await toolCallBulk(CHAT, "huge");
    expect(requests("huge")).toBe(2);

    await toolCallBulk(CHAT, "small");
    expect(requests("small")).toBe(1);
  });

  it("charges style spans, so an output's spans cannot escape the budget", async () => {
    const spans = Array.from({ length: MAX_RETAINED_BYTES / 48 + 1 }, (_v, i) => ({
      start: i,
      end: i + 1,
      fg: -1,
      bg: -1,
      attrs: 0,
    }));
    api.answers.set("t1", () => Promise.resolve({ id: "t1", output: "", output_spans: spans }));
    serve("t2", 4);

    await toolCallBulk(CHAT, "t1");
    await toolCallBulk(CHAT, "t2");

    // t1's spans alone exceed the budget, so it was answered and not held.
    await toolCallBulk(CHAT, "t1");
    expect(requests("t1")).toBe(2);
    await toolCallBulk(CHAT, "t2");
    expect(requests("t2")).toBe(1);
  });

  it("asks for nothing when either id is empty", async () => {
    expect(await toolCallBulk("", "t1")).toBeNull();
    expect(await toolCallBulk(CHAT, "")).toBeNull();
    expect(api.paths).toEqual([]);
  });
});
