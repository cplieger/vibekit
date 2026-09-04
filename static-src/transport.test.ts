// Property-based tests for transport.ts newRequestID/newMessageID format
// invariants, plus behavioral tests for send() — the failed-response read
// (error + the additive `reason` field), the 409 no-toast rule, and the global
// hidden-abort over in-flight non-prompt requests. The send tests drive a fetch
// fake (this repo does not use MSW here) and a stubbed EventSource for init().

import { describe, it, expect, vi } from "vitest";
import fc from "fast-check";

// Observe the failure toast without painting one: the 409 carve-out is ABOUT
// which failures reach this surface.
vi.mock("./failure-notice.js", () => ({
  reportFailure: vi.fn(),
  clearFailure: vi.fn(),
  _resetForTest: vi.fn(),
}));

import { reportFailure } from "./failure-notice.js";
import {
  newRequestID,
  newMessageID,
  newOpID,
  computeBackoff,
  send,
  init,
  BACKOFF_CAP_MS,
} from "./transport.js";

const VALID_CHARS = /^[a-z0-9-]+$/;

describe("newRequestID property invariants", () => {
  it("always starts with r- prefix", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return newRequestID().startsWith("r-");
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("contains only [a-z0-9-] characters", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return VALID_CHARS.test(newRequestID());
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("produces unique IDs over 1000 calls", () => {
    expect.assertions(1);
    const ids = new Set<string>();
    for (let i = 0; i < 1000; i++) {
      ids.add(newRequestID());
    }
    expect(ids.size).toBe(1000);
  });

  it("has structure: r- prefix, base-36 timestamp, separator, entropy", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const id = newRequestID();
        // Structure: "r-" + timestamp(base36) + "-" + entropy
        const withoutPrefix = id.slice(2);
        const dashIdx = withoutPrefix.indexOf("-");
        // Must have a dash separating timestamp from entropy
        if (dashIdx < 1) {
          return false;
        }
        const timestamp = withoutPrefix.slice(0, dashIdx);
        const entropy = withoutPrefix.slice(dashIdx + 1);
        // Timestamp segment must be non-empty base-36
        if (timestamp.length === 0 || !/^[a-z0-9]+$/.test(timestamp)) {
          return false;
        }
        // Entropy segment must be non-empty base-36
        if (entropy.length === 0 || !/^[a-z0-9]+$/.test(entropy)) {
          return false;
        }
        return true;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("newMessageID property invariants", () => {
  it("always starts with m- prefix", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return newMessageID().startsWith("m-");
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("contains only [a-z0-9-] characters", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        return VALID_CHARS.test(newMessageID());
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("produces unique IDs over 1000 calls", () => {
    expect.assertions(1);
    const ids = new Set<string>();
    for (let i = 0; i < 1000; i++) {
      ids.add(newMessageID());
    }
    expect(ids.size).toBe(1000);
  });

  it("has same structure as newRequestID but with m- prefix", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const id = newMessageID();
        const withoutPrefix = id.slice(2);
        const dashIdx = withoutPrefix.indexOf("-");
        if (dashIdx < 1) {
          return false;
        }
        const timestamp = withoutPrefix.slice(0, dashIdx);
        const entropy = withoutPrefix.slice(dashIdx + 1);
        if (timestamp.length === 0 || !/^[a-z0-9]+$/.test(timestamp)) {
          return false;
        }
        if (entropy.length === 0 || !/^[a-z0-9]+$/.test(entropy)) {
          return false;
        }
        return true;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("computeBackoff property invariants", () => {
  it("delay is always in [0, backoffMs)", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.nat(60_000), (prev) => {
        const { delay, backoffMs } = computeBackoff(prev);
        return delay >= 0 && delay < backoffMs;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("backoffMs doubles from previous (capped at BACKOFF_CAP_MS)", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.nat(60_000), (prev) => {
        const { backoffMs } = computeBackoff(prev);
        if (prev === 0) {
          return backoffMs === 500;
        }
        const expected = Math.min(prev * 2, BACKOFF_CAP_MS);
        return backoffMs === expected;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("backoffMs never exceeds BACKOFF_CAP_MS", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.nat(100_000), (prev) => {
        const { backoffMs } = computeBackoff(prev);
        return backoffMs <= BACKOFF_CAP_MS;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("sequence from 0 is monotonically non-decreasing in backoffMs", () => {
    let prev = 0;
    for (let i = 0; i < 20; i++) {
      const { backoffMs } = computeBackoff(prev);
      expect(backoffMs).toBeGreaterThanOrEqual(prev === 0 ? 0 : prev);
      prev = backoffMs;
    }
  });

  it("first call from 0 yields backoffMs=500", () => {
    const { backoffMs } = computeBackoff(0);
    expect(backoffMs).toBe(500);
  });
});

describe("computeBackoff sequence-level invariants", () => {
  it("arbitrary prev-value sequences maintain monotonicity and bounds", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.array(fc.nat(60_000), { minLength: 2, maxLength: 50 }), (prevValues) => {
        for (const prev of prevValues) {
          const { delay, backoffMs } = computeBackoff(prev);
          // Invariant 1: backoffMs never exceeds BACKOFF_CAP_MS
          if (backoffMs > BACKOFF_CAP_MS) {
            return false;
          }
          // Invariant 2: delay is always in [0, backoffMs)
          if (delay < 0 || delay >= backoffMs) {
            return false;
          }
          // Invariant 3: after a successful reconnect (prev=0), resets to 500ms
          if (prev === 0 && backoffMs !== 500) {
            return false;
          }
        }
        return true;
      }),
      { numRuns: 200 },
    );
    expect(result.failed).toBe(false);
  });

  it("simulated reconnect sequences with resets maintain invariants", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.array(fc.boolean(), { minLength: 5, maxLength: 30 }), (resetPattern) => {
        let prev = 0;
        for (const shouldReset of resetPattern) {
          if (shouldReset) {
            prev = 0;
          }
          const { delay, backoffMs } = computeBackoff(prev);
          if (backoffMs > BACKOFF_CAP_MS) {
            return false;
          }
          if (delay < 0 || delay >= backoffMs) {
            return false;
          }
          if (prev === 0 && backoffMs !== 500) {
            return false;
          }
          prev = backoffMs;
        }
        return true;
      }),
      { numRuns: 200 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("newMessageID/newRequestID structural equivalence", () => {
  it("newMessageID suffix has same format as newRequestID suffix", () => {
    expect.assertions(1);
    const suffixPattern = /^[a-z0-9]+-[a-z0-9]+$/;
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const mid = newMessageID();
        const rid = newRequestID();
        if (!mid.startsWith("m-")) {
          return false;
        }
        if (!rid.startsWith("r-")) {
          return false;
        }
        // Both suffixes must match timestamp-entropy structure
        if (!suffixPattern.test(mid.slice(2))) {
          return false;
        }
        if (!suffixPattern.test(rid.slice(2))) {
          return false;
        }
        return true;
      }),
      { numRuns: 50 },
    );
    expect(result.failed).toBe(false);
  });

  it("newMessageID and newRequestID share the same character class after prefix", () => {
    expect.assertions(1);
    const charClass = /^[a-z0-9-]+$/;
    const result = fc.check(
      fc.property(fc.constant(null), () => {
        const mid = newMessageID();
        const rid = newRequestID();
        return charClass.test(mid.slice(2)) && charClass.test(rid.slice(2));
      }),
      { numRuns: 50 },
    );
    expect(result.failed).toBe(false);
  });
});

// newOpID is the create gesture's correlation id, and it has ONE constraint the
// other two do not: the server validates it with ids.ValidIdent
// (`^[A-Za-z0-9_.-]{1,128}$`, and it may not start with '.' or '-'), because it
// becomes a key in the create ledger's in-memory map. A mint the boundary rejects
// would 400 every create.
describe("newOpID", () => {
  it("passes the server's identifier gate", () => {
    const ident = /^[A-Za-z0-9_.-]{1,128}$/;
    for (let i = 0; i < 500; i++) {
      const id = newOpID();
      expect(id).toMatch(ident);
      expect(id.startsWith("op-")).toBe(true);
    }
  });

  it("is unique over 1000 calls: one gesture, one id", () => {
    const ids = new Set<string>();
    for (let i = 0; i < 1000; i++) {
      ids.add(newOpID());
    }
    expect(ids.size).toBe(1000);
  });

  // Distinct from the idempotency token on purpose: that one is per DISPATCH and
  // lives in a 5-minute server cache, this one has to survive a retry the user
  // makes minutes later. Sharing a value would tie the two windows together.
  it("is not the same value as a request id", () => {
    expect(newOpID()).not.toBe(newRequestID());
  });
});

/** One JSON Response, the way the server's writeErr/dispatcher answers. */
function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("send — failed-response read", () => {
  it("lifts `reason` beside the error string on a 409-starting refusal", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse(409, { error: "busy", reason: "starting" }))),
    );

    const r = await send({ type: "cancel", chat_id: "c1" });

    expect(r).toEqual({ ok: false, status: 409, error: "busy", reason: "starting" });
  });

  it("carries no `reason` when the envelope has none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse(409, { error: "busy" }))),
    );

    const r = await send({ type: "cancel", chat_id: "c1" });

    expect(r.status).toBe(409);
    expect(r.error).toBe("busy");
    expect("reason" in r).toBe(false);
  });

  it("never raises the failure toast for a 409, starting variant included", async () => {
    // A plain 409 is the steer handshake and a 409-starting's surface is the
    // send-error face the caller owns — neither is this toast's business.
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse(409, { error: "busy", reason: "starting" }))),
    );

    await send({ type: "cancel", chat_id: "c1" });
    expect(reportFailure).not.toHaveBeenCalled();
  });

  it("still reports a non-409 failure, with the reason lifted all the same", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse(503, { error: "boom", reason: "starting" }))),
    );

    const r = await send({ type: "cancel", chat_id: "c1" });

    expect(r).toEqual({ ok: false, status: 503, error: "boom", reason: "starting" });
    expect(reportFailure).toHaveBeenCalledWith("c1", "boom");
  });

  it("degrades to the bare status line on a non-JSON failure body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response("gateway melted", { status: 502 }))),
    );

    const r = await send({ type: "cancel", chat_id: "c1" });

    expect(r).toEqual({ ok: false, status: 502, error: "HTTP 502" });
  });
});

