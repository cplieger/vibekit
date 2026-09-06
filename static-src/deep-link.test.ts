// `settleDeepLinkedChat`: what a pasted `/chat/<id>` the store has no row for does.
//
// All four of these rules used to live inside `applyRoute`, a private function in
// the composition root, so none of them had a test address — and three of the four
// are about EVIDENCE rather than routing:
//
//  - the ask gate (is there evidence the server cannot answer at all),
//  - the URL guard (does a verdict that arrived a round trip late still describe
//    the screen),
//  - the notice gate (is an unanswered ask this reader's first notice or a second
//    copy of one boot already raised).
//
// `router.js` is REAL here, and deliberately: the URL guard's whole subject is the
// browser's location, so a mocked `parseRoute` would leave the test asserting
// against its own fixture. Each case drives `history.replaceState` the way a tab
// click or a back press does and reads the location back afterwards.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { settleDeepLinkedChat } from "./deep-link.js";

const { mockResolve, mockListLoaded, mockMayAnswer, mockToastError, mockActiveTabRoute } =
  vi.hoisted(() => ({
    mockResolve: vi.fn(),
    mockListLoaded: vi.fn(() => true),
    mockMayAnswer: vi.fn(() => true),
    mockToastError: vi.fn(),
    mockActiveTabRoute: vi.fn(() => null as unknown),
  }));

vi.mock("./chat.js", () => ({ resolveUnknownChat: mockResolve }));
vi.mock("./store-load.js", () => ({
  chatListLoaded: mockListLoaded,
  serverMayAnswer: mockMayAnswer,
}));
// The async factory with a dynamic import is the house form: a `vi.mock` factory is
// hoisted above every top-level statement, so a static import of the helper is not
// initialized when it runs.
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabRoute: mockActiveTabRoute,
}));
vi.mock("./toast.js", () => ({ error: mockToastError }));

/** The location the reader is at, driven the way the app drives it. */
function at(path: string): void {
  history.replaceState(null, "", path);
}

/** The retry the notice offered, or undefined when it offered none. */
function offeredRetry(): (() => void) | undefined {
  const retry = mockToastError.mock.calls[0]?.[1] as { onClick?: () => void } | undefined;
  return retry?.onClick;
}

let origin = "";

beforeEach(() => {
  origin = location.pathname + location.search + location.hash;
  vi.clearAllMocks();
  mockListLoaded.mockReturnValue(true);
  mockMayAnswer.mockReturnValue(true);
  mockActiveTabRoute.mockReturnValue(null);
});

afterEach(() => {
  // The runner's own page: leaving it on /chat/... breaks whatever loads next.
  history.replaceState(null, "", origin);
});

