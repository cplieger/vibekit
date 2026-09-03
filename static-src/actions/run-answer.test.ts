// ---------------------------------------------------------------------------
// The error copy for `runs.answer_input`, which is the one run action whose
// server sentence is worth more than any fixed string.
//
// The refusal that actually happens is a 409: the server claims the ask BEFORE it
// sends, so a second surface answering one gets "that question has already been
// answered, or the step it belonged to has moved on" — which tells the reader their
// answer was not needed rather than that it failed, and there is nothing for them
// to redo.
//
// A static `error` string does not REPLACE that sentence, it PREFIXES it
// (`emitErrorToast` builds `${spec}: ${err.message}`), which is the opposite of what
// it looks like and is why the defect is a contradiction rather than a loss: the
// toast asserted a failure and then explained that nothing needed sending.
//
// The other half is why a fixed sentence is still needed: `@cplieger/fetch` fills
// `message` either way, with the literal `HTTP <status>` when the body was empty or
// unparseable, and with a browser sentence about a fetch on a transport failure
// (`status === 0`). So the presence of the field proves nothing and both empty cases
// have to be recognised — the second of which is what the prefix was leaking as
// "…to the step: HTTP 500".
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiGetTyped: vi.fn().mockResolvedValue(null),
}));

import { error as toastError } from "../toast.js";
import { answerRunInput } from "./runs.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

const mockFetch = vi.fn();
const mockToastError = vi.mocked(toastError);

const FALLBACK = "Couldn't send your answer to the step";
const CONFLICT = "that question has already been answered, or the step it belonged to has moved on";

beforeEach(() => {
  resetActionFramework();
  mockToastError.mockClear();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Dispatch one answer against a canned response and return the toast text. */
async function answerAgainst(res: () => Response | Promise<Response>): Promise<string> {
  mockFetch.mockImplementation(res);
  await answerRunInput.dispatch({ workflowID: "wf_1", ask_id: "a1", text: "the main branch" });
  return (mockToastError.mock.calls.at(-1)?.[0] ?? "") as string;
}

describe("runs.answer_input error copy", () => {
  it("shows the server's sentence on the 409 that actually happens", async () => {
    const msg = await answerAgainst(
      () => new Response(JSON.stringify({ error: CONFLICT }), { status: 409 }),
    );
    expect(msg).toBe(CONFLICT);
  });

  it("shows the server's sentence on a 400 too", async () => {
    const msg = await answerAgainst(
      () =>
        new Response(JSON.stringify({ error: "the step that asked cannot be addressed" }), {
          status: 400,
        }),
    );
    expect(msg).toBe("the step that asked cannot be addressed");
  });

  it("falls back when the body carried no sentence", async () => {
    // fetch's own placeholder, not a server message: showing `HTTP 500` to a reader
    // is worse than the fixed sentence, because it reads like a value they can act on.
    const msg = await answerAgainst(() => new Response("", { status: 500 }));
    expect(msg).toBe(FALLBACK);
  });

  it("falls back on a transport failure, whose message is about a fetch", async () => {
    const msg = await answerAgainst(() => Promise.reject(new Error("Failed to fetch")));
    expect(msg).toBe(FALLBACK);
  });
});