// The global hidden-abort predates the early-ack prompt and SURVIVES it: the
// prompt POST is short now, but every other in-flight request is still dropped
// when the page has been hidden past the threshold, because iOS freezes the
// socket underneath it and the request would otherwise dangle until the
// 15-minute default timeout. This is the pin that the prompt-timeout change
// did not take the mechanism with it.
describe("hidden-abort over in-flight requests", () => {
  /** The minimal EventSource the transport's init() can hold: init is the only
   *  way to install the visibilitychange listener under test, and the real
   *  constructor would open a stream against the vitest server. */
  class FakeEventSource {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    onopen: ((e: Event) => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    readyState = 0;
    url: string;
    constructor(url: string) {
      this.url = url;
    }
    close(): void {
      this.readyState = FakeEventSource.CLOSED;
    }
  }

  it("aborts a non-prompt request once the page was hidden past the threshold", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    // A fetch that settles only through its abort signal: the assertion below
    // can only pass if the hidden-abort fires (nothing else rejects it, so
    // deleting the mechanism times this test out).
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: RequestInfo | URL, reqInit?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            reqInit?.signal?.addEventListener("abort", () => {
              reject(new DOMException("The operation was aborted.", "AbortError"));
            });
          }),
      ),
    );
    let now = 1_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);

    // visibilityState is an accessor on Document.prototype; shadow it on the
    // instance so both transitions are observable, and restore in finally.
    let visibility: DocumentVisibilityState = "visible";
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    });
    try {
      init(
        () => {
          /* no frames in this test */
        },
        () => {
          /* status unobserved */
        },
      );

      const p = send({ type: "cancel", chat_id: "c1" });

      visibility = "hidden";
      document.dispatchEvent(new Event("visibilitychange"));

      now += 30_000; // HIDDEN_ABORT_MS
      visibility = "visible";
      document.dispatchEvent(new Event("visibilitychange"));

      expect(await p).toEqual({
        ok: false,
        status: 0,
        error: "Request cancelled",
        code: "cancelled",
      });
    } finally {
      Reflect.deleteProperty(document, "visibilityState");
    }
  });

  it("leaves the request alone when the hidden spell was shorter than the threshold", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    let aborted = false;
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: RequestInfo | URL, reqInit?: RequestInit) => {
        reqInit?.signal?.addEventListener("abort", () => {
          aborted = true;
        });
        return Promise.resolve(jsonResponse(200, { ok: true }));
      }),
    );
    let now = 2_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);

    let visibility: DocumentVisibilityState = "visible";
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    });
    try {
      init(
        () => {
          /* no frames in this test */
        },
        () => {
          /* status unobserved */
        },
      );

      const p = send({ type: "cancel", chat_id: "c1" });

      visibility = "hidden";
      document.dispatchEvent(new Event("visibilitychange"));
      now += 29_999; // one ms short of the threshold
      visibility = "visible";
      document.dispatchEvent(new Event("visibilitychange"));

      expect((await p).ok).toBe(true);
      expect(aborted).toBe(false);
    } finally {
      Reflect.deleteProperty(document, "visibilityState");
    }
  });
});