describe("settleDeepLinkedChat", () => {
  it("opens the chat and says nothing when the server knows it", async () => {
    // The direction the round trip exists for. A refusal inside `resolveUnknownChat`
    // raises its own notice through the action framework, so a second one here would
    // report one refusal twice.
    at("/chat/c-elsewhere");
    mockResolve.mockResolvedValue("opened");

    expect(await settleDeepLinkedChat("c-elsewhere")).toBe("opened");
    expect(mockToastError).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/chat/c-elsewhere");
  });

  it("canonicalizes the URL and says so when the SERVER says the chat is gone", async () => {
    // The one terminal outcome, and the only thing that licenses it is the server
    // having read its own store.
    at("/chat/c-deleted");
    mockActiveTabRoute.mockReturnValue({ kind: "chat", id: "c-open" });
    mockResolve.mockResolvedValue("gone");

    expect(await settleDeepLinkedChat("c-deleted")).toBe("gone");
    expect(location.pathname).toBe("/chat/c-open");
    expect(mockToastError).toHaveBeenCalledExactlyOnceWith("That conversation no longer exists.");
  });

  it("falls back to the empty chat route when no tab is active", async () => {
    at("/chat/c-deleted");
    mockResolve.mockResolvedValue("gone");

    expect(await settleDeepLinkedChat("c-deleted")).toBe("gone");
    expect(location.pathname).toBe("/");
  });

  // -------------------------------------------------------------------------
  // The `unresolved` arm. This was SILENT: a confirmation that 5xx'd, timed out
  // or died on the network correctly refused the terminal claim and then raised
  // nothing at all, so a reader pasted a link, the page did nothing, and there was
  // no explanation and no way to try again. Unlike the failed-boot arm there is no
  // boot toast standing behind it, because the chat list loaded fine.
  // -------------------------------------------------------------------------

  it("RAISES a non-terminal notice with a retry when nobody answered", async () => {
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");

    expect(await settleDeepLinkedChat("c-real")).toBe("unresolved");
    expect(mockToastError).toHaveBeenCalledTimes(1);
    expect(offeredRetry()).toBeTypeOf("function");
  });

  it("does NOT claim the chat is gone in that notice", async () => {
    // The defect being avoided, asserted as a property of the words rather than by
    // pinning the sentence: the server failed to answer, so anything terminal here
    // is the false claim this whole path refuses to make.
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");

    await settleDeepLinkedChat("c-real");
    const said = String(mockToastError.mock.calls[0]?.[0]);
    expect(said).not.toMatch(/no longer exists|deleted|gone/i);
    expect(said).toMatch(/didn't answer|did not answer/i);
  });

  it("HOLDS the URL, so the retry and a reload both still address the chat", async () => {
    // Canonicalizing here would point the URL at the fallback view, and then the
    // retry — and a reload, and a re-share of the same link — would ask about
    // whatever the reader landed on instead of the chat they asked for.
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");

    await settleDeepLinkedChat("c-real");
    expect(location.pathname).toBe("/chat/c-real");
  });

  it("re-ASKS the server on retry rather than reloading the page", async () => {
    // A reload throws away the live SSE connection, every open tab's state and the
    // transcript underneath, to repeat one GET. The retry re-enters the same door,
    // so a server that has recovered opens the chat.
    at("/chat/c-real");
    mockResolve.mockResolvedValue("unresolved");
    await settleDeepLinkedChat("c-real");
    expect(mockResolve).toHaveBeenCalledTimes(1);

    mockResolve.mockResolvedValue("opened");
    offeredRetry()?.();
    await vi.waitFor(() => {
      expect(mockResolve).toHaveBeenCalledTimes(2);
    });
    expect(mockResolve).toHaveBeenLastCalledWith("c-real");
  });

  it("stays SILENT on an unresolved when boot has already raised a notice", async () => {
    // The non-duplication rule. Boot toasts "Couldn't load your chats." whenever its
    // own load failed, and a load that failed did not latch — so an unlatched list
    // means the reader is already holding a notice about this server and this would
    // be the second copy of it. The URL is held either way.
    at("/chat/c-real");
    mockListLoaded.mockReturnValue(false);
    mockResolve.mockResolvedValue("unresolved");

    expect(await settleDeepLinkedChat("c-real")).toBe("held");
    expect(mockToastError).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/chat/c-real");
  });

  // -------------------------------------------------------------------------
  // The ask gate. Round 3's fix, preserved exactly: a reload of any `/chat/<id>`
  // against a restarting server holds the URL and stays quiet, and does not spend a
  // round trip to be told what boot's own toast already said.
  // -------------------------------------------------------------------------

  it("does not ask at all when there is evidence the server cannot answer", async () => {
    at("/chat/c-real");
    mockMayAnswer.mockReturnValue(false);

    expect(await settleDeepLinkedChat("c-real")).toBe("held");
    expect(mockResolve).not.toHaveBeenCalled();
    expect(mockToastError).not.toHaveBeenCalled();
    expect(location.pathname).toBe("/chat/c-real");
  });

  // -------------------------------------------------------------------------
  // The URL guard. The answer arrives a round trip after the route was applied, and
  // in that window a tab click or a back press moves the location. Acting on a
  // verdict about an older location is the same stale-answer defect the verdict
  // itself exists to remove, one layer up.
  // -------------------------------------------------------------------------

  it("drops a late `gone` verdict when the reader has moved to another chat", async () => {
    at("/chat/c-asked");
    mockActiveTabRoute.mockReturnValue({ kind: "chat", id: "c-open" });
    mockResolve.mockImplementation(() => {
      // The move happens WHILE the request is in flight, which is the only window
      // this guard exists for.
      at("/chat/c-moved-on");
      return Promise.resolve("gone");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("stale");
    expect(location.pathname).toBe("/chat/c-moved-on");
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("drops a late `gone` verdict when the reader has moved to another VIEW", async () => {
    // The guard compares the KIND as well as the id: a reader who left for /files is
    // not owed a claim about a conversation, and rewriting that URL would take them
    // off the view they chose.
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/files/src");
      return Promise.resolve("gone");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("stale");
    expect(location.pathname).toBe("/files/src");
  });

  it("drops a late `unresolved` verdict too, so no notice chases an abandoned link", async () => {
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/chat/c-moved-on");
      return Promise.resolve("unresolved");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("stale");
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("acts on a verdict for the SAME id even if the URL was rewritten to itself", async () => {
    // The guard must not be so strict that an ordinary re-entry looks stale — the
    // comparison is on the id, not on identity of the visit.
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/chat/c-asked");
      return Promise.resolve("gone");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("gone");
    expect(mockToastError).toHaveBeenCalledTimes(1);
  });

  it("opens a chat that resolved while the reader was elsewhere", async () => {
    // The guard gates the URL rewrite and the notice, NOT the open: a chat that
    // exists should end up on the strip whether or not the reader is still looking
    // at its URL, and `resolveUnknownChat` has already opened it by then.
    at("/chat/c-asked");
    mockResolve.mockImplementation(() => {
      at("/files/src");
      return Promise.resolve("opened");
    });

    expect(await settleDeepLinkedChat("c-asked")).toBe("opened");
    expect(location.pathname).toBe("/files/src");
  });
});
