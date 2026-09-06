// `confirmChatExists` over the REAL transport and the REAL decoder.
//
// Its sibling suite in store-load.test.ts mocks `apiGetTypedOrError`, which is the
// right seam for the verdict rules — but it means the fixture it hands in as
// `r.data.chat` is the same object the assertion reads back, so
// `decodeChatConfirmResponseLocal` is never invoked and `@cplieger/fetch` never
// runs. A wrong key in that decoder would leave every one of those cases green
// while production took status 200 plus `code: "decode"` on every confirmation and
// answered `unresolved` forever: a chat that exists would dead-end permanently, and
// the only trace would be one `console.warn`. Verified correct by inspection at the
// time, which is exactly the kind of claim a test is for.
//
// So this file mocks NOTHING between `confirmChatExists` and the socket. It stubs
// the global `fetch` — the last thing before the network — and lets the real
// api-client, the real `@cplieger/fetch` and the real generated `decodeChatHeader`
// run over real `Response` objects. `@cplieger/fetch` resolves `cfg.fetchFn ?? fetch`
// at CALL time, so the stub is what the request reaches.
//
// It also closes the other half of the same gap: the status mapping is pinned here
// against genuine 404 / 500 / 204 responses rather than against a hand-built
// `ApiResult` envelope, so a change to how the library reports a status is caught
// rather than mocked past.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { confirmChatExists } from "./store-load.js";

const { mockUpsertHeader } = vi.hoisted(() => ({ mockUpsertHeader: vi.fn() }));

// The store is mocked because this file's subject is the WIRE, not adoption — and
// `upsertHeader` is spied rather than stubbed away, because the value it receives is
// the decoder's OUTPUT and is therefore the assertion that proves the decoder ran.
vi.mock("./store.js", () => ({
  get: () => undefined,
  getSessions: () => [],
  setSessions: vi.fn(),
  upsertHeader: mockUpsertHeader,
  rebuildMsgIndex: vi.fn(),
  bumpMessages: vi.fn(),
  normalizeMessage: (m: unknown) => m,
  liveTurnMessage: () => undefined,
  relatchTurnVerdict: vi.fn(),
  latchFieldsFor: () => ({}),
  syncEpoch: () => 0,
}));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

/** Every URL the stub was asked for, so a case can assert the request as well as
 *  the verdict. */
let asked: string[] = [];

/** Answer the next fetch with a real Response. */
function answerWith(status: number, body: string | null, contentType = "application/json"): void {
  vi.stubGlobal("fetch", (url: unknown) => {
    asked.push(String(url));
    return Promise.resolve(
      new Response(body, {
        status,
        headers: body === null ? {} : { "content-type": contentType },
      }),
    );
  });
}

/** A response body exactly as `serveChatMessages` writes one:
 *  `map[string]any{"chat": c.Header(), "messages": …, "has_more": …}`.
 *
 *  The chat object carries every field `vibekit.ChatHeader` marshals WITHOUT
 *  `omitempty` — `name`, `id`, `usage`, `created_at`, `updated_at`,
 *  `message_count`, and `usage`'s own six — because the generated `decodeChatHeader`
 *  requires exactly those and this file is the only place that runs it. That
 *  requirement is not incidental: the FIRST version of this fixture was the
 *  `{id, name, message_count, usage: {}}` object the mocked suite hands in, and the
 *  real decoder REJECTED it. A shape a mock passes straight through is not evidence
 *  the wire shape is right, which is the whole reason this file exists.
 *
 *  The sibling keys are present on purpose — the confirm decoder is deliberately
 *  narrower than `decodeChatGetResponseLocal` and must ignore them rather than
 *  require them, since every required field is one more way an answer that DID
 *  arrive gets discarded as undecodable. */
function serverBody(id: string): string {
  return JSON.stringify({
    chat: {
      id,
      name: "Made elsewhere",
      created_at: 1_730_000_000,
      updated_at: 1_730_000_500,
      message_count: 3,
      usage: {
        context_pct: 12,
        context_size: 200_000,
        credits: 0.5,
        turn_count: 2,
        last_turn_ms: 4200,
        has_real_data: true,
      },
    },
    messages: [],
    has_more: false,
    draft: "",
    turn_open: false,
  });
}

beforeEach(() => {
  asked = [];
  mockUpsertHeader.mockClear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("confirmChatExists over the real transport", () => {
  it("decodes the server's own body shape and adopts the header off it", async () => {
    answerWith(200, serverBody("c-elsewhere"));

    expect(await confirmChatExists("c-elsewhere")).toBe("exists");
    // The killing assertion: this value came out of the real decoder, so it is
    // proof the decoder read the key the handler writes rather than proof the
    // fixture equals itself.
    expect(mockUpsertHeader).toHaveBeenCalledTimes(1);
    expect(mockUpsertHeader.mock.calls[0]?.[0]).toMatchObject({
      id: "c-elsewhere",
      name: "Made elsewhere",
      message_count: 3,
    });
    expect(asked).toEqual(["/api/chats/c-elsewhere?limit=1"]);
  });

  it("REFUSES the claim for a 2xx body under any other key", async () => {
    // The mutant the mocked suite cannot express. A decoder keyed on anything but
    // `chat` throws, the transport reports status 200 with `code: "decode"`, and the
    // verdict is `unresolved` — so this is the case that separates "the decoder is
    // correct" from "the decoder is never called".
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    answerWith(
      200,
      JSON.stringify({ session: { id: "c-elsewhere", name: "x", message_count: 0, usage: {} } }),
    );

    expect(await confirmChatExists("c-elsewhere")).toBe("unresolved");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("REFUSES the claim for a 2xx whose chat is not an object", async () => {
    // The generated `decodeChatHeader` is what rejects this, so the case also pins
    // that the confirm path goes through it rather than casting.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    answerWith(200, JSON.stringify({ chat: "c-elsewhere" }));

    expect(await confirmChatExists("c-elsewhere")).toBe("unresolved");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });

  it("REFUSES the claim for a 2xx that is not JSON at all", async () => {
    // A proxy's HTML error page served with a 200 is the live shape of this, and it
    // is a no-answer rather than a statement about the chat.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    answerWith(200, "<html>gateway</html>", "text/html");

    expect(await confirmChatExists("c-elsewhere")).toBe("unresolved");
  });

  it("answers `gone` for a real 404 response", async () => {
    // The status mapping against a genuine Response rather than a fixture envelope:
    // `parseErrorResponse` carries the real HTTP status, which is what makes the
    // status test in `confirmChatExists` reachable at all.
    answerWith(404, JSON.stringify({ error: "chat not found" }));

    expect(await confirmChatExists("c-deleted")).toBe("gone");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });

  it("REFUSES the claim for a real 500 response", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    answerWith(500, JSON.stringify({ error: "internal error" }));

    expect(await confirmChatExists("c-real")).toBe("unresolved");
  });

  it("REFUSES the claim when the fetch itself throws", async () => {
    // The whole no-response family reaches the client as status 0, and this is the
    // one shape a mocked envelope can only assert by construction.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    vi.stubGlobal("fetch", () => Promise.reject(new TypeError("Failed to fetch")));

    expect(await confirmChatExists("c-real")).toBe("unresolved");
  });

  it("REFUSES the claim for a 204 with no body", async () => {
    // An empty 2xx never reaches the decoder — the transport returns `data:
    // undefined`, the api-client collapses it to null, and the verdict falls through
    // to the status test, which 204 fails. An absent body is not a statement either.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    answerWith(204, null);

    expect(await confirmChatExists("c-real")).toBe("unresolved");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });
});