// THE REPLAY CURSOR ON THE URL. EventSource sends `Last-Event-ID` on ITS OWN
// automatic retry and on nothing else, and every reconnect the transport drives
// itself closes the source first — so those reconnects opened a brand-new stream
// with no history and got no replay at all, leaving recovery to the floor/head gap
// arithmetic tripping into a full heal.
describe("the replay cursor", () => {
  /** Every EventSource the transport constructed, in order, so a test can read
   *  which URL each reconnect asked for. */
  function recordingEventSource(): { urls: string[]; sources: FakeSource[] } {
    const urls: string[] = [];
    const sources: FakeSource[] = [];
    class Recording extends FakeSource {
      constructor(url: string) {
        super(url);
        urls.push(url);
        sources.push(this);
      }
    }
    vi.stubGlobal("EventSource", Recording);
    return { urls, sources };
  }

  class FakeSource {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    onopen: ((e: Event) => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    readyState = 0;
    url: string;
    constructor(url: string) {
      this.url = url;
    }
    close(): void {
      this.readyState = FakeSource.CLOSED;
    }
  }

  /** One SSE frame as the browser delivers it: `lastEventId` is what advances the
   *  transport's cursor. */
  function frame(id: number, data: unknown): MessageEvent {
    return new MessageEvent("message", { data: JSON.stringify(data), lastEventId: String(id) });
  }

  it("asks for nothing on a first connection, which has missed nothing", () => {
    const { urls } = recordingEventSource();
    init(
      () => {
        /* frames unobserved */
      },
      () => {
        /* status unobserved */
      },
    );
    expect(urls).toEqual(["/api/events"]);
  });

  it("carries the cursor on a reconnect this module drove itself", () => {
    const { urls, sources } = recordingEventSource();
    init(
      () => {
        /* frames unobserved */
      },
      () => {
        /* status unobserved */
      },
    );
    const first = sources[0];
    expect(first).toBeDefined();
    // Two frames delivered, so the cursor is at 7.
    first?.onmessage?.(frame(5, { type: "chat_updated", chat_id: "c1" }));
    first?.onmessage?.(frame(7, { type: "chat_updated", chat_id: "c2" }));

    // The stream dies terminally, which is the case this module reconnects for:
    // the browser has given up, so its own Last-Event-ID goes with the source.
    if (first !== undefined) {
      first.readyState = FakeSource.CLOSED;
    }
    first?.onerror?.(new Event("error"));
    // The reconnect is behind a backoff timer; kick it directly rather than
    // waiting out a 500ms ramp.
    return vi.waitFor(
      () => {
        expect(urls).toHaveLength(2);
        expect(urls[1]).toBe("/api/events?last_event_id=7");
      },
      { timeout: 3000 },
    );
  });
});
